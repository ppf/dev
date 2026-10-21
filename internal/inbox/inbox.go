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

	"github.com/ppf/dev/internal/config"
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
)

// Note is one line about a worker; Key dedupes it against the worker's previous note.
type Note struct {
	Worker string `json:"worker"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Key    string `json:"key"`
}

// String is the note's line: "<worker> <kind>: <text>".
func (n Note) String() string {
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
	// askLines is how many of a last message's non-blank lines may hold its question: the question and its options.
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

// question is the question msg ends on: its last line ending in "?" (markdown emphasis aside), not a heading,
// among its last askLines non-blank lines and followed only by list items, its options; "" when none.
func question(msg string) (ask, options string) {
	var lines []string
	for l := range strings.Lines(msg) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-askLines; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasSuffix(strings.TrimRight(l, "*_"), "?") && !strings.HasPrefix(l, "#") {
			return lines[i], strings.Join(lines[i+1:], "")
		}
		if !option.MatchString(l) {
			return "", ""
		}
	}
	return "", ""
}

// span is d in whole minutes: "17m", "1h", "1h 5m".
func span(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
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

// Batch is the notes taken from an inbox at once, and how many older ones were dropped.
type Batch struct {
	Notes   []Note
	Dropped int
}

// String is the batch as the coordinator gets it; "" for none.
func (bt Batch) String() string {
	if len(bt.Notes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[dev boss inbox] %d worker update(s):", len(bt.Notes))
	for _, n := range bt.Notes {
		b.WriteString("\n- " + n.String())
	}
	if bt.Dropped > 0 {
		fmt.Fprintf(&b, "\n(%d older update(s) dropped: run dev agent list --mine)", bt.Dropped)
	}
	return b.String()
}

// Message is the batch as sent over Claude's messaging socket, which Claude shows as another session's message
// and would try to answer.
func (bt Batch) Message() string {
	return bt.String() + "\n(sent by dev: handle these as usual; no reply needed)"
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
func (b Box) lock() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(b.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
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
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
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
	if s.Last[n.Worker] == n.Key {
		return false
	}
	s.Last[n.Worker] = n.Key
	s.Notes = append(s.Notes, n)
	if over := len(s.Notes) - maxNotes; over > 0 {
		s.Notes, s.Dropped = s.Notes[over:], s.Dropped+over
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
	bt := Batch{Notes: s.Notes, Dropped: s.Dropped}
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
}

// flightTimeout is how long a sent batch may go unshown while the coordinator rests before it counts as lost.
// A busy Claude shows a message only between tool calls or after its turn, and one waiting on a dialog after the
// dialog, so the clock only counts out a rest.
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

// Arrived clears the batch in flight when prompt, from the coordinator's UserPromptSubmit, is its message.
func (b Box) Arrived(prompt string) error {
	return b.update(func(s *state) error {
		if s.InFlight != nil && strings.Contains(prompt, s.InFlight.batch().Message()) {
			s.InFlight = nil
		}
		return nil
	})
}

// Expire puts the batch in flight back, for the paste, once it went unshown flightTimeout while the coordinator
// rests (resting); expired reports it.
func (b Box) Expire(now time.Time, resting bool) (expired bool, err error) {
	err = b.update(func(s *state) error {
		if s.InFlight == nil || !resting || now.Sub(time.Unix(s.InFlight.At, 0)) < flightTimeout {
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
