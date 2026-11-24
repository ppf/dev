package todo

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// After the full list, a boss's reminder lists only the items changed since, under a count line; items to re-check
// always show; ForgetShown (a start or compaction) brings the full list back.
func TestRemindChangesOnly(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, text := range []string{"a", "b", "c"} {
		if _, err := s.Add(Item{Text: text, Details: "d"}, now); err != nil {
			t.Fatal(err)
		}
	}
	remind := func() string {
		t.Helper()
		got, err := s.Remind(read(t, s, now), nil, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := remind(); !strings.HasPrefix(got, "[dev todo] Your open to do list:\n1. a\n2. b\n3. c\n") {
		t.Errorf("first = %q, want the full list", got)
	}
	got := remind()
	if !strings.HasPrefix(got, "[dev todo] Your open to do list: 3 open, none changed since the last one (dev todo lists them).") ||
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
	want := "[dev todo] Your open to do list: 3 open, 2 changed since the last one, #3 left it; the others as before (dev todo lists them):\n2. b2\n4. d\n"
	if !strings.HasPrefix(got, want) || strings.Contains(got, "1. a") {
		t.Errorf("changed = %q, want prefix %q", got, want)
	}
	l := read(t, s, now)
	l.Items[0].Gone, l.Items[0].On, l.Items[0].Session = true, "w", &Session{ID: "w", Name: "w"}
	if got, _ := s.Remind(l, nil, now); !strings.Contains(got, "Re-check these first") || !strings.Contains(got, "1. a (on w, session gone)") {
		t.Errorf("flagged = %q, want the re-check block", got)
	}
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
	if _, err := s.StartRemind(read(t, s, now), false, "now", nil, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Remind(read(t, s, now), nil, now)
	want := "22 open, 2 changed since the last one; the others as before (dev todo lists them):\n21. t21\n22. t22\n"
	if err != nil || !strings.Contains(got, want) || strings.Contains(got, "20. t20") {
		t.Errorf("after start = %q, %v; want %q", got, err, want)
	}
}
