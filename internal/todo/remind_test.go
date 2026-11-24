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
// always show, counted when newly to re-check; an item that left is named once; ForgetShown (a compaction) or another
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
		// A minute on: a ticked item has left the list
		got, err := s.Remind(read(t, s, now.Add(time.Minute)), nil, conversation, now)
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
	want = "[dev todo] Your open to do list: 3 open, 1 new or changed since the last one; the others as before (dev todo lists them):\n" +
		"Re-check these first: tick it, replace it with what's true now, or confirm it holds (dev todo add --replaces <n>, with --details when it is the user's and has none):\n1. a (on w, session gone)\nTick"
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

// The start list names at most startShown items, then "+N more": the next reminder lists those as not in it, once.
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
	want := "[dev todo] Your open to do list: 22 open, none new or changed since the last one, 2 not in your start list; the others as before (dev todo lists them):\n21. t21\n22. t22\n"
	if err != nil || !strings.HasPrefix(got, want) {
		t.Errorf("after start = %q, %v; want %q", got, err, want)
	}
	if got, _ := s.Remind(read(t, s, now), nil, "c1", now); !strings.HasPrefix(got, "[dev todo] Your open to do list: 22 open, none new or changed since the last one (dev todo lists them).") {
		t.Errorf("next = %q, want them told once", got)
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

// A replace that leaves an item's line as it was (its link, details, a confirm) still counts: its revision moved.
func TestRemindReplacedSameLine(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "a", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remind(read(t, s, now), nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := s.Replace(1, Item{Text: "a", Details: "other"}, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Remind(read(t, s, later), nil, "c1", later); !strings.Contains(got, "1 new or changed since the last one:\n1. a\n") {
		t.Errorf("replaced = %q, want it listed", got)
	}
}

// "Left it" names neither one the start list never showed nor one the ticked lines name; an empty start keeps nothing.
func TestRemindLeftAndEmptyStart(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.StartRemind(read(t, s, now), false, "now", nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= startShown+1; i++ {
		if _, err := s.Add(Item{Text: "t" + strconv.Itoa(i), Details: "d"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Remind(read(t, s, now), nil, "c1", now); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. t1\n") {
		t.Errorf("after an empty start = %q, want the full list", got)
	}
	if _, err := s.StartRemind(read(t, s, now), false, "now", nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1, 2, startShown + 1}, now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Remind(read(t, s, now.Add(time.Minute)), []Ticked{{ID: 2, Text: "t2"}}, "c1", now)
	if !strings.Contains(got, "#1 left it") || strings.Contains(got, "#2,") || strings.Contains(got, "#21") {
		t.Errorf("left = %q, want #1 alone", got)
	}
}

// A hook without a conversation id gets the full list, kept nowhere; an item ticked but still on the list didn't leave it.
func TestRemindNoConversationAndTickedStill(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, text := range []string{"a", "b"} {
		if _, err := s.Add(Item{Text: text, Details: "d"}, now); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if got, _ := s.Remind(read(t, s, now), nil, "", now); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n") {
			t.Errorf("no conversation = %q, want the full list", got)
		}
	}
	if _, err := s.Remind(read(t, s, now), nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Remind(read(t, s, now), nil, "c1", now); strings.Contains(got, "left it") {
		t.Errorf("ticked, still listed = %q, want no left it", got)
	}
	if got, _ := s.Remind(read(t, s, now.Add(time.Minute)), nil, "c1", now); !strings.Contains(got, "#1 left it") {
		t.Errorf("once it left = %q, want #1 left it", got)
	}
}

// A tick names the item it ticked by its exact text: one whose text another's starts with isn't it.
func TestRemindTickedExactText(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "Review PR follow-up", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "keep", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remind(read(t, s, now), nil, "c1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Remind(read(t, s, now.Add(time.Minute)), []Ticked{{ID: 1, Text: "Review PR"}}, "c1", now)
	if !strings.Contains(got, "#1 left it") {
		t.Errorf("another text ticked = %q, want #1 left it", got)
	}
}
