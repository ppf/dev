package todo

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func itemIDs(items []Item) []int {
	var out []int
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestEndSession(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1700000000, 0)
	add := func(it Item, at time.Time) int {
		id, err := s.Add(it, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := add(Item{Text: "From before", On: "boss"}, t0)
	var got []SessionFacts
	write := func(f SessionFacts) error { got = append(got, f); return nil }

	// No mark and no start: nothing to write, but a session starts
	if wrote, err := s.EndSession(t0.Add(time.Minute), 0, 0, true, write); err != nil || wrote {
		t.Fatalf("first start = %v, %v: want nothing written", wrote, err)
	}
	if l := read(t, s, t0); l.Updated != t0.Unix() {
		t.Errorf("a session start moved the list's updated to %d", l.Updated)
	}
	t1 := t0.Add(time.Hour)
	added := add(Item{Text: "Fix #4", On: "boss"}, t1)
	ticked := add(Item{Text: "Merge #5", On: "boss"}, t1)
	if err := s.WithResult("PR #5 merged").Done([]int{ticked}, t1); err != nil {
		t.Fatal(err)
	}
	ask := add(Item{Text: "Ship it?", Options: []string{"yes", "no"}}, t1)
	add(Item{Text: "test", Quiet: true, On: "boss"}, t1)
	if _, err := s.AddNote(Note{Kind: NoteState, Text: "half-way: #4; next: PR", Why: "release"}, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(Note{Kind: NoteDecided, Text: "CSV only"}, t1); err != nil {
		t.Fatal(err)
	}
	// Replaced: numbered anew in its time, still from before
	if err := s.Replace(old, Item{Text: "From before, checked", On: "boss"}, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{added}, t1.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Two days on a change drops the ticks off the list and the finished items: the session's mark keeps them
	t2 := t1.Add(48 * time.Hour)
	add(Item{Text: "later test", Quiet: true, On: "boss"}, t2)
	if wrote, err := s.EndSession(t2, 0, 0, false, write); err != nil || !wrote {
		t.Fatalf("close = %v, %v: want written", wrote, err)
	}
	f := got[0]
	if f.Start != t0.Add(time.Minute).Unix() || f.End != t2.Unix() {
		t.Errorf("window %d-%d", f.Start, f.End)
	}
	if !slices.Equal(itemIDs(f.Ticked), []int{ticked, added}) || f.Ticked[0].Result != "PR #5 merged" {
		t.Errorf("ticked = %+v, want #%d (its result) then #%d", f.Ticked, ticked, added)
	}
	// The replaced item counts as added in it
	if !slices.Equal(itemIDs(f.Added), []int{old}) || !slices.Equal(itemIDs(f.Asks), []int{ask}) {
		t.Errorf("added %v asks %v", itemIDs(f.Added), itemIDs(f.Asks))
	}
	if f.State == nil || f.State.Why != "release" || len(f.Notes) != 1 || f.Notes[0].Text != "CSV only" {
		t.Errorf("state %+v notes %+v", f.State, f.Notes)
	}
	// Written: a second end writes nothing
	if wrote, err := s.EndSession(t2.Add(time.Minute), 0, 0, false, write); err != nil || wrote {
		t.Errorf("second end = %v, %v: want nothing", wrote, err)
	}

	// The next session: what the one before left open shows until done
	t3 := t2.Add(time.Hour)
	if _, err := s.EndSession(t3, 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	fresh := add(Item{Text: "New", On: "boss"}, t3.Add(time.Minute))
	if wrote, err := s.EndSession(t3.Add(time.Hour), 0, 0, true, write); err != nil || !wrote {
		t.Fatalf("clear = %v, %v", wrote, err)
	}
	if f := got[1]; !slices.Equal(itemIDs(f.Added), []int{fresh}) || !slices.Equal(itemIDs(f.Left), []int{old}) {
		t.Errorf("2nd session added %v left %v: want #%d added, #%d left", itemIDs(f.Added), itemIDs(f.Left), fresh, old)
	}
	if wrote, err := s.EndSession(t3.Add(2*time.Hour), 0, 0, false, write); err != nil || !wrote {
		t.Fatalf("close = %v, %v", wrote, err)
	}
	if f := got[2]; !slices.Equal(itemIDs(f.Left), []int{old, fresh}) || len(f.Added) != 0 {
		t.Errorf("3rd session left %v added %v: want #%d #%d left", itemIDs(f.Left), itemIDs(f.Added), old, fresh)
	}
}

// Numbers restart once a list empties: a new item under an old number is added, not left from before.
func TestEndSessionNumberReused(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1700000000, 0)
	var got []SessionFacts
	write := func(f SessionFacts) error { got = append(got, f); return nil }
	if _, err := s.EndSession(t0, 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "first", On: "boss"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EndSession(t0.Add(time.Hour), 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(2 * time.Hour)
	if err := s.Done([]int{1}, t1); err != nil {
		t.Fatal(err)
	}
	id, err := s.Add(Item{Text: "second", On: "boss"}, t1.Add(time.Minute))
	if err != nil || id != 1 {
		t.Fatalf("Add = %d, %v: want #1 again", id, err)
	}
	if _, err := s.EndSession(t1.Add(time.Hour), 0, 0, false, write); err != nil {
		t.Fatal(err)
	}
	if f := got[1]; len(f.Left) != 0 || len(f.Added) != 1 || f.Added[0].Text != "second" {
		t.Errorf("left %+v added %+v: want the new #1 added, nothing left", f.Left, f.Added)
	}
}

// only writes just the session that started then; Unend marks a written one unwritten again.
func TestEndSessionOnlyAndUnend(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1700000000, 0)
	write := func(SessionFacts) error { return nil }
	if _, err := s.EndSession(t0, 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	if wrote, err := s.EndSession(t0.Add(time.Hour), 0, t0.Unix()-1, false, write); err != nil || wrote {
		t.Errorf("another session's only = %v, %v: want nothing written", wrote, err)
	}
	if wrote, err := s.EndSession(t0.Add(time.Hour), 0, t0.Unix(), false, write); err != nil || !wrote {
		t.Errorf("its only = %v, %v: want written", wrote, err)
	}
	if err := s.Unend(t0.Unix()); err != nil {
		t.Fatal(err)
	}
	if start, un, err := s.SessionStart(); err != nil || start != t0.Unix() || !un {
		t.Errorf("after Unend = %d, %v, %v: want its session unwritten", start, un, err)
	}
}

func TestEndSessionFromAndFailure(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "a", On: "boss"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A failed write starts nothing; a session from from is kept unwritten, for a later start or the manager's
	if wrote, err := s.EndSession(t0.Add(time.Hour), t0.Unix(), 0, true, func(SessionFacts) error { return errors.New("disk") }); err == nil || wrote {
		t.Fatalf("failed write = %v, %v: want the error", wrote, err)
	}
	if start, un, err := s.SessionStart(); err != nil || !un || start != t0.Unix() {
		t.Errorf("after a failed write = %d, %v, %v: want the session from from kept unwritten", start, un, err)
	}
	var f SessionFacts
	if wrote, err := s.EndSession(t0.Add(time.Hour), 0, 0, false, func(x SessionFacts) error { f = x; return nil }); err != nil || !wrote {
		t.Fatalf("from = %v, %v", wrote, err)
	}
	if f.Start != t0.Unix() || len(f.Added) != 1 {
		t.Errorf("facts from the session's creation = %+v", f)
	}
	if _, un, err := s.SessionStart(); err != nil || un {
		t.Errorf("unwritten = %v, %v after the write", un, err)
	}
}

// A take-over keeps the taker's own session, not the list's earlier boss's.
func TestTakeOverKeepsSession(t *testing.T) {
	from, to := store(t), store(t)
	now := time.Unix(1700000000, 0)
	write := func(SessionFacts) error { return nil }
	if _, err := from.Add(Item{Text: "theirs", On: "boss"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.EndSession(now, 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	if _, err := from.EndSession(now.Add(time.Minute), 0, 0, false, write); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if _, err := to.EndSession(later, 0, 0, true, write); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if start, un, err := to.SessionStart(); err != nil || start != later.Unix() || !un {
		t.Errorf("taker's session = %d, %v, %v: want its own, unwritten", start, un, err)
	}
}
