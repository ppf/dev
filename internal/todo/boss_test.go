package todo

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// An item on Boss is stored without a session, kept by a Replace that doesn't name another, given back by --on me;
// it is the boss's own: open, not on others, not the user's to tick, answer or start.
func TestOnBoss(t *testing.T) {
	s := store(t)
	s.boss = "boss-a"
	now := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{
		{Text: "parked", On: Boss, Session: &Session{ID: "x", Name: "x"}},
		{Text: "a person's", On: "Marek"},
		{Text: "mine"},
		{Text: "pick?", On: Boss, Options: []string{"Yes", "No"}},
		{Text: "linked", On: "w", Session: &Session{ID: "w", Name: "w"}},
	} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	byID := func(id int) Shown {
		l := read(t, s, now)
		return l.Items[slices.IndexFunc(l.Items, func(it Shown) bool { return it.ID == id })]
	}
	l := read(t, s, now)
	if got := byID(1); got.On != Boss || got.Session != nil {
		t.Errorf("stored as %q %+v, want on boss, no session", got.On, got.Session)
	}
	if n, on := l.Open(); n != 5 || on != 2 {
		t.Errorf("Open = %d, %d on others; want 5, 2 (the boss's own not on others)", n, on)
	}
	if err := s.DoneText(1, "parked", now); err == nil || !strings.Contains(err.Error(), "is on boss, not the user's to tick") {
		t.Errorf("tick: %v", err)
	}
	if _, err := s.Answer(4, "pick?", "Yes", 0, now); err == nil || !strings.Contains(err.Error(), "is on boss, not the user's to answer") {
		t.Errorf("answer: %v", err)
	}
	if _, _, err := s.Start(1, "parked", "boss-a", now); err == nil || !strings.Contains(err.Error(), "is on boss, not the user's to start") {
		t.Errorf("start: %v", err)
	}
	if err := s.Replace(1, Item{Text: "parked again"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(5, Item{On: Boss}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(4, Item{On: Me}, now); err != nil {
		t.Fatal(err)
	}
	if got, want := ids(read(t, s, now)), []string{"3 mine", "4 pick?", "1 parked again on boss", "2 a person's on Marek", "5 linked on boss"}; !slices.Equal(got, want) {
		t.Errorf("after replaces: %q, want %q", got, want)
	}
	if it := byID(5); it.Session != nil {
		t.Errorf("a linked item put on boss keeps its session %+v", it.Session)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Errorf("the boss ticks its own: %v", err)
	}
}

// The boss reads its own item as its own; a parked one (not doing) is never unchanged, one it works on is.
func TestOnBossLinesAndChecks(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{{Text: "parked", On: Boss}, {Text: "doing", On: Boss, Doing: &Doing{Text: "on it", At: t0.Unix()}}} {
		if _, err := s.Add(it, t0); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(t, s, t0).lines(0, t0); got != "\n1. parked (yours to do, --on boss)\n2. doing (yours to do, --on boss)" {
		t.Errorf("lines = %q", got)
	}
	later := t0.Add(3 * 24 * time.Hour)
	if got := checked(t, s, 1, later); got != "" {
		t.Errorf("parked: check %q, want none", got)
	}
	if got := checked(t, s, 2, later); got != "unchanged 3d: check it at its source" {
		t.Errorf("doing: check %q", got)
	}
	if got, want := nudges(t, s, later), []string{`#2 "doing" (yours, --on boss): unchanged 3d: check it at its source`}; !slices.Equal(got, want) {
		t.Errorf("nudges = %q, want %q", got, want)
	}
}

// A state note replaces the list's earlier one, also at the cap, under a new number; other kinds still stop there.
func TestNoteState(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	first, err := s.AddNote(Note{Kind: NoteState, Text: "6 fixes parked"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxNotes; i++ {
		if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil {
			t.Fatal(err)
		}
	}
	second, err := s.AddNote(Note{Kind: NoteState, Text: "7 fixes parked"}, now)
	if err != nil {
		t.Fatalf("state at the cap: %v", err)
	}
	notes, err := s.Notes(now)
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, n := range notes {
		if n.Kind == NoteState {
			states = append(states, n.Text)
		}
	}
	if len(notes) != MaxNotes || !slices.Equal(states, []string{"7 fixes parked"}) || second <= first {
		t.Errorf("%d notes, states %q, ids %d then %d; want %d, the new one only, a new number", len(notes), states, first, second, MaxNotes)
	}
	if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "one more"}, now); err == nil || !strings.Contains(err.Error(), "drop one first") {
		t.Errorf("a risk at the cap: %v", err)
	}
	if got := NotesText(notes, now); !strings.Contains(got, " state: 7 fixes parked") {
		t.Errorf("NotesText = %q", got)
	}
}
