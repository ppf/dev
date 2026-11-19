// Package inbox is a dev boss's inbox: one-line notes about its workers (finished, failed, needs a
// decision, asks, stuck, high context), kept in a locked file per boss and delivered to the coordinator
// Claude in batches.
package inbox

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/wait"
)

// OptOff is the tmux option that turns the inbox off ("off"), globally or per boss session.
const OptOff = "@dev_boss_inbox"

// OptNote is the pane option holding a worker's latest note (Note.Summary), which the boss previews show.
const OptNote = "@dev_boss_note"

// Note kinds.
const (
	Finished = "finished"
	Failed   = "failed"
	Decision = "needs a decision"
	Asks     = "asks" // a turn that ended on a question
	Stuck    = "stuck"
	Context  = "high context"
	Resumed  = "resumed"
	Waits    = "waiting"                 // its turn ended on a planned wait (dev agent wait-until)
	Message  = "message"                 // from another boss: dev agent message
	Answered = "you answered"            // the user answered an item on the boss's list: dev todo answer
	Start    = "you were asked to start" // the user asked to start an item: dev todo start
	// WaitsOnYou is from a boss under this one: it put an item for the user on its list (dev todo add, ask)
	WaitsOnYou = "waits on you"
	// ToDo is an item on the boss's own list that may be out of date, or ticked by itself (todo.Nudge, todo.Ticked)
	ToDo = "to do"
)

// up reports a kind of note a boss under another boss (its @dev_started_by) sends that boss: what waits on the
// user and what went wrong, not its finished turns, high context or resumes.
func up(kind string) bool {
	switch kind {
	case WaitsOnYou, Asks, Decision, Failed, Stuck:
		return true
	}
	return false
}

// Note is one line about a worker; Key dedupes it against the worker's previous note.
type Note struct {
	Worker string `json:"worker"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Key    string `json:"key"`
	// Outside marks a worker a coordinator outside tmux started (dev agent start --boss).
	Outside bool `json:"outside,omitempty"`
}

// leaveIt ends an Outside note's line.
const leaveIt = "started by a coordinator outside tmux: leave it to that coordinator"

// String is the note's line: "<worker> <kind>: <text>", then leaveIt when Outside; a Message's is "message <text>",
// an Answered's "you answered <text>", a Start's "you were asked to start <text>", a ToDo's "to do <text>".
func (n Note) String() string {
	if n.Kind == Message || n.Kind == Answered || n.Kind == Start || n.Kind == ToDo {
		return n.Kind + " " + n.Text
	}
	if n.Outside {
		return n.Worker + " " + n.Summary() + "; " + leaveIt
	}
	return n.Worker + " " + n.Summary()
}

// Summary is the note without its worker: "<kind>: <text>".
func (n Note) Summary() string {
	if n.Text == "" {
		return n.Kind
	}
	return n.Kind + ": " + n.Text
}

const (
	maxText  = 200
	maxNotes = 20
	// askLines is how many of a last message's non-blank lines may hold its question: the question and its options, or list items asking questions.
	askLines = 5
)

// hookEvent is the part of a worker's hook input a note needs.
type hookEvent struct {
	Name             string          `json:"hook_event_name"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Last             string          `json:"last_assistant_message"`
	Error            string          `json:"error"`
	ErrorDetails     string          `json:"error_details"`
	Tool             string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
}

// Forget clears worker's last note key, so its next turn's note isn't taken for a repeat of the last one,
// and the question its last turn ended on: its new prompt answers it.
func (b Box) Forget(worker string) error {
	return b.update(func(s *state) error {
		delete(s.Last, worker)
		if h, ok := s.Workers[worker]; ok {
			h.Question, h.AskedAt, h.Reminded = "", 0, 0
		}
		return nil
	})
}

// FromHook builds worker's note for a Claude, Codex or Grok hook event; ok=false for events that make none (an idle
// reminder, tool events).
func FromHook(worker string, input []byte) (Note, bool) {
	var e hookEvent
	if json.Unmarshal(input, &e) != nil {
		return Note{}, false
	}
	var n Note
	ask, options := question(e.Last)
	switch {
	case e.Name == "Stop" && ask != "":
		// The question's end, then as many options as fit
		n = Note{Kind: Asks, Text: clean(cleanTail(ask) + " " + options)}
	case e.Name == "Stop":
		n = Note{Kind: Finished, Text: clean(e.Last)}
	case e.Name == "StopFailure":
		// The error's type, then what Claude showed ("You've hit your session limit · resets 7:10pm (Europe/Warsaw)")
		text := cmp.Or(e.Error, "stopped on an error")
		if e.Last != "" {
			text += ": " + e.Last
		}
		if e.ErrorDetails != "" && e.ErrorDetails != e.Last {
			text += " (" + e.ErrorDetails + ")"
		}
		n = Note{Kind: Failed, Text: clean(text)}
	case e.Name == "Notification" && (e.NotificationType == "permission_prompt" || e.NotificationType == "elicitation_dialog"):
		n = Note{Kind: Decision, Text: clean(e.Message)}
	case e.Name == "PermissionRequest": // Codex's; Claude's hooks report permissions as a Notification
		var in struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(e.ToolInput, &in) // tool_input may be a string (a patch) without a description
		n = Note{Kind: Decision, Text: cmp.Or(clean(in.Description), strings.TrimSpace("permission request "+e.Tool))}
	default:
		return Note{}, false
	}
	n.Worker = worker
	n.Key = n.Kind + "\x00" + n.Text
	return n, true
}

// option is a list item line: "- x", "* x", "• x", "1. x", "a) x".
var option = regexp.MustCompile(`^\s*([-*•]|\d+[.)]|[A-Za-z][.)])\s`)

// IsOption reports a list item line, as a question's options are.
func IsOption(l string) bool { return option.MatchString(l) }

// optionText is option line l without its list marker.
func optionText(l string) string { return strings.TrimSpace(l[len(option.FindString(l)):]) }

// question is the question msg ends on (see QuestionAt) and its options after it; "" when none. List items that
// ask questions themselves (questions) come as its options, each cut after its recommendation, so a note cut to
// length keeps the first.
func question(msg string) (ask, options string) {
	var lines []string
	for l := range strings.Lines(msg) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	i := QuestionAt(lines)
	if i < 0 {
		return "", ""
	}
	if !questions(lines[i:]) {
		return lines[i], strings.Join(lines[i+1:], "")
	}
	asks := slices.Clone(lines[i:])
	for k, l := range asks {
		if m := askItem.FindStringIndex(l); m != nil {
			asks[k] = l[:m[1]]
		}
	}
	return asks[0], strings.Join(asks[1:], " ")
}

// askItem is a list item asking a question mid-line, then recommending an answer in bold: "- Post it? I recommend
// **Yes**."; the match ends with the recommendation.
var askItem = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)]|[A-Za-z][.)])\s.*\?\s[^?]*?\bI(?:'d)? (?:recommend|suggest)\s+\*\*[^*]+\*\*\.?`)

// questions reports a question (QuestionAt) and the lines under it that are list items asking questions themselves
// (askItem): it is the first of them, its lead-in ends in ":", or all the lines under it are such items.
func questions(asked []string) bool {
	return askItem.MatchString(asked[0]) || strings.HasSuffix(strings.TrimSpace(asked[0]), ":") ||
		len(asked) > 1 && !slices.ContainsFunc(asked[1:], func(l string) bool { return !askItem.MatchString(l) })
}

var (
	// replyAsk starts a sentence telling the user how to reply: "Reply **Done**", "Answer **yes** or **no**"
	replyAsk = regexp.MustCompile(`(?:^|[.!?]\s+)(Reply|Answer)(?:\s+(?:with|either))?\s+`)
	// sentenceEnd ends the reply's sentence: its options are in it
	sentenceEnd = regexp.MustCompile(`[.!?](?:\s|$)`)
	// replyWords is a reply's options as words between slashes, all its sentence says: "Done / Skip", "go/no-go"
	replyWords = regexp.MustCompile(`^[\w-]+(?:\s*/\s*[\w-]+)+$`)
	// replyWord is a reply's option in bold, after its verb, "or", a comma or a slash
	replyWord = regexp.MustCompile(`(?:^|\bor\s+|,\s*|/\s*)\*\*([^*]{1,24})\*\*`)
)

// replyOptions are the options line l names in a sentence telling the user how to reply: "Reply **Done** once
// it's connected, or **Skip** to leave it." offers Done and Skip, "Reply yes/no." yes and no. An Answer sentence
// needs two bold options ("Answer **yes** or **no**"); nil when none does.
func replyOptions(l string) []string {
	for _, m := range replyAsk.FindAllStringSubmatchIndex(l, -1) {
		rest := l[m[1]:]
		if end := sentenceEnd.FindStringIndex(rest); end != nil {
			rest = rest[:end[0]]
		}
		reply := l[m[2]:m[3]] == "Reply"
		if w := replyWords.FindString(strings.TrimSpace(rest)); w != "" && reply {
			return strings.Fields(strings.ReplaceAll(w, "/", " "))
		}
		var opts []string
		for {
			o := replyWord.FindStringSubmatchIndex(rest)
			if o == nil || (o[0] != 0 && len(opts) == 0) {
				break
			}
			opts = append(opts, rest[o[2]:o[3]])
			rest = rest[o[1]:]
		}
		if len(opts) > 1 || len(opts) == 1 && reply {
			return opts
		}
	}
	return nil
}

// quoted is a line quoting someone else's question, not asking one: a blockquote or a "Your question:" style label.
var quoted = regexp.MustCompile(`(?i)^(?:>|[*_]*(?:your question|question|q|you asked)[*_]*\s*:)`)

// QuestionAt is where among a message's non-blank lines is the question it ends on, among its last askLines
// lines; -1 when none. It is the last line ending in "?" (markdown emphasis aside), not a heading, followed only
// by list items, its options; or the last line, telling how to reply with the options it names (replyOptions);
// or list items that are all questions (askItem), from their lead-in ending in ":" when they have one, else from
// the first of them, or of the last askLines lines when they run past those.
func QuestionAt(lines []string) int {
	asking := true // every list item below asks a question
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-askLines; i-- {
		l := strings.TrimSpace(lines[i])
		switch {
		case quoted.MatchString(l):
			if asking && i < len(lines)-1 {
				return i + 1
			}
			return -1
		case strings.HasSuffix(strings.TrimRight(l, "*_"), "?") && !strings.HasPrefix(l, "#"):
			return i
		case i == len(lines)-1 && len(replyOptions(l)) > 0:
			return i
		case option.MatchString(l):
			asking = asking && askItem.MatchString(l)
		case asking && i < len(lines)-1 && strings.HasSuffix(l, ":"):
			return i
		case asking && i < len(lines)-1:
			return i + 1
		default:
			return -1
		}
	}
	if asking && len(lines) > 0 {
		return max(0, len(lines)-askLines)
	}
	return -1
}

// Options are what the question at asked[0] offers, without bold or code marks: the options its line names when
// it is alone, telling how to reply (replyOptions), else the list items after it; none when those items are
// questions themselves (questions).
func Options(asked []string) []string {
	if len(asked) == 1 {
		return replyOptions(asked[0])
	}
	if questions(asked) {
		return nil
	}
	var opts []string
	for _, l := range asked[1:] {
		opts = append(opts, unmark.Replace(optionText(l)))
	}
	return opts
}

var unmark = strings.NewReplacer("**", "", "`", "")

// Span is d in whole minutes: "17m", "1h", "1h 5m".
func Span(d time.Duration) string {
	return wait.Span(d)
}

// clean collapses whitespace and truncates.
func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText-1]) + "…"
	}
	return s
}

// cleanTail is clean truncating from the start, as a question ends a message.
func cleanTail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxText {
		s = "…" + strings.TrimLeft(string(r[len(r)-maxText+1:]), " ")
	}
	return s
}

// Header starts every batch as the coordinator gets it.
const Header = "[dev boss inbox]"

// Batch is the notes taken from an inbox at once, and how many older ones were dropped.
type Batch struct {
	Notes   []Note
	Dropped int
	lost    bool // taken with PasteNext set: a Restore sets it again
}

// String is the batch as the coordinator gets it; "" for none.
func (bt Batch) String() string {
	if len(bt.Notes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d worker update(s):", Header, len(bt.Notes))
	for _, n := range bt.Notes {
		b.WriteString("\n- " + n.String())
	}
	if bt.Dropped > 0 {
		fmt.Fprintf(&b, "\n(%d older update(s) dropped: run dev agent list --mine and dev agent messages)", bt.Dropped)
	}
	if slices.ContainsFunc(bt.Notes, func(n Note) bool { return n.Kind == Message }) {
		b.WriteString("\n" + messageHint)
	}
	if slices.ContainsFunc(bt.Notes, func(n Note) bool { return n.Kind == Answered }) {
		b.WriteString("\n" + answeredHint)
	}
	if slices.ContainsFunc(bt.Notes, func(n Note) bool { return n.Kind == ToDo }) {
		b.WriteString("\n" + toDoHint)
	}
	return b.String()
}

// Message is the batch as sent over Claude's messaging socket, which Claude shows as another session's message
// and would try to answer.
func (bt Batch) Message() string {
	return bt.String() + "\n(from dev's boss inbox: act on these like any inbox batch; don't reply to this session, dev reads no reply here)"
}

// state is an inbox file: the pending notes, each worker's last note key (dedupe) and health, the last paste,
// and the batch sent over Claude's messaging socket that the coordinator hasn't shown yet.
type state struct {
	Notes     []Note             `json:"notes"`
	Last      map[string]string  `json:"last"`
	Workers   map[string]*health `json:"workers,omitempty"`
	Dropped   int                `json:"dropped,omitempty"`
	LastPaste int64              `json:"last_paste,omitempty"`
	InFlight  *flight            `json:"in_flight,omitempty"`
	// PasteNext: a sent batch went unseen; Claude drops a repeated message, so the next batch is pasted
	PasteNext bool `json:"paste_next,omitempty"`
	// Prompt is the user's latest prompt to the coordinator, not one dev sent (a batch, ResumePrompt)
	Prompt *Prompt `json:"prompt,omitempty"`
}

// Prompt is a prompt the coordinator got at At (unix seconds).
type Prompt struct {
	Text string `json:"text"`
	At   int64  `json:"at"`
	// Pick: the user's AskUserQuestion pick (Picked), good for one relayed answer; Spent once one was (Claim).
	// Question and Answer are its one question and answer, empty for a pick of several.
	Pick     bool   `json:"pick,omitempty"`
	Question string `json:"question,omitempty"`
	Answer   string `json:"answer,omitempty"`
	Spent    bool   `json:"spent,omitempty"`
}

// flight is a batch sent over Claude's messaging socket at At (unix seconds).
type flight struct {
	Notes   []Note `json:"notes"`
	Dropped int    `json:"dropped,omitempty"`
	At      int64  `json:"at"`
}

func (f *flight) batch() Batch {
	return Batch{Notes: f.Notes, Dropped: f.Dropped}
}

// health is what the inbox keeps of a worker between Watch rounds; times are unix seconds.
type health struct {
	Screen    string `json:"screen,omitempty"`     // a hash of its screen.Snapshot while it works
	ScreenAt  int64  `json:"screen_at,omitempty"`  // when Screen last changed
	Stuck     string `json:"stuck,omitempty"`      // its last stuck note's key
	Question  string `json:"question,omitempty"`   // what its last turn ended asking, until its next prompt
	AskedAt   int64  `json:"asked_at,omitempty"`   // when it asked
	Reminded  int    `json:"reminded,omitempty"`   // reminders of Question queued
	PRChecked string `json:"pr_checked,omitempty"` // the idle stretch (@agent_since) its PR was checked in
	Full      bool   `json:"full,omitempty"`       // a high context note went out, until its use drops back
	WaitEnd   int64  `json:"wait_end,omitempty"`   // when a planned wait it still worked through ended: its stuck clock starts there
}

// Box is one boss's inbox file.
type Box struct{ path string }

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Open is the inbox of the boss session on the tmux server at socket, under $XDG_STATE_HOME/dev/inbox
// (default ~/.local/state); session is its id ($N) and creation time, as a restarted server reuses ids.
func Open(socket, session string) (Box, error) {
	dir, err := config.StateDir()
	if err != nil {
		return Box{}, err
	}
	name := strings.Trim(unsafe.ReplaceAllString(socket+"-"+session, "_"), "_")
	return Box{path: filepath.Join(dir, "inbox", name+".json")}, nil
}

// lock takes the inbox's exclusive lock; close the file to release it.
func (b Box) lock() (*os.File, error) { return lockPath(b.path) }

// lockPath takes the exclusive lock of the file at path; close the file to release it.
func lockPath(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

// read loads the inbox file; a missing one is empty. Call it under lock.
func (b Box) read() (state, error) {
	var s state
	data, err := os.ReadFile(b.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return s, err
	default:
		if err := json.Unmarshal(data, &s); err != nil {
			return s, fmt.Errorf("inbox %s: %w", b.path, err)
		}
	}
	if s.Last == nil {
		s.Last = map[string]string{}
	}
	if s.Workers == nil {
		s.Workers = map[string]*health{}
	}
	return s, nil
}

// update runs f on the inbox under an exclusive lock and writes the result back.
func (b Box) update(f func(*state) error) error {
	lock, err := b.lock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	s, err := b.read()
	if err != nil {
		return err
	}
	if err := f(&s); err != nil {
		return err
	}
	return writeJSON(b.path, s)
}

// writeJSON replaces the file at path with v as JSON; call it under the file's lock.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add queues n unless it repeats its worker's last note; past maxNotes the oldest go. added reports
// whether it was queued. An Asks note asked at now is kept for Watch to repeat while unanswered.
func (b Box) Add(n Note, now time.Time) (added bool, err error) {
	err = b.update(func(s *state) error {
		if added = s.add(n); added && n.Kind == Asks {
			h := s.health(n.Worker)
			h.Question, h.AskedAt, h.Reminded = n.Text, now.Unix(), 0
		}
		return nil
	})
	return added, err
}

// health is worker's entry, added when missing.
func (s *state) health(worker string) *health {
	h, ok := s.Workers[worker]
	if !ok {
		h = &health{}
		s.Workers[worker] = h
	}
	return h
}

func (s *state) add(n Note) bool {
	// A message's sender, an answer's or a start's user and a to do item are no worker of this boss: their key would
	// spoil that worker's dedupe
	if n.Kind != Message && n.Kind != Answered && n.Kind != Start && n.Kind != ToDo {
		if s.Last[n.Worker] == n.Key {
			return false
		}
		s.Last[n.Worker] = n.Key
	}
	s.Notes = append(s.Notes, n)
	// The oldest go first, answers, starts and ticks last: an answered or ticked item is off the list and a started one
	// waits on its note, so a dropped note would be lost
	for over := len(s.Notes) - maxNotes; over > 0; over-- {
		i := slices.IndexFunc(s.Notes, func(n Note) bool { return n.Kind != Answered && n.Kind != Start && n.Key != tickKey })
		if i < 0 {
			break
		}
		s.Notes, s.Dropped = slices.Delete(s.Notes, i, i+1), s.Dropped+1
	}
	return true
}

// Take removes and returns the pending batch.
func (b Box) Take() (Batch, error) {
	var bt Batch
	err := b.update(func(s *state) error {
		bt = s.take()
		return nil
	})
	return bt, err
}

func (s *state) take() Batch {
	bt := Batch{Notes: s.Notes, Dropped: s.Dropped, lost: s.PasteNext}
	s.Notes, s.Dropped, s.PasteNext = nil, 0, false
	return bt
}

// pasteGap rate-limits pasted batches: at most one per boss per pasteGap. The boss's own hooks pick up
// the rest when it next stops or the user types.
const pasteGap = 10 * time.Second

// TakeForPaste is Take, but at most once per pasteGap; it records now as the last paste. Put the batch
// back with Restore if the paste fails.
func (b Box) TakeForPaste(now time.Time) (Batch, error) {
	var bt Batch
	err := b.update(func(s *state) error {
		if len(s.Notes) == 0 || now.Sub(time.Unix(s.LastPaste, 0)) < pasteGap {
			return nil
		}
		bt = s.take()
		s.LastPaste = now.Unix()
		return nil
	})
	return bt, err
}

// Restore puts an undelivered batch back in front of the notes queued since.
func (b Box) Restore(bt Batch) error {
	return b.update(func(s *state) error {
		s.restore(bt)
		s.LastPaste = 0
		return nil
	})
}

func (s *state) restore(bt Batch) {
	s.Notes = append(slices.Clone(bt.Notes), s.Notes...)
	s.Dropped += bt.Dropped
	s.PasteNext = s.PasteNext || bt.lost
}

// flightTimeout is how long a sent batch may go unshown into the coordinator's rest before it counts as lost.
// A busy Claude shows a message only between tool calls or after its turn, and one waiting on a dialog after the
// dialog, so only a rest counts.
const flightTimeout = 5 * time.Minute

// TakeForSend takes the pending batch to send over Claude's messaging socket and records it in flight. It takes
// nothing while a batch is in flight; paste reports pending notes left to the paste after a lost batch (PasteNext).
func (b Box) TakeForSend(now time.Time) (bt Batch, paste bool, err error) {
	err = b.update(func(s *state) error {
		if s.InFlight != nil || len(s.Notes) == 0 {
			return nil
		}
		if s.PasteNext {
			paste = true
			return nil
		}
		bt = s.take()
		s.InFlight = &flight{Notes: bt.Notes, Dropped: bt.Dropped, At: now.Unix()}
		return nil
	})
	return bt, paste, err
}

// Unsend puts the batch in flight, which couldn't be sent, back in front of the notes queued since.
func (b Box) Unsend() error {
	return b.update(func(s *state) error {
		if s.InFlight != nil {
			s.restore(s.InFlight.batch())
			s.InFlight = nil
		}
		return nil
	})
}

// Arrived clears the batch in flight when prompt, from the coordinator's UserPromptSubmit, contains its message;
// a prompt dev didn't send (no batch, not ResumePrompt) is kept as the user's latest, from now.
func (b Box) Arrived(prompt string, now time.Time) error {
	return b.update(func(s *state) error {
		if s.InFlight != nil && strings.Contains(prompt, s.InFlight.batch().Message()) {
			s.InFlight = nil
		}
		if p := strings.TrimSpace(prompt); !strings.HasPrefix(p, Header) && p != ResumePrompt && p != "" {
			s.Prompt = &Prompt{Text: clip(prompt), At: now.Unix()}
		}
		return nil
	})
}

// Picked keeps picks, the user's in the coordinator's AskUserQuestion (agent.HookPick), as their latest prompt.
func (b Box) Picked(picks []agent.Pick, now time.Time) error {
	var words []string
	for _, p := range picks {
		if p.Answer != "" {
			words = append(words, p.String())
		}
	}
	p := &Prompt{Text: clip(strings.Join(words, "; ")), At: now.Unix(), Pick: true}
	if len(picks) == 1 {
		p.Question, p.Answer = picks[0].Question, picks[0].Answer
	}
	return b.update(func(s *state) error {
		s.Prompt = p
		return nil
	})
}

// Claim spends p, the pick an answer is relayed from: refused once spent or when the user said something since.
func (b Box) Claim(p Prompt) error {
	return b.update(func(s *state) error {
		switch {
		case !s.Prompt.is(p):
			return errors.New("the user said something since: look again")
		case s.Prompt.Spent:
			return errors.New("the user's latest pick answered an item already: ask them this one")
		}
		s.Prompt.Spent = true
		return nil
	})
}

// Unclaim gives back p, claimed for an answer that was refused, unless the user said something since.
func (b Box) Unclaim(p Prompt) error {
	return b.update(func(s *state) error {
		if s.Prompt.is(p) {
			s.Prompt.Spent = false
		}
		return nil
	})
}

// is reports p the same prompt as q, spent or not.
func (p *Prompt) is(q Prompt) bool {
	if p == nil {
		return false
	}
	now := *p
	now.Spent = q.Spent
	return now == q
}

// ForgetPrompt drops the user's latest prompt (Arrived), for a new conversation.
func (b Box) ForgetPrompt() error {
	return b.update(func(s *state) error {
		s.Prompt = nil
		return nil
	})
}

// clip is s with whitespace collapsed, cut to maxText in the middle, as a prompt's answer may be at either end.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText/2-1]) + " … " + string(r[len(r)-maxText/2+2:])
	}
	return s
}

// UserPrompt is the user's latest prompt to the coordinator (Arrived), nil before any.
func (b Box) UserPrompt() (*Prompt, error) {
	lock, err := b.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	s, err := b.read()
	return s.Prompt, err
}

// Expire puts the batch in flight back, for the paste, once it went unshown flightTimeout into the coordinator's
// rest, which began at rest (zero while it doesn't rest); expired reports it.
func (b Box) Expire(now, rest time.Time) (expired bool, err error) {
	err = b.update(func(s *state) error {
		if s.InFlight == nil || rest.IsZero() || now.Unix()-max(rest.Unix(), s.InFlight.At) < int64(flightTimeout.Seconds()) {
			return nil
		}
		s.restore(s.InFlight.batch())
		s.InFlight, s.PasteNext, expired = nil, true, true
		return nil
	})
	return expired, err
}

// workers is each worker's health, read under the lock.
func (b Box) workers() (map[string]*health, error) {
	lock, err := b.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	s, err := b.read()
	return s.Workers, err
}

// Pending reports whether notes wait, or a sent batch waits to be shown.
func (b Box) Pending() (bool, error) {
	lock, err := b.lock()
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Close() }()
	s, err := b.read()
	return len(s.Notes) > 0 || s.InFlight != nil, err
}
