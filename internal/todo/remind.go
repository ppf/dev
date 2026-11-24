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
	head := fmt.Sprintf("[dev todo] Your open to do list: %d open, %d changed since the last one", open, changed)
	if changed == 0 {
		head = fmt.Sprintf("[dev todo] Your open to do list: %d open, none changed since the last one", open)
	}
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

// Remind is l's reminder for its boss reading worker notes at now: the full list (Reminder) the first time since its
// start (StartRemind) or ForgetShown, then only the items changed since under a count line. What it names is kept for
// the next one.
func (s Store) Remind(l List, ticked []Ticked, now time.Time) (string, error) {
	seen, err := s.readShown()
	if err != nil {
		return "", err
	}
	text, shown := l.reminder(ticked, seen, now)
	// An open item not named again is as the boss last got it
	for _, it := range l.Items {
		if line, ok := seen[it.ID]; ok && !it.Done && shown[it.ID] == "" {
			shown[it.ID] = line
		}
	}
	return text, s.writeShown(shown)
}

// StartRemind is l's StartReminder, what it names kept for the next Remind.
func (s Store) StartRemind(l List, kept bool, age string, ticked []Ticked, now time.Time) (string, error) {
	text, shown := l.startReminder(kept, age, ticked, now)
	return text, s.writeShown(shown)
}

// ForgetShown drops what the boss last got of its list, as a compaction may have dropped it: the next Remind is the full list.
func (s Store) ForgetShown() error { return ignoreMissing(os.Remove(s.shownPath())) }

// shownPath is the file of the list's lines its boss last got: beside the lists, not among them.
func (s Store) shownPath() string {
	return filepath.Join(filepath.Dir(s.path), "shown", filepath.Base(s.path))
}

// readShown loads the lines its boss last got, nil for none: no file, or one that isn't JSON, which it removes.
func (s Store) readShown() (map[int]string, error) {
	data, err := os.ReadFile(s.shownPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var seen map[int]string
	if json.Unmarshal(data, &seen) != nil || seen == nil {
		return nil, ignoreMissing(os.Remove(s.shownPath()))
	}
	return seen, nil
}

// writeShown keeps shown as the lines its boss last got.
func (s Store) writeShown(shown map[int]string) error {
	path := s.shownPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(shown)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
