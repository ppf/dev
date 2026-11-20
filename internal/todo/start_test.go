package todo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
)

// Any To do card starts, not an ask: the user's own, the boss's own parked one, a gone worker's, a closed boss's on a
// session; started, it sits In progress (the list's boss's) under its old status word; a late start goes again from
// wherever it sits now. A card in another column, or one changed since it was read, doesn't.
func TestStartShapes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	live := Owner{Name: "boss-x", Workers: []sidebar.Session{{Name: "busy", ID: "busy", Agent: "Claude", State: agent.Working}}}
	closed := Owner{Name: "boss-x", Closed: true}
	link := func(n string) *Session { return &Session{ID: n, Name: n} }
	for _, tt := range []struct {
		name       string
		owner      Owner
		it         Item
		status, on string // the old word once started
		refused    string
	}{
		{name: "own", owner: live, it: Item{Text: "a"}, status: StatusStarting, on: "boss-x"},
		{name: "parked", owner: live, it: Item{Text: "a", On: Boss}, status: StatusIdle, on: "boss-x"},
		{name: "gone worker", owner: live, it: Item{Text: "a", On: "gone", Session: link("gone")}, status: StatusGone, on: "gone"},
		{name: "closed, on a session", owner: closed, it: Item{Text: "a", On: "busy", Session: link("busy")}, status: StatusBossClosed, on: "busy"},
		{name: "closed, parked", owner: closed, it: Item{Text: "a", On: Boss}, status: StatusBossClosed, on: Boss},
		{name: "closed, own", owner: closed, it: Item{Text: "a"}, status: StatusStarting, on: "boss-x"},
		{name: "a live worker's", owner: live, it: Item{Text: "a", On: "busy", Session: link("busy")}, refused: "is in In progress (busy), not To do"},
		{name: "a person's", owner: live, it: Item{Text: "a", On: "Marek"}, refused: "is in Waiting (Marek), not To do"},
		{name: "closed, a person's", owner: closed, it: Item{Text: "a", On: "Marek"}, refused: "is in Waiting (Marek), not To do"},
		{name: "own, doing", owner: live, it: Item{Text: "a", Doing: &Doing{Text: "x", At: 1}}, refused: "is in In progress, not To do"},
		{name: "parked, doing", owner: live, it: Item{Text: "a", On: Boss, Doing: &Doing{Text: "x", At: 1}}, refused: "is in In progress (boss-x), not To do"},
		{name: "a gone worker's, workers unread", owner: Owner{Name: "boss-x", Unread: true}, it: Item{Text: "a", On: "gone", Session: link("gone")}, refused: "is in In progress (gone), not To do"},
		{name: "an ask on boss", owner: live, it: Item{Text: "a?", On: Boss, Options: []string{"x", "y"}}, refused: "is a question"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := store(t)
			if err := s.update(now, func(f *file) error {
				it := tt.it
				it.ID, it.Added = 1, now.Unix()
				f.Items = append(f.Items, it)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			sh := placed(t, s, tt.owner, 1, now)
			_, _, err := s.Start(sh, tt.it.Text, "boss-x", now)
			if tt.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refused) {
					t.Errorf("start: %v, want %q", err, tt.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			l := read(t, s, now)
			l.SetStatus(tt.owner)
			got := l.Items[0]
			if got.Column != sidebar.ColumnProgress || got.Assignee.Kind != sidebar.AssigneeBoss || got.Started == nil {
				t.Errorf("started = %+v, want In progress, the boss's, started", got.Placement)
			}
			if got.Status != tt.status || got.StatusOn != tt.on {
				t.Errorf("old word = %q · %q, want %q · %q", got.Status, got.StatusOn, tt.status, tt.on)
			}
			// Late: sent again, though it sits In progress
			late := now.Add(StartLate)
			if _, prev, err := s.Start(placed(t, s, tt.owner, 1, late), tt.it.Text, "manager", late); err != nil || prev.Started == nil {
				t.Errorf("late start: %v, prev %v", err, prev)
			}
		})
	}

	// Changed between the read and the start: refused, even with its text the same
	s := store(t)
	if _, err := s.Add(Item{Text: "a", On: Boss}, now); err != nil {
		t.Fatal(err)
	}
	sh := placed(t, s, live, 1, now)
	if err := s.Replace(1, Item{On: "Marek"}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(sh, "a", "boss-x", now); err == nil || !strings.Contains(err.Error(), "changed while it was read") {
		t.Errorf("start of a changed item: %v", err)
	}
	if _, err := s.Add(Item{Text: "b", On: Boss}, now); err != nil {
		t.Fatal(err)
	}
	sh = placed(t, s, live, 2, now)
	if err := s.update(now, func(f *file) error {
		f.Items[len(f.Items)-1].Doing = &Doing{Text: "on it", At: now.Unix()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(sh, "b", "boss-x", now); err == nil || !strings.Contains(err.Error(), "changed while it was read") {
		t.Errorf("start of an item the boss took up meanwhile: %v", err)
	}
	sh = placed(t, s, live, 2, now)
	if err := s.Replace(2, Item{Link: "https://github.com/o/r/pull/9"}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(sh, "b", "boss-x", now); err == nil || !strings.Contains(err.Error(), "changed while it was read") {
		t.Errorf("start of an item replaced meanwhile (same text and assignee): %v", err)
	}
	if _, _, err := s.Start(Shown{ID: 9}, "a", "boss-x", now); err == nil || !strings.Contains(err.Error(), "no open item 9") {
		t.Errorf("start of no item: %v", err)
	}
	// A late start whose PR moved since (in Review) is sent again
	if _, err := s.Add(Item{Text: "c"}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(placed(t, s, live, 3, now), "c", "boss-x", now); err != nil {
		t.Fatal(err)
	}
	late := now.Add(StartLate)
	sh = placed(t, s, live, 3, late)
	sh.Column = sidebar.ColumnReview
	if _, _, err := s.Start(sh, "c", "boss-x", late); err != nil {
		t.Errorf("a late start in Review: %v", err)
	}
	// A start ends doing: handed to a session, the user is no longer on it
	id, err := s.Add(Item{Text: "d"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(now, func(f *file) error {
		p := &f.Items[slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id })]
		p.Started, p.Doing = &Started{At: now.Add(-StartLate).Unix(), To: "boss-x"}, &Doing{Text: "on it", At: now.Unix()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	it, prev, err := s.Start(placed(t, s, live, id, now), "d", "boss-x", now)
	if err != nil || it.Doing != nil || it.Started == nil {
		t.Errorf("a late start of an item marked doing = %+v, %v; want started, doing ended", it, err)
	}
	if err := s.Unstart(id, it.Started.At, prev, now); err != nil {
		t.Fatal(err)
	}
	if err := s.update(now, func(f *file) error {
		p := f.Items[slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id })]
		if p.Doing == nil || p.Doing.Text != "on it" || p.Started == nil || p.Started.At != now.Add(-StartLate).Unix() {
			t.Errorf("after Unstart = %+v / %+v, want the late start and doing back", p.Started, p.Doing)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Ticked while its note was sent: the undo leaves it ticked, without doing or a start
	if it, prev, err = s.Start(placed(t, s, live, id, now.Add(StartLate)), "d", "boss-x", now.Add(StartLate)); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{id}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Unstart(id, it.Started.At, prev, now); err != nil {
		t.Fatal(err)
	}
	if err := s.update(now, func(f *file) error {
		p := f.Items[slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id })]
		if p.Doing != nil || *p.Started != *it.Started {
			t.Errorf("Unstart onto a ticked item = %+v / %+v, want it as ticked", p.Started, p.Doing)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The boss on its started parked item takes the start
	if id, err = s.Add(Item{Text: "e", On: Boss}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(placed(t, s, live, id, now), "e", "boss-x", now); err != nil {
		t.Fatal(err)
	}
	if it, err := s.SetDoing(id, "", false, &Doing{Text: "writing it"}, now); err != nil || it.Started != nil {
		t.Errorf("the boss on its started parked item = %+v, %v; want the start ended", it.Started, err)
	}
}

// A boss's start list words a started item by whose it is: the user's, its own parked one, a gone worker's, one on a
// worker still running.
func TestStartLines(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	started := &Started{At: now.Unix(), To: "boss-x"}
	link := func(n string) *Session { return &Session{ID: n, Name: n} }
	l := List{Items: []Shown{
		{ID: 1, Text: "mine", Started: started},
		{ID: 2, Text: "parked", On: Boss, Started: started},
		{ID: 3, Text: "lost", On: "gone", Session: link("gone"), Gone: true, Started: started},
		{ID: 4, Text: "runs", On: "busy", Session: link("busy"), Started: started},
	}}
	got := l.StartReminder(false, "1m ago", nil, now)
	for _, want := range []string{
		"\n1. mine (the user asked to start it: start a worker on it and link it with --replaces <n> --on <worker>)",
		"\n2. parked (yours to do, --on boss) (the user asked to start it: do it yourself, dev todo doing <n> takes the start, or start a worker on it and link it with --replaces <n> --on <worker>)",
		"\n3. lost (on gone, session gone) (the user asked to start it: start a fresh worker on it and link it with --replaces <n> --on <worker>)",
		"\n4. runs (on busy) (the user asked to start it: see what its worker does, or start a fresh one on it and link it with --replaces <n> --on <worker>)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StartReminder =\n%s\nwant a line\n%s", got, want)
		}
	}
}
