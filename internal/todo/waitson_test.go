package todo

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// An ask --for an open item links them: the item shows the ask as WaitsOn while the ask is open; an answer or a tick
// of the ask clears it, --replaces keeps it, and a link to no open item, an ask or itself is refused.
func TestWaitsOn(t *testing.T) {
	s := Store{path: t.TempDir() + "/boss-x.json"}
	now := time.Unix(1700000000, 0)
	add := func(it Item) int {
		t.Helper()
		id, err := s.Add(it, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	waits := func(id int) *WaitsOn {
		t.Helper()
		l, err := s.Read(now)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range l.Items {
			if it.ID == id {
				return it.WaitsOn
			}
		}
		t.Fatalf("no item %d", id)
		return nil
	}
	item := add(Item{Text: "Contract: a worker's question", On: Boss})
	ask := add(Item{Text: "Start the worker for #1 now?", Options: []string{"Start now | a worker on #1 (Recommended)", "Later"}, For: item})
	if w := waits(item); w == nil || w.ID != ask || w.Text != "Start the worker for #1 now?" || len(w.Choices) != 2 || !w.Choices[0].Recommended {
		t.Fatalf("waits on = %+v, want ask %d with its options", w, ask)
	}
	if waits(ask) != nil {
		t.Error("the ask itself waits on nothing")
	}
	for _, c := range []struct {
		name string
		it   Item
		want string
	}{
		{"no open item", Item{Text: "Q?", Options: []string{"a", "b"}, For: 9}, "no open item 9"},
		{"an ask", Item{Text: "Q?", Options: []string{"a", "b"}, For: ask}, "is an ask itself"},
		{"no ask", Item{Text: "plain", For: item}, "only an ask"},
	} {
		if _, err := s.Add(c.it, now); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := s.ReplaceAsk(ask, Item{For: ask}, now); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Errorf("for itself: %v", err)
	}
	if _, err := s.ReplaceAsk(ask, Item{Context: []string{"more"}}, now); err != nil || waits(item) == nil {
		t.Errorf("--replaces dropped the link: %v %+v", err, waits(item))
	}
	// Linked anew to another item, then back
	other := add(Item{Text: "Other", On: Boss})
	if _, err := s.ReplaceAsk(ask, Item{For: other}, now); err != nil || waits(other) == nil || waits(other).ID != ask || waits(item) != nil {
		t.Errorf("--for anew: %v, other %+v, item %+v", err, waits(other), waits(item))
	}
	if _, err := s.ReplaceAsk(ask, Item{For: item}, now); err != nil {
		t.Fatal(err)
	}
	// Two open asks for it: the newest is the one it waits on, the older once the newest goes
	newer := add(Item{Text: "Or now?", Options: []string{"Yes", "No"}, For: item})
	if w := waits(item); w == nil || w.ID != newer {
		t.Errorf("two asks: waits on %+v, want %d", w, newer)
	}
	if err := s.Done([]int{newer}, now); err != nil {
		t.Fatal(err)
	}
	if w := waits(item); w == nil || w.ID != ask {
		t.Errorf("newest ticked: waits on %+v, want %d", w, ask)
	}
	if _, err := s.Answer(ask, "Start the worker for #1 now?", "Start now", 0, now); err != nil {
		t.Fatal(err)
	}
	if w := waits(item); w != nil {
		t.Errorf("answered: still waits on %+v", w)
	}
	// Reopened, the ask is unlinked: its decision was made
	reopened, err := s.Reopen(ask, "Start the worker for #1 now?", now)
	if err != nil || reopened.For != 0 || waits(item) != nil {
		t.Errorf("reopened: %v for %d, item waits on %+v", err, reopened.For, waits(item))
	}
	if err := s.Done([]int{reopened.ID}, now); err != nil {
		t.Fatal(err)
	}
	ask2 := add(Item{Text: "Ship it?", Options: []string{"Yes", "No"}, For: item})
	if waits(item) == nil {
		t.Fatal("a new ask for it: no link")
	}
	if err := s.Done([]int{ask2}, now); err != nil {
		t.Fatal(err)
	}
	if w := waits(item); w != nil {
		t.Errorf("ticked: still waits on %+v", w)
	}
	// The item ticked first: a later correction of its ask drops the link, not refused
	ask3 := add(Item{Text: "Again?", Options: []string{"Yes", "No"}, For: item})
	if err := s.Done([]int{item}, now); err != nil {
		t.Fatal(err)
	}
	if w := waits(item); w != nil {
		t.Errorf("a ticked item waits on %+v", w)
	}
	if _, err := s.ReplaceAsk(ask3, Item{Context: []string{"x"}}, now); err != nil {
		t.Errorf("correcting an ask whose item is ticked: %v", err)
	}
	f, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if a := f.Items[slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == ask3 })]; a.For != 0 {
		t.Errorf("the ticked item's link kept: for %d", a.For)
	}
}
