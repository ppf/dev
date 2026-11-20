package todo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// The user marks only their own item that is no ask, while it reads its text; a boss only its own (--on boss); ending
// what isn't marked, and a line over MaxDoing, are refused, all as Refused.
func TestSetDoing(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	w := &Session{ID: "w", Name: "fix-login"}
	for _, it := range []Item{{Text: "mine"}, {Text: "parked", On: Boss}, {Text: "q?", Options: []string{"x", "y"}}, {Text: "theirs", On: "fix-login", Session: w}, {Text: "Carl's", On: "Carl"}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	d := func(text string) *Doing { return &Doing{Text: text} }
	for _, tt := range []struct {
		id    int
		text  string
		user  bool
		doing *Doing
		want  string
	}{
		{1, "other", true, d("x"), "look at the list again"},
		{2, "parked", true, d("x"), "on boss, not the user's"},
		{3, "q?", true, d("x"), "is a question"},
		{4, "theirs", true, d("x"), "on fix-login, not the user's"},
		{5, "Carl's", true, d("x"), "on Carl, not the user's"},
		{1, "", false, d("x"), "isn't your own (--on boss)"},
		{4, "", false, d("x"), "isn't your own (--on boss)"},
		{1, "mine", true, nil, "isn't marked doing"},
		{9, "", true, d("x"), "no open item 9"},
		{1, "mine", true, d(strings.Repeat("x", MaxDoing+1)), "at most 100"},
	} {
		if _, err := s.SetDoing(tt.id, tt.text, tt.user, tt.doing, now); err == nil || !strings.Contains(err.Error(), tt.want) || !errors.As(err, new(Refused)) {
			t.Errorf("SetDoing(%d, %q, user %v): %v, want Refused %q", tt.id, tt.text, tt.user, err, tt.want)
		}
	}
	it, err := s.SetDoing(1, "mine", true, d("  sending\tthe cert "), now)
	if err != nil || it.Doing == nil || *it.Doing != (Doing{Text: "sending the cert", At: now.Unix()}) {
		t.Fatalf("user SetDoing = %+v, %v", it.Doing, err)
	}
	if _, err := s.SetDoing(2, "", false, d(""), now); err != nil {
		t.Fatal(err)
	}
	f, _ := s.read()
	if f.Items[0].Doing == nil || f.Items[0].Doing.Text != "sending the cert" || f.Items[1].Doing == nil || f.Items[1].Doing.Text != "" {
		t.Errorf("stored: %+v / %+v, want both doing", f.Items[0].Doing, f.Items[1].Doing)
	}
	if it, err := s.SetDoing(1, "", true, nil, now); err != nil || it.Doing != nil {
		t.Errorf("stop = %+v, %v", it.Doing, err)
	}
	if f, _ := s.read(); f.Items[0].Doing != nil {
		t.Errorf("after stop: %+v", f.Items[0].Doing)
	}
}

// Doing places the user's item and the boss's own in progress, under the old words yours and working.
func TestDoingPlaced(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, it := range []Item{{Text: "mine"}, {Text: "parked", On: Boss}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetDoing(1, "mine", true, &Doing{Text: "x"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDoing(2, "", false, &Doing{}, now); err != nil {
		t.Fatal(err)
	}
	l := read(t, s, now)
	l.SetStatus(Owner{Name: "boss-x", Workers: []sidebar.Session{}})
	for i, want := range []struct{ status, kind string }{{StatusYours, sidebar.AssigneeYou}, {StatusWorking, sidebar.AssigneeBoss}} {
		if it := l.Items[i]; it.Column != sidebar.ColumnProgress || it.Status != want.status || it.Assignee.Kind != want.kind {
			t.Errorf("item %d: column %q status %q assignee %+v, want progress %s %s", it.ID, it.Column, it.Status, it.Assignee, want.status, want.kind)
		}
	}
}

// A tick, by hand, by --text, by an answer or by --until, ends doing; a replace keeps it while its --on stays, and
// ends it once another takes the item.
func TestDoingEnds(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, it := range []Item{{Text: "a"}, {Text: "b"}, {Text: "c", Until: now.Unix() + 60}, {Text: "d"}, {Text: "e", On: Boss}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	for id, text := range map[int]string{1: "a", 2: "b", 3: "c", 4: "d"} {
		if _, err := s.SetDoing(id, text, true, &Doing{Text: "on it"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetDoing(5, "", false, &Doing{Text: "on it"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DoneText(2, "b", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(4, Item{Text: "d2"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(5, Item{Text: "e2"}, now); err != nil {
		t.Fatal(err)
	}
	f, _ := s.read()
	for _, it := range f.Items[:2] {
		if it.Done == 0 || it.Doing != nil {
			t.Errorf("ticked item %d: done %d doing %+v, want ticked, no doing", it.ID, it.Done, it.Doing)
		}
	}
	if err := s.update(now.Add(time.Minute), func(*file) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.read(); f.Items[0].ID != 3 || f.Items[0].Done == 0 || f.Items[0].Doing != nil {
		t.Errorf("--until tick: %+v, want ticked, no doing", f.Items[0])
	}
	l := read(t, s, now.Add(time.Minute))
	for _, it := range l.Items {
		if it.ID >= 4 && (it.Doing == nil || it.Doing.Text != "on it") {
			t.Errorf("replaced item %d: doing %+v, want kept", it.ID, it.Doing)
		}
	}
	if err := s.Replace(4, Item{On: "Carl"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(5, Item{On: Me}, now); err != nil {
		t.Fatal(err)
	}
	for _, it := range read(t, s, now).Items {
		if it.ID >= 4 && it.Doing != nil {
			t.Errorf("item %d handed on: doing %+v, want ended", it.ID, it.Doing)
		}
	}
}

// An answer ends doing too (an ask is never marked by the user, but an older one may be).
func TestDoingEndsOnAnswer(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if err := s.update(now, func(f *file) error {
		f.Items = []Item{{ID: 1, Text: "q?", Options: []string{"x", "y"}, Doing: &Doing{Text: "old"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	it, err := s.Answer(1, "q?", "x", 0, now)
	if err != nil || it.Doing != nil {
		t.Errorf("answer = %+v, %v; want no doing", it.Doing, err)
	}
}

// --part-of links two open items, shown both ways; it refuses itself, no such open item, and an item linked already;
// a replace keeps it or sets a new one; the link clears once its other half leaves.
func TestPartOf(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, it := range []Item{{Text: "send the cert"}, {Text: "c"}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.Add(Item{Text: "install the cert", On: Boss, PartOf: 1}, now)
	if err != nil || id != 3 {
		t.Fatalf("add --part-of 1 = %d, %v", id, err)
	}
	partOf := func(l List) map[int]int {
		m := map[int]int{}
		for _, it := range l.Items {
			m[it.ID] = it.PartOf
		}
		return m
	}
	if got := partOf(read(t, s, now)); got[1] != 3 || got[3] != 1 || got[2] != 0 {
		t.Errorf("part_of = %v, want 1⇄3", got)
	}
	for _, tt := range []struct {
		partOf int
		want   string
	}{
		{1, "already linked"},
		{3, "already linked"},
		{9, "no open item 9"},
	} {
		if _, err := s.Add(Item{Text: "x", PartOf: tt.partOf}, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("add --part-of %d: %v, want %q", tt.partOf, err, tt.want)
		}
	}
	if err := s.Replace(2, Item{PartOf: 2}, now); err == nil || !strings.Contains(err.Error(), "isn't part of itself") {
		t.Errorf("replace --part-of itself: %v", err)
	}
	if err := s.Replace(2, Item{PartOf: 1}, now); err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Errorf("replace onto a linked item: %v", err)
	}
	if err := s.Replace(3, Item{Text: "install it", On: "Carl"}, now); err != nil {
		t.Fatal(err)
	}
	if got := partOf(read(t, s, now)); got[1] != 3 || got[3] != 1 {
		t.Errorf("after --replaces: %v, want 1⇄3 kept", got)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	if got := partOf(read(t, s, now.Add(time.Second))); got[3] != 1 {
		t.Errorf("other half ticked, still shown: %v, want 3→1", got)
	}
	if got := partOf(read(t, s, now.Add(doneShown))); got[3] != 0 {
		t.Errorf("other half gone: %v, want 3 unlinked", got)
	}
	if err := s.Replace(2, Item{PartOf: 3}, now.Add(doneShown)); err != nil {
		t.Fatalf("relink after unlink: %v", err)
	}
	if got := partOf(read(t, s, now.Add(doneShown))); got[2] != 3 || got[3] != 2 {
		t.Errorf("replace --part-of 3: %v, want 2⇄3", got)
	}
}

// A boss's list names what the user and the boss are on, so a cleared or reopened boss learns it.
func TestDoingLines(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	for _, it := range []Item{{Text: "send the cert"}, {Text: "install it", On: Boss}, {Text: "read"}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetDoing(1, "send the cert", true, &Doing{Text: "mailing ops"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDoing(2, "", false, &Doing{}, now); err != nil {
		t.Fatal(err)
	}
	got := read(t, s, now).lines(0, now)
	for _, want := range []string{"\n1. send the cert (the user is on it: mailing ops)", "\n2. install it (yours to do, --on boss) (you are on it)", "\n3. read"} {
		if !strings.Contains(got, want) {
			t.Errorf("lines = %q, want %q", got, want)
		}
	}
}
