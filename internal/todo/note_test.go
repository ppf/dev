package todo

import (
	"strings"
	"testing"
	"time"
)

func TestNotes(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "Merge #4"}, now); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddNote(Note{Kind: NoteDecided, Text: "CSV only", Why: "parser", Item: 1}, now)
	if err != nil || id != 1 {
		t.Fatalf("AddNote = %d, %v", id, err)
	}
	until := now.Add(time.Hour).Unix()
	if id, err = s.AddNote(Note{Kind: NoteSaid, Text: "no merges", Until: until}, now); err != nil || id != 2 {
		t.Fatalf("AddNote = %d, %v", id, err)
	}
	if l := read(t, s, now); l.Updated != now.Unix() {
		t.Errorf("a note moved the list's updated to %d", l.Updated)
	}
	got, err := s.Notes(now.Add(26 * time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != 1 || got[0].ItemDone {
		t.Fatalf("after until = %+v, %v: want n1 only (n2 dropped itself)", got, err)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	text := func(at time.Time) string {
		n, err := s.Notes(at)
		if err != nil {
			t.Fatal(err)
		}
		return NotesText(n, at)
	}
	if got := text(now.Add(time.Minute)); !strings.Contains(got, "(item #1 done)") {
		t.Errorf("done item's note = %q, want it flagged", got)
	}
	want := "[dev todo] Your notes (1 of 12, oldest 3d):\n n1 " + time.Unix(1700000000, 0).Format(time.DateOnly) + " decided: CSV only (why: parser) (item #1 done)\nCheck each still holds: drop what's over (dev todo note drop <n>)."
	if got := text(now.Add(72 * time.Hour)); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if err := s.DropNote(1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DropNote(1, now); err == nil {
		t.Error("dropping a dropped note: want an error")
	}
	if got := text(now.Add(2 * time.Hour)); got != "" {
		t.Errorf("no notes = %q, want empty", got)
	}
}

func TestNoteRefusals(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	long := strings.Repeat("é", MaxNote+1)
	for name, n := range map[string]Note{
		"unknown kind":     {Kind: "idea", Text: "x"},
		"empty":            {Kind: NoteRisk},
		"long text":        {Kind: NoteRisk, Text: long},
		"long why":         {Kind: NoteDecided, Text: "x", Why: long},
		"said without end": {Kind: NoteSaid, Text: "x"},
		"item not on list": {Kind: NoteRisk, Text: "x", Item: 7},
	} {
		if _, err := s.AddNote(n, now); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := s.AddNote(Note{Kind: NoteRisk, Text: strings.Repeat("é", MaxNote)}, now); err != nil {
		t.Errorf("a text of %d characters: %v", MaxNote, err)
	}
	for i := 1; i < MaxNotes; i++ {
		if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "one more"}, now); err == nil || !strings.Contains(err.Error(), "drop one first") {
		t.Errorf("13th note = %v, want drop one first", err)
	}
}

// Set and Clear leave the notes, and a list moving between keys takes them along.
func TestNotesSurviveListChanges(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "flaky"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Set([]string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(now); err != nil {
		t.Fatal(err)
	}
	to, err := Desktop("local_next")
	if err != nil {
		t.Fatal(err)
	}
	if taken, err := to.take(s); err != nil || !taken {
		t.Fatalf("take = %v, %v", taken, err)
	}
	if n, err := to.Notes(now); err != nil || len(n) != 1 || n[0].Text != "flaky" {
		t.Errorf("notes after take = %+v, %v", n, err)
	}
}

// A note tied to an item stays done once the list restarts its numbers, however the item left.
func TestNoteItemNumberReuse(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, leave := range []func() error{
		func() error { return s.Done([]int{1}, now) },
		func() error { return s.Clear(now) },
		func() error { return s.Set([]string{"other"}, now) },
	} {
		if err := s.Set([]string{"Merge"}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r", Item: 1}, now); err != nil {
			t.Fatal(err)
		}
		if err := leave(); err != nil {
			t.Fatal(err)
		}
		later := now.Add(time.Minute)
		if err := s.Clear(later); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(Item{Text: "unrelated"}, later); err != nil {
			t.Fatal(err)
		}
		n, err := s.Notes(later)
		if err != nil || len(n) == 0 || !n[len(n)-1].ItemDone {
			t.Errorf("notes after a new item 1 = %+v, %v: want the note still done", n, err)
		}
		if err := s.DropNote(n[len(n)-1].ID, later); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNoteIDAfterDrop(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for range 2 {
		if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DropNote(2, now); err != nil {
		t.Fatal(err)
	}
	if id, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil || id != 2 {
		t.Errorf("id after dropping the highest = %d, %v, want 2", id, err)
	}
}
