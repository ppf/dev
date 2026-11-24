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
	if seen == nil {
		if items == "" {
			return tickedText(ticked), shown
		}
		return withTicked(ticked, "[dev todo] Your open to do list:"+items+tickLine+whoLine+detailsLine(items)), shown
	}
	// New or changed (seenAs); one kept as "" was open past the start list's "+N more": not shown yet
	changed, unshown := 0, 0
	open, listed := map[int]bool{}, map[int]bool{}
	for _, it := range l.Items {
		listed[it.ID] = true
		if it.Done {
			continue
		}
		open[it.ID] = true
		switch prev, was := seen[it.ID]; {
		case was && prev == "":
			unshown++
		case prev != it.seenAs(now):
			changed++
		}
	}
	// Not one never shown (past the start list), nor one the ticked lines name
	told := func(id int) bool {
		// The very item the boss got: a number may have been reused since
		return slices.ContainsFunc(ticked, func(t Ticked) bool {
			return t.ID == id && seenText(seen[id]) == t.Text
		})
	}
	var left []string
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		// One ticked still on the list (by its boss, or answered) is said elsewhere
		if !listed[id] && seen[id] != "" && !told(id) {
			left = append(left, "#"+strconv.Itoa(id))
		}
	}
	if len(open) == 0 {
		if len(left) == 0 {
			return tickedText(ticked), shown
		}
		return withTicked(ticked, "[dev todo] Your open to do list is empty now: "+strings.Join(left, ", ")+" left it."), shown
	}
	count := strconv.Itoa(changed)
	if changed == 0 {
		count = "none"
	}
	head := fmt.Sprintf("[dev todo] Your open to do list: %d open, %s new or changed since the last one", len(open), count)
	if unshown > 0 {
		head += fmt.Sprintf(", %d not in your start list", unshown)
	}
	if len(left) > 0 {
		head += ", " + strings.Join(left, ", ") + " left it"
	}
	if items == "" {
		return withTicked(ticked, head+" (dev todo lists them)."+tickLine), shown
	}
	rest := "; the others as before (dev todo lists them):"
	if len(shown) == len(open) {
		rest = ":"
	}
	return withTicked(ticked, head+rest+items+tickLine+whoLine+detailsLine(items)), shown
}

// Remind is l's reminder for its boss reading worker notes at now in conversation (its Claude session id): the full
// list (Reminder) when none is kept for that conversation (StartRemind or an earlier Remind in it; ForgetShown drops it),
// else only the items new or changed since under a count line. What it names is kept for the next one. The text holds
// even with err: the shown file not read (it is then the full list) or not written (it is then dropped).
func (s Store) Remind(l List, ticked []Ticked, conversation string, now time.Time) (text string, err error) {
	// No list and none kept: no files for it. No conversation (a hook without session_id) can't tell its own: the full list
	if _, err := os.Stat(s.shownPath()); conversation == "" || len(l.Items) == 0 && errors.Is(err, fs.ErrNotExist) {
		text, _ = l.reminder(ticked, nil, now)
		if conversation == "" {
			// What was kept no longer says what the boss got
			return text, s.ForgetShown()
		}
		return text, nil
	}
	err = s.locked(func() error {
		seen, readErr := s.readShown(conversation)
		var shown map[int]string
		text, shown = l.reminder(ticked, seen, now)
		// An item not named again is as the boss last got it; a ticked one still listed stays, to be named when it
		// leaves, unless the ticked lines name it now
		for _, it := range l.Items {
			told := it.Done && slices.ContainsFunc(ticked, func(t Ticked) bool { return t.ID == it.ID && t.Text == it.Text })
			if line, ok := seen[it.ID]; ok && shown[it.ID] == "" && !told {
				shown[it.ID] = line
			}
		}
		return errors.Join(readErr, s.keepShown(conversation, shown))
	})
	if text == "" && err != nil {
		// Not locked: the full list, ticked lines too, which TakeTicked already took
		text = l.Reminder(ticked, now)
	}
	return text, err
}

// StartRemind is l's start reminder (startReminder), what it names kept for the next Remind in conversation; the text
// holds even with err, the shown file not written.
func (s Store) StartRemind(l List, kept bool, age string, ticked []Ticked, conversation string, now time.Time) (text string, err error) {
	if _, err := os.Stat(s.shownPath()); conversation == "" || len(l.Items) == 0 && errors.Is(err, fs.ErrNotExist) {
		return l.startReminder(kept, age, ticked, now), nil
	}
	err = s.locked(func() error {
		text = l.startReminder(kept, age, ticked, now)
		if text == "" {
			// Nothing shown: the first reminder is the full list
			return s.forgetShown()
		}
		// One past startShown is kept as "": the next Remind lists it as not in the start list, not as changed
		_, shown := l.linesSince(startShown, nil, now)
		for _, it := range l.Items {
			if _, ok := shown[it.ID]; !ok && !it.Done {
				shown[it.ID] = ""
			}
		}
		return s.keepShown(conversation, shown)
	})
	if text == "" && err != nil {
		text = l.startReminder(kept, age, ticked, now)
	}
	return text, err
}

// ForgetShown drops what the boss last got of its list, as a compaction may have dropped it: the next Remind is the full list.
func (s Store) ForgetShown() error {
	// None kept: no lock file for a list that may not exist
	if _, err := os.Stat(s.shownPath()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return s.locked(s.forgetShown)
}

// forgetShown is ForgetShown under the list's lock, held by its caller.
func (s Store) forgetShown() error { return ignoreMissing(os.Remove(s.shownPath())) }

// keepShown writes shown as what the boss last got in conversation; one not written drops the earlier, which no longer
// says what the boss got.
func (s Store) keepShown(conversation string, shown map[int]string) error {
	if err := s.writeShown(conversation, shown); err != nil {
		return errors.Join(err, s.forgetShown())
	}
	return nil
}

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
