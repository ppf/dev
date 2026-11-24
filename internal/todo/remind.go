package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// tickLine asks a boss reading worker notes to tick what they closed.
const tickLine = "\nTick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> \"<new text>\"."

// reminder is Reminder; with seen (the lines a boss last got, by item id), only the items changed since under a
// count line, those to re-check always. shown is the lines it names.
func (l List) reminder(ticked []Ticked, seen map[int]string, now time.Time) (string, map[int]string) {
	items, shown := l.linesSince(0, seen, now)
	open, _ := l.Open()
	if open == 0 || seen == nil && items == "" {
		return tickedText(ticked), shown
	}
	if seen == nil {
		return withTicked(ticked, "[dev todo] Your open to do list:"+items+tickLine+whoLine+detailsLine(items)), shown
	}
	changed := len(shown)
	isOpen := map[int]bool{}
	for _, it := range l.Items {
		if !it.Done {
			isOpen[it.ID] = true
			if it.Flagged() {
				changed--
			}
		}
	}
	count := strconv.Itoa(changed)
	if changed == 0 {
		count = "none"
	}
	head := fmt.Sprintf("[dev todo] Your open to do list: %d open, %s changed since the last one", open, count)
	var left []string
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		if !isOpen[id] {
			left = append(left, "#"+strconv.Itoa(id))
		}
	}
	if len(left) > 0 {
		head += ", " + strings.Join(left, ", ") + " left it"
	}
	if items == "" {
		return withTicked(ticked, head+" (dev todo lists them)."+tickLine), shown
	}
	return withTicked(ticked, head+"; the others as before (dev todo lists them):"+items+tickLine+whoLine+detailsLine(items)), shown
}

// Remind is l's reminder for its boss reading worker notes at now in conversation (its Claude session id): the full
// list (Reminder) when none is kept for that conversation (StartRemind or an earlier Remind in it; ForgetShown drops it),
// else only the items changed since under a count line. What it names is kept for the next one. The text holds even
// with err: the shown file not read (it is then the full list) or not written.
func (s Store) Remind(l List, ticked []Ticked, conversation string, now time.Time) (string, error) {
	seen, readErr := s.readShown(conversation)
	text, shown := l.reminder(ticked, seen, now)
	// An open item not named again is as the boss last got it
	for _, it := range l.Items {
		if line, ok := seen[it.ID]; ok && !it.Done && shown[it.ID] == "" {
			shown[it.ID] = line
		}
	}
	return text, errors.Join(readErr, s.writeShown(conversation, shown))
}

// StartRemind is l's StartReminder, what it names kept for the next Remind in conversation; the text holds even with
// err, the shown file not written.
func (s Store) StartRemind(l List, kept bool, age string, ticked []Ticked, conversation string, now time.Time) (string, error) {
	text, shown := l.startReminder(kept, age, ticked, now)
	return text, s.writeShown(conversation, shown)
}

// ForgetShown drops what the boss last got of its list, as a compaction may have dropped it: the next Remind is the full list.
func (s Store) ForgetShown() error { return ignoreMissing(os.Remove(s.shownPath())) }

// shownPath is the file of the list's lines its boss last got: beside the lists, not among them.
func (s Store) shownPath() string {
	return filepath.Join(filepath.Dir(s.path), "shown", filepath.Base(s.path))
}

// shownFile is the lines a boss last got of its list, by item id, and the conversation it got them in.
type shownFile struct {
	Conversation string         `json:"conversation"`
	Lines        map[int]string `json:"lines"`
}

// readShown is the lines its boss last got in conversation, nil for none: no file, another conversation's, or one
// that isn't JSON, which it removes and reports.
func (s Store) readShown(conversation string) (map[int]string, error) {
	data, err := os.ReadFile(s.shownPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var f shownFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errors.Join(fmt.Errorf("%s: %w: removed, the full list goes out", s.shownPath(), err), ignoreMissing(os.Remove(s.shownPath())))
	}
	if f.Conversation != conversation || f.Lines == nil {
		return nil, nil
	}
	return f.Lines, nil
}

// writeShown keeps shown as the lines its boss last got in conversation.
func (s Store) writeShown(conversation string, shown map[int]string) error {
	path := s.shownPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(shownFile{Conversation: conversation, Lines: shown})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
