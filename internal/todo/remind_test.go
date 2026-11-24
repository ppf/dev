package todo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// After the full list, a boss's reminder lists only the items changed since, under a count line; items to re-check
// always show, aside from the count; an item that left is named once; ForgetShown (a compaction) or another
// conversation brings the full list back.
func TestRemindChangesOnly(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, text := range []string{"a", "b", "c"} {
		if _, err := s.Add(Item{Text: text, Details: "d"}, now); err != nil {
			t.Fatal(err)
		}
	}
	remindIn := func(conversation string) string {
		t.Helper()
		got, err := s.Remind(read(t, s, now), nil, conversation, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	remind := func() string { t.Helper(); return remindIn("c1") }
	if got := remind(); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n2. b\n3. c\n") {
		t.Errorf("first = %q, want the full list", got)
	}
	got := remind()
	if !strings.HasPrefix(got, "[dev todo] Your open to do list: 3 open, none new or changed since the last one (dev todo lists them).") ||
		strings.Contains(got, "1. a") || !strings.Contains(got, "Tick what these notes closed") {
		t.Errorf("unchanged = %q, want the count line alone", got)
	}
	if err := s.Replace(2, Item{Text: "b2", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{3}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "d", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	got = remind()
	want := "[dev todo] Your open to do list: 3 open, 2 new or changed since the last one, #3 left it; the others as before (dev todo lists them):\n2. b2\n4. d\n"
	if !strings.HasPrefix(got, want) || strings.Contains(got, "1. a") {
		t.Errorf("changed = %q, want prefix %q", got, want)
	}
	if got := remind(); !strings.HasPrefix(got, "[dev todo] Your open to do list: 3 open, none new or changed since the last one (dev todo lists them).") {
		t.Errorf("after the change = %q, want no item and #3 not named again", got)
	}
	l := read(t, s, now)
	l.Items[0].Gone, l.Items[0].On, l.Items[0].Session = true, "w", &Session{ID: "w", Name: "w"}
	want = "[dev todo] Your open to do list: 3 open, none new or changed since the last one; the others as before (dev todo lists them):\n" +
		"Re-check these first: tick it, replace it with what's true now, or confirm it holds (dev todo add --replaces <n>):\n1. a (on w, session gone)\nTick"
	if got, _ := s.Remind(l, nil, "c1", now); !strings.HasPrefix(got, want) {
		t.Errorf("flagged = %q, want prefix %q", got, want)
	}
	if got := remindIn("c2"); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n2. b2\n4. d\n") {
		t.Errorf("another conversation = %q, want the full list", got)
	}
	remind()
	if err := s.ForgetShown(); err != nil {
		t.Fatal(err)
	}
	if got := remind(); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n2. b2\n4. d\n") {
		t.Errorf("after ForgetShown = %q, want the full list", got)
	}
}

// The start list names at most startShown items: the next reminder lists the ones it left out, once.
func TestStartRemindKeepsShown(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for i := 1; i <= startShown+2; i++ {
		if _, err := s.Add(Item{Text: "t" + strconv.Itoa(i), Details: "d"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartRemind(read(t, s, now), false, "now", nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Remind(read(t, s, now), nil, "c1", now)
	want := "22 open, 2 new or changed since the last one; the others as before (dev todo lists them):\n21. t21\n22. t22\n"
	if err != nil || !strings.Contains(got, want) || strings.Contains(got, "20. t20") {
		t.Errorf("after start = %q, %v; want %q", got, err, want)
	}
}

// A shown file that can't be read or written still gives the reminder, the full list when unread, with the error.
func TestRemindShownFailing(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "a", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.shownPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.shownPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Remind(read(t, s, now), nil, "c1", now)
	if err == nil || !strings.Contains(err.Error(), "the full list goes out") || !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n") {
		t.Errorf("not JSON = %q, %v; want the full list and the error", got, err)
	}
	if got, err := s.Remind(read(t, s, now), nil, "c1", now); err != nil || !strings.Contains(got, "none new or changed") {
		t.Errorf("rewritten = %q, %v; want the count line", got, err)
	}
	// A file where its directory goes: nothing can be written
	dir := filepath.Dir(s.shownPath())
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = s.Remind(read(t, s, now), []Ticked{{ID: 9, Text: "x"}}, "c1", now)
	if err == nil || !strings.Contains(got, "Ticked by itself") || !strings.Contains(got, "1. a") {
		t.Errorf("not written = %q, %v; want the reminder with the tick and the error", got, err)
	}
}

// A new item to re-check counts as new; a list emptied names what left it.
func TestRemindFlaggedAndEmptied(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "a", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remind(read(t, s, now), nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "b", On: "w", Session: &Session{ID: "w", Name: "w"}}, now); err != nil {
		t.Fatal(err)
	}
	l := read(t, s, now)
	l.Items[1].Gone = true
	if got, _ := s.Remind(l, nil, "c1", now); !strings.HasPrefix(got, "[dev todo] Your open to do list: 2 open, 1 new or changed since the last one;") {
		t.Errorf("new flagged = %q, want it counted", got)
	}
	if err := s.Done([]int{1, 2}, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Remind(read(t, s, now.Add(time.Minute)), nil, "c1", now); got != "[dev todo] Your open to do list is empty now: #1, #2 left it." {
		t.Errorf("emptied = %q, want what left it", got)
	}
}
