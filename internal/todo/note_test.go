package todo

import (
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
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

// An id isn't used again while the file lives, the highest dropped or all; an older file's notes count too.
func TestNoteIDNotReused(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	add := func() int {
		t.Helper()
		id, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add()
	add()
	if err := s.DropNote(2, now); err != nil {
		t.Fatal(err)
	}
	if id := add(); id != 3 {
		t.Errorf("id after dropping the highest = %d, want 3", id)
	}
	for _, id := range []int{1, 3} {
		if err := s.DropNote(id, now); err != nil {
			t.Fatal(err)
		}
	}
	if id := add(); id != 4 {
		t.Errorf("id after dropping all = %d, want 4", id)
	}
	// A file written before NextNote: ids go on after its highest
	if err := s.locked(func() error {
		return s.write(file{Notes: []Note{{ID: 9, Kind: NoteRisk, Text: "old"}}})
	}); err != nil {
		t.Fatal(err)
	}
	if id := add(); id != 10 {
		t.Errorf("id after an older file's highest = %d, want 10", id)
	}
}

// A boss with notes and no open items is closed and its name opened again: the notes are still there after every
// read and write a start makes, also when the list sat under the earlier key (session id and creation time).
func TestNotesSurviveBossRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	boss := func(id, created string) Store {
		t.Helper()
		s, err := ForPane(tmux.NewFake(func([]string) (string, error) {
			return tmux.FormatRow("/tmp/tmux-1/default", "boss-p", "1", id, created), nil
		}), "%1")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := boss("$1", "1700000000")
	if _, err := first.Add(Item{Text: "ship"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddNote(Note{Kind: NoteDecided, Text: "CSV only", Item: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := first.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	again := boss("$2", "1700000100")
	if again != first {
		t.Fatalf("restarted boss's store %v, want %v", again, first)
	}
	// What a start does: read the list, take what ticked, look PRs up, and a changed list
	if _, err := again.Read(later); err != nil {
		t.Fatal(err)
	}
	if _, err := again.TakeTicked(later, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := again.Lookup(later, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func() error{
		func() error { return again.Clear(later) },
		func() error { return again.Set([]string{"next"}, later) },
		func() error { _, err := again.Add(Item{Text: "more"}, later); return err },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		n, err := again.Notes(later)
		if err != nil || len(n) != 1 || n[0].Text != "CSV only" || !n[0].ItemDone {
			t.Fatalf("notes = %+v, %v: want the done note kept", n, err)
		}
	}
	// A note-only list under the earlier key moves to the name's key
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old, err := open("/tmp/tmux-1/default-$7-1700000000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.AddNote(Note{Kind: NoteRisk, Text: "flaky"}, now); err != nil {
		t.Fatal(err)
	}
	if n, err := boss("$7", "1700000000").Notes(now); err != nil || len(n) != 1 || n[0].Text != "flaky" {
		t.Errorf("notes under the earlier key = %+v, %v: want them moved", n, err)
	}
}

// A note unchanged for 7 days is marked to re-check, each note by its own age.
func TestNotesTextStale(t *testing.T) {
	now := time.Unix(1700000000, 0)
	week := 7 * 24 * time.Hour
	note := func(age time.Duration) Note {
		return Note{ID: 1, Kind: NoteRisk, Text: "x", Added: now.Add(-age).Unix()}
	}
	if got := NotesText([]Note{note(week - time.Second)}, now); strings.Contains(got, "still true?") {
		t.Errorf("6d23h = %q, want no mark", got)
	}
	if got := NotesText([]Note{note(week)}, now); !strings.Contains(got, "x (7d, still true?)\n") {
		t.Errorf("7d = %q, want the mark", got)
	}
	done := note(8 * 24 * time.Hour)
	done.Item, done.ItemDone = 4, true
	got := NotesText([]Note{done, note(time.Hour)}, now)
	if strings.Count(got, "still true?") != 1 || !strings.Contains(got, "oldest 8d") || !strings.Contains(got, "(item #4 done) (8d, still true?)\n") {
		t.Errorf("mixed ages = %q, want only the 8d note marked, after its item flag", got)
	}
}
