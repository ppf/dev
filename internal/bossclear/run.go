package bossclear

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
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
	// Written by dev statusline; unset when its status line doesn't run it
	used, err := c.Run("display-message", "-p", "-t", a.Pane, "#{"+agent.OptContext+"}")
	if err != nil {
		return b, nil, err
	}
	if used = strings.TrimSpace(used); used != "" {
		n, err := strconv.Atoi(used)
		if err != nil {
			return b, nil, fmt.Errorf("%s %s: %w", a.Session, agent.OptContext, err)
		}
		b.Context = &n
	}
	if b.InputEmpty, err = inputEmpty(c, a.Pane); err != nil {
		return b, nil, err
	}
	// Its inbox switched off still keeps what was queued
	box, ok, err := inbox.Coordinator(c, a.Pane)
	if err != nil {
		return b, nil, err
	}
	if ok {
		if b.Queued, err = box.Box.Pending(); err != nil {
			return b, nil, err
		}
	}
	owned, err := startedBy(c, a)
	if err != nil {
		return b, nil, err
	}
	agents, err := control.List(c)
	if err != nil {
		return b, nil, err
	}
	var workers []Pane
	for _, name := range owned {
		i := slices.IndexFunc(agents, func(w control.Agent) bool { return w.Session == name })
		if i < 0 {
			// No agent registered (yet): no hook state, so maybe working
			workers = append(workers, Pane{Session: name})
			continue
		}
		p, _, err := readPane(c, agents[i], now)
		if err != nil {
			return b, nil, err
		}
		workers = append(workers, p)
	}
	return b, workers, nil
}

// startedBy is the sessions boss a started (@dev_started_by names it, by name or session id).
func startedBy(c *tmux.Client, a control.Agent) ([]string, error) {
	id, err := c.Run("display-message", "-p", "-t", a.Pane, "#{session_id}")
	if err != nil {
		return nil, err
	}
	type session struct {
		Name      string `tmux:"session_name"`
		StartedBy string `tmux:"@dev_started_by"`
	}
	sessions, err := tmux.Query[session](c, "list-sessions")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sessions {
		if sidebar.Names(s.StartedBy, strings.TrimSpace(id), a.Session, true) {
			out = append(out, s.Name)
		}
	}
	return out, nil
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

// Options are dev agent clear's: Facts the caller's own facts file ("" for none), Force clears under MinContext or
// with the share unknown, Stall and Timeout bound each prompt's turn and the /clear (control.PromptWait), Out gets
// the progress lines.
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
// handoff), then a proof prompt and VerifyProof. A failed verify prompts the boss once with what it missed and
// verifies again. From the save on, inbox.OptHandoff holds the boss's inbox notes back (a note that came in refuses
// the clear at the second check). A --facts fact the boss couldn't fix (carriedMisses) is refused before anything is
// sent; nothing is cleared before the handoff passes; the handoff, facts copy and transcript stay. A cleared manager's
// open offer (OfferQuestion) is answered "Clear it".
func Run(c *tmux.Client, clk control.Clock, a control.Agent, o Options) (r Result, err error) {
	if err := check(c, a, o.Force, clk.Now()); err != nil {
		return r, err
	}
	if held, err := inbox.ClearHeld(c, a.Pane); held || err != nil {
		return r, cmp.Or(err, fmt.Errorf("another dev agent clear runs for %s", a.Session))
	}
	var facts string
	var carried []Fact
	if o.Facts != "" {
		data, err := os.ReadFile(o.Facts)
		if err != nil {
			return r, fmt.Errorf("--facts: %w", err)
		}
		if facts, carried = string(data), readFacts(string(data)); len(carried) == 0 {
			return r, fmt.Errorf("--facts %s: no numbered facts (\"1. <fact> confirmed: <item, PR or file>\" or \"… unconfirmed\")", o.Facts)
		}
		// The boss copies them word for word: it couldn't fix one
		if bad := carriedMisses(carried); len(bad) > 0 {
			return r, fmt.Errorf("--facts %s: %s; nothing sent", o.Facts, strings.Join(bad, "; "))
		}
	}
	out, err := c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_session}\t#{@agent_transcript}")
	if err != nil {
		return r, err
	}
	conversation, transcript, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if r.Transcript = transcript; transcript == "" {
		// The handoff names it, so the old conversation can be read again
		return r, fmt.Errorf("%s's hook records no transcript path: nothing cleared", a.Session)
	}
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
	var factsCopy string
	if facts != "" {
		factsCopy = filepath.Join(dir, stamp+"-facts.md")
	}
	for _, p := range []string{r.Handoff, r.Proof, factsCopy} {
		switch _, err := os.Stat(p); {
		case p == "" || errors.Is(err, fs.ErrNotExist):
		case err == nil:
			return r, fmt.Errorf("%s exists already: run it again in a second", p)
		default:
			return r, err
		}
	}
	if factsCopy != "" {
		if err := os.WriteFile(factsCopy, []byte(facts), 0o600); err != nil {
			return r, err
		}
	}

	// Past every turn this run may wait on: a pid reused after a kill holds the notes no longer
	hold := inbox.ClearValue(conversation, r.Handoff, clk.Now().Add(5*(o.Timeout+o.Stall)+time.Minute))
	if _, err := c.Run("set-option", "-p", "-t", a.Pane, inbox.OptHandoff, hold); err != nil {
		return r, err
	}
	defer func() {
		// Only its own: once this process ends it holds nothing anyway (inbox.clearing)
		if uerr := release(c, a.Pane, hold); uerr != nil {
			err = errors.Join(err, fmt.Errorf("unsetting %s: %w", inbox.OptHandoff, uerr))
		}
	}()
	fmt.Fprintf(o.Out, "Saving: %s updates its list and notes and writes %s (old transcript %s)\n", a.Session, r.Handoff, r.Transcript)
	if err := prompt(c, clk, a, SavePrompt(r.Handoff, r.Transcript, factsCopy), o); err != nil {
		return r, fmt.Errorf("save: %w; nothing cleared", err)
	}
	h, miss, err := verifyHandoff(c, a, r, carried, clk.Now())
	if err != nil {
		return r, err
	}
	if len(miss) > 0 {
		fmt.Fprintf(o.Out, "Handoff misses: %s; %s fixes it\n", strings.Join(miss, "; "), a.Session)
		if err := prompt(c, clk, a, FixPrompt(r.Handoff, miss), o); err != nil {
			return r, fmt.Errorf("handoff fix: %w; nothing cleared", err)
		}
		if h, miss, err = verifyHandoff(c, a, r, carried, clk.Now()); err != nil {
			return r, err
		}
		if len(miss) > 0 {
			return r, fmt.Errorf("handoff %s still misses: %s; nothing cleared", r.Handoff, strings.Join(miss, "; "))
		}
	}
	fmt.Fprintf(o.Out, "Handoff verified: %d key facts\n", len(h.Facts))
	saved, err := c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_session} #{@agent_prompts}")
	if err != nil {
		return r, err
	}

	// A worker may have started or a note come in during the save
	if err := check(c, a, o.Force, clk.Now()); err != nil {
		return r, fmt.Errorf("%w; nothing cleared, handoff %s kept", err, r.Handoff)
	}
	if err := unchanged(c, a, conversation, saved); err != nil {
		return r, fmt.Errorf("%w; nothing cleared, handoff %s kept", err, r.Handoff)
	}
	proof := ProofPrompt(r.Handoff, r.Proof)
	manual := fmt.Sprintf("handoff %s, old transcript %s; the proof prompt to send by hand first (its held inbox notes reach it once this "+
		"command ends): %s", r.Handoff, r.Transcript, proof)
	if _, err := control.PromptWait(c, clk, a.Pane, "/clear", o.Stall, o.Timeout); err != nil {
		return r, fmt.Errorf("/clear: %w; it may still clear: once dev agent read %s shows a fresh conversation, %s", err, a.Session, manual)
	}
	fmt.Fprintf(o.Out, "Cleared: %s runs a new conversation; it writes its proof to %s\n", a.Session, r.Proof)

	if err := prompt(c, clk, a, proof, o); err != nil {
		return r, fmt.Errorf("proof: %w; %s", err, manual)
	}
	miss, err = verifyProof(c, a, r, h, clk.Now())
	if err != nil {
		return r, fmt.Errorf("%w; %s", err, manual)
	}
	if len(miss) > 0 {
		fmt.Fprintf(o.Out, "Proof misses: %s; %s fixes it\n", strings.Join(miss, "; "), a.Session)
		if err := prompt(c, clk, a, ProofFixPrompt(r.Handoff, r.Proof, miss), o); err != nil {
			return r, fmt.Errorf("proof fix: %w; %s", err, manual)
		}
		if miss, err = verifyProof(c, a, r, h, clk.Now()); err != nil {
			return r, fmt.Errorf("%w; %s", err, manual)
		}
		if len(miss) > 0 {
			return r, fmt.Errorf("proof %s still misses: %s; %s", r.Proof, strings.Join(miss, "; "), manual)
		}
	}
	r.Facts = len(h.Facts)
	if a.Session == sidebar.Manager {
		if err := answerOffer(c, a.Pane, clk.Now()); err != nil {
			fmt.Fprintf(o.Out, "⚠ %q left open on the manager's list: %v\n", OfferQuestion, err)
		}
	}
	return r, nil
}

// carriedMisses is what the boss couldn't fix in the caller's facts it must copy word for word: a fact unmarked or
// worded as done while unconfirmed, a number used twice, more than MaxFacts.
func carriedMisses(fs []Fact) []string {
	var miss []string
	if len(fs) > MaxFacts {
		miss = append(miss, fmt.Sprintf("%d facts, at most %d", len(fs), MaxFacts))
	}
	if r := repeated(fs); len(r) > 0 {
		miss = append(miss, "fact numbers used twice: "+strings.Join(r, ", "))
	}
	for _, f := range fs {
		if f.Mark == "" {
			miss = append(miss, fmt.Sprintf("fact %d ends in neither \"confirmed: <item, PR or file>\" nor \"unconfirmed\"", f.N))
		}
	}
	if done := StatedDone(fs); done != "" {
		miss = append(miss, done)
	}
	return miss
}

// unchanged refuses the /clear unless boss a, right before it, still runs conversation and has had no turn since its
// handoff passed (saved: its session and prompt count then), its input box is empty and no inbox note waits: a
// check() reads every worker first, which takes a while.
func unchanged(c *tmux.Client, a control.Agent, conversation, saved string) error {
	now, err := c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_session} #{@agent_prompts}")
	if err != nil {
		return err
	}
	if session, _, _ := strings.Cut(strings.TrimSpace(now), " "); session != conversation {
		return fmt.Errorf("%s runs another conversation than the one that wrote the handoff", a.Session)
	}
	if strings.TrimSpace(now) != strings.TrimSpace(saved) {
		return fmt.Errorf("%s had a turn since its handoff passed", a.Session)
	}
	empty, err := inputEmpty(c, a.Pane)
	if err != nil || !empty {
		return cmp.Or(err, fmt.Errorf("%s's input box holds text, maybe the user's draft", a.Session))
	}
	box, ok, err := inbox.Coordinator(c, a.Pane)
	if err != nil || !ok {
		return err
	}
	if queued, err := box.Box.Pending(); err != nil || queued {
		return cmp.Or(err, fmt.Errorf("inbox notes wait for %s", a.Session))
	}
	return nil
}

// release unsets pane's inbox.OptHandoff while it still holds hold, this run's value.
func release(c *tmux.Client, pane, hold string) error {
	v, err := c.Run("display-message", "-p", "-t", pane, "#{"+inbox.OptHandoff+"}")
	if err != nil || strings.TrimSpace(v) != hold {
		return err
	}
	_, err = c.Run("set-option", "-pu", "-t", pane, inbox.OptHandoff)
	return err
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
	empty, err := inputEmpty(c, a.Pane)
	if err != nil {
		return err
	}
	if !empty {
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

// inputEmpty reports pane's input box empty, a dim suggestion counting as empty.
func inputEmpty(c *tmux.Client, pane string) (bool, error) {
	shot, err := c.Run("capture-pane", "-p", "-e", "-t", pane)
	if err != nil {
		return false, err
	}
	return inbox.InputEmpty(shot), nil
}

// list is boss a's list as read at now.
func list(c *tmux.Client, a control.Agent, now time.Time) (todo.List, error) {
	s, err := todo.ForPane(c, a.Pane)
	if err != nil {
		return todo.List{}, err
	}
	return s.Read(now)
}

// verifyHandoff reads r's handoff and boss a's list and checks them: VerifyHandoff against its open items, and no
// item of the user's or a person's without details.
func verifyHandoff(c *tmux.Client, a control.Agent, r Result, carried []Fact, now time.Time) (Handoff, []string, error) {
	l, err := list(c, a, now)
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
	h, miss := VerifyHandoff(string(data), open, r.Transcript, carried)
	if len(bare) > 0 {
		miss = append(miss, "items of the user's or a person's without details on the list: "+numbers(bare))
	}
	return h, miss, nil
}

// verifyProof reads r's proof and checks it against handoff h and boss a's open asks.
func verifyProof(c *tmux.Client, a control.Agent, r Result, h Handoff, now time.Time) ([]string, error) {
	l, err := list(c, a, now)
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
		"item's status, and --details on every item the user or a person owes (dev todo add --replaces <n> --details '…'; an ask: dev todo ask --replaces <n> --details '…', which keeps its question's time); " +
		"2. your notes (dev todo note): decisions, what the user said, open risks; 3. write the handoff file " + path +
		" with exactly these sections as markdown headings: " + Headings(Sections) + ". Under ## Key facts: numbered, at " +
		"most " + strconv.Itoa(MaxFacts) + ", the facts you must not lose, each ending \"unconfirmed\" or, after a full stop, \"confirmed: <item number, PR or file>\"; an unconfirmed fact is never worded as done (not \"is merged\", but \"reported merged, not checked\"). " +
		"Under ## Open asks: each open ask by its #number and what each pick starts. Under ## Roles: who answers your workers' permission prompts and dialogs, and who relays your asks to the user. Under ## Old transcript: " +
		transcript + ". Name every open item of your list by its #number somewhere in the file. No secrets in it."
	if facts != "" {
		text += " Copy each fact in " + facts + " (from whoever runs this clear) verbatim, its mark included, as one of your key facts, numbered on."
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
