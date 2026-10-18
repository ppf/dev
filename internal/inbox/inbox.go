// Package inbox is a dev boss's inbox: one-line notes about its workers (finished, failed, needs a
// decision, stuck), kept in a locked file per boss and delivered to the coordinator Claude in batches.
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
)

// OptOff is the tmux option that turns the inbox off ("off"), globally or per boss session.
const OptOff = "@dev_boss_inbox"

// Note kinds.
const (
	Finished = "finished"
	Failed   = "failed"
	Decision = "needs a decision"
	Stuck    = "stuck"
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
	if n.Text == "" {
		return n.Worker + " " + n.Kind
	}
	return n.Worker + " " + n.Kind + ": " + n.Text
}

const (
	maxText  = 200
	maxNotes = 20
)

// hookEvent is the part of a worker's hook input a note needs.
type hookEvent struct {
	Name             string          `json:"hook_event_name"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Last             string          `json:"last_assistant_message"`
	Error            string          `json:"error"`
	Tool             string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
}

// Forget clears worker's last note key, so its next turn's note isn't taken for a repeat of the last one.
func (b Box) Forget(worker string) error {
	return b.update(func(s *state) error {
		delete(s.Last, worker)
		return nil
	})
}

// FromHook builds worker's note for a Claude or Codex hook event; ok=false for events that make none (an idle
// reminder, tool events).
func FromHook(worker string, input []byte) (Note, bool) {
	var e hookEvent
	if json.Unmarshal(input, &e) != nil {
		return Note{}, false
	}
	var n Note
	switch {
	case e.Name == "Stop":
		n = Note{Kind: Finished, Text: clean(e.Last)}
	case e.Name == "StopFailure":
		n = Note{Kind: Failed, Text: cmp.Or(clean(e.Error), "stopped on an error")}
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

// StuckNote is worker's note for a state held too long: working, or waiting on a decision. since keys
// it, so one stretch gives one note.
func StuckNote(worker, state string, held time.Duration, since string) Note {
	text := fmt.Sprintf("working for %dm", int(held.Minutes()))
	if state != "working" {
		text = fmt.Sprintf("waiting on a decision for %dm, unanswered", int(held.Minutes()))
	}
	return Note{Worker: worker, Kind: Stuck, Text: text, Key: Stuck + "\x00" + state + since}
}

// clean collapses whitespace and truncates.
func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText-1]) + "…"
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

// state is an inbox file: the pending notes, each worker's last note key (dedupe) and the last paste.
type state struct {
	Notes     []Note            `json:"notes"`
	Last      map[string]string `json:"last"`
	Dropped   int               `json:"dropped,omitempty"`
	LastPaste int64             `json:"last_paste,omitempty"`
}

// Box is one boss's inbox file.
type Box struct{ path string }

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Open is the inbox of the boss session on the tmux server at socket, under $XDG_STATE_HOME/dev/inbox
// (default ~/.local/state); session is its id ($N) and creation time, as a restarted server reuses ids.
func Open(socket, session string) (Box, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Box{}, err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	name := strings.Trim(unsafe.ReplaceAllString(socket+"-"+session, "_"), "_")
	return Box{path: filepath.Join(dir, "dev", "inbox", name+".json")}, nil
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
// whether it was queued.
func (b Box) Add(n Note) (added bool, err error) {
	err = b.update(func(s *state) error {
		if s.Last[n.Worker] == n.Key {
			return nil
		}
		s.Last[n.Worker] = n.Key
		s.Notes = append(s.Notes, n)
		if over := len(s.Notes) - maxNotes; over > 0 {
			s.Notes, s.Dropped = s.Notes[over:], s.Dropped+over
		}
		added = true
		return nil
	})
	return added, err
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
	s.Notes, s.Dropped = nil, 0
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
		s.Notes = append(slices.Clone(bt.Notes), s.Notes...)
		s.Dropped += bt.Dropped
		s.LastPaste = 0
		return nil
	})
}

// Pending reports whether notes wait.
func (b Box) Pending() (bool, error) {
	lock, err := b.lock()
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Close() }()
	s, err := b.read()
	return len(s.Notes) > 0, err
}
