package bossclear

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// Read reads what Check needs of boss a and of the workers it started.
func Read(c *tmux.Client, a control.Agent, now time.Time) (Boss, []Pane, error) {
	b := Boss{IsBoss: a.Boss}
	p, conversation, err := readPane(c, a, now)
	if err != nil {
		return b, nil, err
	}
	b.Pane, b.Conversation = p, conversation
	// Its status line's (dev statusline)
	used, err := c.Run("display-message", "-p", "-t", a.Pane, "#{"+agent.OptContext+"}")
	if err != nil {
		return b, nil, err
	}
	if n, err := strconv.Atoi(strings.TrimSpace(used)); err == nil {
		b.Context = &n
	}
	shot, err := c.Run("capture-pane", "-p", "-e", "-t", a.Pane)
	if err != nil {
		return b, nil, err
	}
	b.InputEmpty = inbox.InputEmpty(shot)
	box, ok, err := inbox.Here(c, a.Pane)
	if err != nil {
		return b, nil, err
	}
	if ok {
		if b.Queued, err = box.Box.Pending(); err != nil {
			return b, nil, err
		}
	}
	agents, err := control.List(c)
	if err != nil {
		return b, nil, err
	}
	var workers []Pane
	for _, w := range agents {
		if w.StartedBy != a.Session {
			continue
		}
		p, _, err := readPane(c, w, now)
		if err != nil {
			return b, nil, err
		}
		workers = append(workers, p)
	}
	return b, workers, nil
}

// readPane reads a's state (its screen checked), planned wait and rate-limit resume, and its hook's session id.
func readPane(c *tmux.Client, a control.Agent, now time.Time) (Pane, string, error) {
	st, err := control.ReadChecked(c, a.Pane)
	if err != nil {
		return Pane{}, "", err
	}
	p := Pane{Session: a.Session, State: st.Trusted(), WaitingFor: st.WaitingFor}
	if a.Wait != nil {
		p.Wait = a.Wait.Text(now)
	}
	p.Resume, err = inbox.ResumeAhead(c, a.Pane, now)
	return p, st.Session, err
}

// Options are dev agent clear's: Facts the caller's own facts file ("" for none), Force clears under MinContext,
// Stall and Timeout bound each prompt's turn (control.PromptWait), Out gets the progress lines.
type Options struct {
	Facts          string
	Force          bool
	Stall, Timeout time.Duration
	Out            io.Writer
}

// Result is a clear done: the handoff and proof files, the old conversation's transcript and the key facts restated.
type Result struct {
	Handoff, Proof, Transcript string
	Facts                      int
}

// Run clears boss a's context: Check, a fixed save prompt (list, notes, handoff file under
// $XDG_STATE_HOME/dev/handoff/<boss>/), the handoff verified (VerifyHandoff and dev's own list: no item of the user's or
// a person's without details), the checks again, /clear confirmed by the new session (its start hook names the
// handoff: inbox.OptHandoff), then a proof prompt and VerifyProof. A failed verify prompts the boss once with what it
// missed and verifies again. Nothing is cleared before the handoff passes; the handoff, facts copy and transcript stay.
func Run(c *tmux.Client, clk control.Clock, a control.Agent, o Options) (Result, error) {
	var r Result
	if err := check(c, a, o.Force, clk.Now()); err != nil {
		return r, err
	}
	var facts string
	if o.Facts != "" {
		data, err := os.ReadFile(o.Facts)
		if err != nil {
			return r, fmt.Errorf("--facts: %w", err)
		}
		facts = string(data)
	}
	transcript, err := c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_transcript}")
	if err != nil {
		return r, err
	}
	r.Transcript = strings.TrimSpace(transcript)
	dir, err := config.StateDir()
	if err != nil {
		return r, err
	}
	dir = filepath.Join(dir, "handoff", a.Session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return r, err
	}
	stamp := clk.Now().Format("2006-01-02-150405")
	r.Handoff = filepath.Join(dir, stamp+".md")
	r.Proof = filepath.Join(dir, stamp+"-proof.md")
	for _, p := range []string{r.Handoff, r.Proof} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			return r, fmt.Errorf("%s exists already: run it again in a second", p)
		}
	}
	var factsCopy string
	if facts != "" {
		factsCopy = filepath.Join(dir, stamp+"-facts.md")
		if err := os.WriteFile(factsCopy, []byte(facts), 0o600); err != nil {
			return r, err
		}
	}

	fmt.Fprintf(o.Out, "Saving: %s updates its list and notes and writes %s\n", a.Session, r.Handoff)
	if err := prompt(c, clk, a, SavePrompt(r.Handoff, r.Transcript, factsCopy), o); err != nil {
		return r, fmt.Errorf("save: %w; nothing cleared", err)
	}
	h, miss, err := verifyHandoff(c, a, r)
	if err != nil {
		return r, err
	}
	if len(miss) > 0 {
		fmt.Fprintf(o.Out, "Handoff misses: %s; %s fixes it\n", strings.Join(miss, "; "), a.Session)
		if err := prompt(c, clk, a, FixPrompt(r.Handoff, miss), o); err != nil {
			return r, fmt.Errorf("handoff fix: %w; nothing cleared", err)
		}
		if h, miss, err = verifyHandoff(c, a, r); err != nil {
			return r, err
		}
		if len(miss) > 0 {
			return r, fmt.Errorf("handoff %s still misses: %s; nothing cleared", r.Handoff, strings.Join(miss, "; "))
		}
	}
	fmt.Fprintf(o.Out, "Handoff verified: %d key facts\n", len(h.Facts))

	// A worker may have finished or a note come in during the save
	if err := check(c, a, o.Force, clk.Now()); err != nil {
		return r, fmt.Errorf("%w; handoff %s kept", err, r.Handoff)
	}
	if _, err := c.Run("set-option", "-p", "-t", a.Pane, inbox.OptHandoff, r.Handoff); err != nil {
		return r, err
	}
	defer func() { _, _ = c.Run("set-option", "-pu", "-t", a.Pane, inbox.OptHandoff) }()
	if _, err := control.PromptWait(c, clk, a.Pane, "/clear", o.Stall, o.Timeout); err != nil {
		return r, fmt.Errorf("/clear: %w; handoff %s and old transcript %s kept", err, r.Handoff, r.Transcript)
	}
	fmt.Fprintf(o.Out, "Cleared: %s runs a new conversation\n", a.Session)

	proof := ProofPrompt(r.Handoff, r.Proof)
	// manual is what the caller does when the proof step fails
	manual := fmt.Sprintf("handoff %s, old transcript %s; the proof prompt to send by hand: %s", r.Handoff, r.Transcript, proof)
	if err := prompt(c, clk, a, proof, o); err != nil {
		return r, fmt.Errorf("proof: %w; %s", err, manual)
	}
	miss, err = verifyProof(c, a, r, h)
	if err != nil {
		return r, fmt.Errorf("%w; %s", err, manual)
	}
	if len(miss) > 0 {
		fmt.Fprintf(o.Out, "Proof misses: %s; %s fixes it\n", strings.Join(miss, "; "), a.Session)
		if err := prompt(c, clk, a, ProofFixPrompt(r.Handoff, r.Proof, miss), o); err != nil {
			return r, fmt.Errorf("proof fix: %w; %s", err, manual)
		}
		if miss, err = verifyProof(c, a, r, h); err != nil {
			return r, fmt.Errorf("%w; %s", err, manual)
		}
		if len(miss) > 0 {
			return r, fmt.Errorf("proof %s still misses: %s; %s", r.Proof, strings.Join(miss, "; "), manual)
		}
	}
	r.Facts = len(h.Facts)
	return r, nil
}

// check reads boss a (found again: its wait may have changed) and its workers, and runs Check.
func check(c *tmux.Client, a control.Agent, force bool, now time.Time) error {
	a, err := control.Resolve(c, a.Pane)
	if err != nil {
		return err
	}
	b, workers, err := Read(c, a, now)
	if err != nil {
		return err
	}
	return Check(b, workers, force)
}

// prompt sends text to boss a once it is idle with an empty input box, and waits for its turn to end; a turn ending
// on a decision (a permission prompt, a question) is an error: that stays the user's.
func prompt(c *tmux.Client, clk control.Clock, a control.Agent, text string, o Options) error {
	shot, err := c.Run("capture-pane", "-p", "-e", "-t", a.Pane)
	if err != nil {
		return err
	}
	if !inbox.InputEmpty(shot) {
		return fmt.Errorf("%s's input box holds text, maybe the user's draft", a.Session)
	}
	st, err := control.PromptWait(c, clk, a.Pane, text, o.Stall, o.Timeout)
	if err != nil {
		return err
	}
	if agent.Deciding(st.Trusted(), st.WaitingFor) {
		return fmt.Errorf("%s's turn ended on a decision (%s): the user's to answer", a.Session, st.WaitingFor)
	}
	return nil
}

// list is boss a's list as read now, gone workers marked.
func list(c *tmux.Client, a control.Agent) (todo.List, error) {
	s, err := todo.ForPane(c, a.Pane)
	if err != nil {
		return todo.List{}, err
	}
	l, err := s.Read(time.Now())
	if err != nil {
		return l, err
	}
	return l, l.MarkGone(func() ([]todo.Session, error) { return todo.Workers(c, a.Pane) })
}

// verifyHandoff reads r's handoff and boss a's list and checks them: VerifyHandoff against its open items, and no
// item of the user's or a person's without details.
func verifyHandoff(c *tmux.Client, a control.Agent, r Result) (Handoff, []string, error) {
	l, err := list(c, a)
	if err != nil {
		return Handoff{}, nil, err
	}
	data, err := os.ReadFile(r.Handoff)
	if errors.Is(err, fs.ErrNotExist) {
		return Handoff{}, []string{"the file " + r.Handoff + " wasn't written"}, nil
	}
	if err != nil {
		return Handoff{}, nil, err
	}
	var open, bare []int
	for _, it := range l.Items {
		if it.Done {
			continue
		}
		open = append(open, it.ID)
		if it.NeedsDetails() {
			bare = append(bare, it.ID)
		}
	}
	h, miss := VerifyHandoff(string(data), open, r.Transcript)
	if len(bare) > 0 {
		miss = append(miss, "items of the user's or a person's without details on the list: "+numbers(bare))
	}
	return h, miss, nil
}

// verifyProof reads r's proof and checks it against handoff h and boss a's open asks.
func verifyProof(c *tmux.Client, a control.Agent, r Result, h Handoff) ([]string, error) {
	l, err := list(c, a)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(r.Proof)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"the file " + r.Proof + " wasn't written"}, nil
	}
	if err != nil {
		return nil, err
	}
	var asks []int
	for _, it := range l.Items {
		if !it.Done && len(it.Options) > 0 {
			asks = append(asks, it.ID)
		}
	}
	return VerifyProof(string(data), h, asks), nil
}

// Headings is sections as "## " headings, comma-separated.
func Headings(sections []string) string {
	h := make([]string, len(sections))
	for i, s := range sections {
		h[i] = "## " + s
	}
	return strings.Join(h, ", ")
}

// SavePrompt is the prompt asking a boss to save for a clear: its list and notes, then the handoff file at path with
// Sections, naming transcript, merging the caller's facts file when given. One line, as a pasted prompt.
func SavePrompt(path, transcript, facts string) string {
	text := "[dev agent clear] Save for a clear of your context, in this order, without starting or prompting any worker " +
		"and without ending on a question (write what you don't know as unconfirmed): 1. update your to do list: each " +
		"item's status, and --details on every item the user or a person owes (dev todo add --replaces <n> --details '…'); " +
		"2. your notes (dev todo note): decisions, what the user said, open risks; 3. write the handoff file " + path +
		" with exactly these sections as markdown headings: " + Headings(Sections) + ". Under ## Key facts: numbered, at " +
		"most 15, the facts you must not lose, each ending \"confirmed: <item number, PR or file>\" or \"unconfirmed\". " +
		"Under ## Open asks: each open ask by its #number and what each pick starts. Under ## Old transcript: " +
		transcript + ". Name every open item of your list by its #number somewhere in the file. No secrets in it."
	if facts != "" {
		text += " Merge in the facts in " + facts + " (from whoever runs this clear) as key facts, each marked the same way."
	}
	return text
}

// FixPrompt asks a boss to fix its handoff at path, naming what dev's check missed.
func FixPrompt(path string, miss []string) string {
	return "[dev agent clear] Your handoff " + path + " fails dev's check: " + strings.Join(miss, "; ") +
		". Fix the file (and your list where it says so), then end your turn."
}

// ProofPrompt asks the fresh boss to prove it read the handoff at path: the proof file with ProofSections.
func ProofPrompt(path, proof string) string {
	return "[dev agent clear] Read " + path + ". Before touching any worker, write " + proof + " with these markdown " +
		"headings: " + Headings(ProofSections) + ". Under ## Key facts: each key fact restated, numbered as in the handoff, " +
		"ending with its mark as there (\"confirmed: …\" or \"unconfirmed\"). Under ## Open asks: per open ask (its " +
		"#number), what you do on each pick. Under ## Roles: who answers your workers' permission prompts and dialogs, " +
		"and who relays asks to the user. Then end your turn."
}

// ProofFixPrompt asks the boss to fix its proof, naming what dev's check missed.
func ProofFixPrompt(path, proof string, miss []string) string {
	return "[dev agent clear] Your proof " + proof + " misses: " + strings.Join(miss, "; ") + ". Re-read " + path +
		" and fix the proof, then end your turn."
}
