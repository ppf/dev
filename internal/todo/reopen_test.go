package todo

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// reopenWriters are the six writers that rewrite a list (and prune it), each given a store and a time: the store
// holding the list after it.
var reopenWriters = []struct {
	name  string
	write func(s Store, at time.Time) (Store, error)
}{
	{"update", func(s Store, at time.Time) (Store, error) { _, err := s.Add(Item{Text: "other"}, at); return s, err }},
	{"TakeTicked", func(s Store, at time.Time) (Store, error) { _, err := s.TakeTicked(at, nil, nil); return s, err }},
	{"Lookup", func(s Store, at time.Time) (Store, error) {
		p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED"}}}
		return s, s.Lookup(at, LookupTTL, 0, p.view)
	}},
	{"AddNote", func(s Store, at time.Time) (Store, error) {
		_, err := s.AddNote(Note{Kind: NoteRisk, Text: "a risk"}, at)
		return s, err
	}},
	{"DropNote", func(s Store, at time.Time) (Store, error) {
		id, err := s.AddNote(Note{Kind: NoteRisk, Text: "a risk"}, at)
		if err != nil {
			return s, err
		}
		return s, s.DropNote(id, at)
	}},
	{"take", func(s Store, at time.Time) (Store, error) {
		to, err := Desktop("local_t" + strconv.FormatInt(at.Unix(), 10))
		if err != nil {
			return s, err
		}
		if taken, err := to.take(s); err != nil || !taken {
			return s, fmt.Errorf("take: %v %v", taken, err)
		}
		return to, nil
	}},
}

// reopenItem is the item ticked and reopened in the tests: every field a reopen keeps.
func reopenItem() Item {
	return Item{Text: "Review the plan", Link: prURL, Context: []string{"why: it ships today"}}
}

// openItem is s's open item id as written, and whether there is one.
func openItem(t *testing.T, s Store, id int) (Item, bool) {
	t.Helper()
	f, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
	if i < 0 {
		return Item{}, false
	}
	return f.Items[i], true
}

// A ticked item that a writer moved to the finished items comes back with its number and fields, and leaves the
// finished items; after it, each writer keeps it open and out of them.
func TestReopenThroughWriters(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	later := t0.Add(doneShown)
	for _, w := range reopenWriters {
		t.Run(w.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			s, _ := Desktop("local_boss")
			// What makes TakeTicked and Lookup write: an item ticking by itself, one ticking once its PR merged
			for _, it := range []Item{{Text: "first", Until: t0.Unix() + 1}, {Text: "pr", Link: prURL, DoneOn: OnMerged}} {
				if _, err := s.Add(it, t0); err != nil {
					t.Fatal(err)
				}
			}
			id, err := s.Add(reopenItem(), t0)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := openItem(t, s, id)
			if err := s.Done([]int{id}, t0); err != nil {
				t.Fatal(err)
			}
			// The writer moves it out of the list
			if s, err = w.write(s, later); err != nil {
				t.Fatal(err)
			}
			// take moves the files as they are: the item stays ticked in the list
			if _, done := stored(t, s); !slices.Contains(done, id) && w.name != "take" {
				t.Fatalf("finished %v: want #%d kept by %s", done, id, w.name)
			}
			got, err := s.Reopen(id, before.Text, later.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, before) {
				t.Errorf("reopened %+v, want %+v", got, before)
			}
			// And the writer after it keeps it open
			at := later.Add(2 * time.Minute)
			if s, err = w.write(s, at); err != nil {
				t.Fatal(err)
			}
			if it, ok := openItem(t, s, id); !ok || !reflect.DeepEqual(it, before) {
				t.Errorf("after %s: open #%d %+v (%v), want %+v", w.name, id, it, ok, before)
			}
			l, err := s.Read(at)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(l.Finished, func(f Finished) bool { return f.ID == id }) {
				t.Errorf("finished %+v: #%d still among them", l.Finished, id)
			}
			if !slices.ContainsFunc(l.Items, func(sh Shown) bool { return sh.ID == id && !sh.Done && sh.Text == before.Text }) {
				t.Errorf("items %+v: #%d not open", l.Items, id)
			}
		})
	}
}

// One still shown ticked, or past doneShown and not yet moved by a writer, comes back too.
func TestReopenNotMoved(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	for _, at := range []time.Time{t0.Add(time.Second), t0.Add(doneShown + time.Second)} {
		s := store(t)
		if _, err := s.Add(reopenItem(), t0); err != nil {
			t.Fatal(err)
		}
		before, _ := openItem(t, s, 1)
		if err := s.Done([]int{1}, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Reopen(1, before.Text, at); err != nil {
			t.Fatal(err)
		}
		if it, ok := openItem(t, s, 1); !ok || !reflect.DeepEqual(it, before) {
			t.Errorf("at +%v: open #1 %+v (%v), want %+v", at.Sub(t0), it, ok, before)
		}
		if items, done := stored(t, s); !slices.Equal(items, []int{1}) || len(done) != 0 {
			t.Errorf("at +%v: stored items %v, finished %v: want [1] and none", at.Sub(t0), items, done)
		}
	}
}

// A number taken meanwhile (numbers restart once the list empties) gives the next one; what would tick it again goes,
// as do its answer, doer, start and other half; a future --until stays.
func TestReopenNewNumberAndDroppedFields(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	later := t0.Add(doneShown)
	if _, err := s.Add(Item{Text: "ask?", Options: []string{"Yes", "No"}, Until: t0.Unix() + 3600}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "on merge", Link: prURL, DoneOn: OnMerged, Until: t0.Unix() + 1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerPick(1, "ask?", "Yes", "Yes", 0, t0); err != nil {
		t.Fatal(err)
	}
	// #2 ticks by itself at its --until; then the list empties and numbers restart
	if _, err := s.Add(Item{Text: "new one"}, later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if it, ok := openItem(t, s, 1); !ok || it.Text != "new one" {
		t.Fatalf("open #1 %+v: want the new one", it)
	}
	ask, err := s.Reopen(1, "ask?", later.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ask.ID != 2 || ask.Answer != "" || ask.Until != t0.Unix()+3600 || !slices.Equal(ask.Options, []string{"Yes", "No"}) {
		t.Errorf("reopened ask %+v: want #2, unanswered, its options and future --until kept", ask)
	}
	merged, err := s.Reopen(2, "on merge", later.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if merged.ID != 3 || merged.DoneOn != "" || merged.Until != 0 || merged.Link != prURL {
		t.Errorf("reopened %+v: want #3, no --done-on, no passed --until, its link", merged)
	}
	if id, err := s.Add(Item{Text: "after"}, later.Add(2*time.Minute)); err != nil || id != 4 {
		t.Errorf("next add got #%d (%v), want #4", id, err)
	}
}

// Refused, writing nothing: an item that was a worker's, the boss's own or a person's; another text; past a day.
func TestReopenRefused(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{
		{Text: "worker's", On: "w", Session: &Session{ID: "w", Name: "w"}},
		{Text: "boss's", On: Boss},
		{Text: "person's", On: "Priyanka"},
	} {
		s := store(t)
		if _, err := s.Add(it, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.Done([]int{1}, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Reopen(1, it.Text, t0.Add(time.Minute)); !errors.As(err, new(Refused)) {
			t.Errorf("%s: reopen err %v, want refused", it.Text, err)
		}
		if _, ok := openItem(t, s, 1); ok {
			t.Errorf("%s: reopened anyway", it.Text)
		}
	}
	s := store(t)
	if _, err := s.Add(Item{Text: "mine"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		text string
		at   time.Time
	}{{"not mine", t0.Add(time.Minute)}, {"mine", t0.Add(keptFor)}} {
		if _, err := s.Reopen(1, c.text, c.at); !errors.As(err, new(Refused)) {
			t.Errorf("%q at +%v: err %v, want refused", c.text, c.at.Sub(t0), err)
		}
	}
}

// A quick reopen keeps no link to its other half, from either side; an item that ticked by itself is no longer told
// as ticked.
func TestReopenUnlinksAndUntells(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "send"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "install", On: Boss, PartOf: 1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "until", Until: t0.Unix() + 1}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(2 * time.Second)
	if _, err := s.Reopen(1, "send", at); err != nil {
		t.Fatal(err)
	}
	for _, it := range read(t, s, at).Items {
		if it.PartOf != 0 {
			t.Errorf("#%d still part of #%d", it.ID, it.PartOf)
		}
	}
	if _, err := s.Reopen(3, "until", at); err != nil {
		t.Fatal(err)
	}
	if told, err := s.TakeTicked(at, nil, nil); err != nil || len(told) != 0 {
		t.Errorf("ticked told after reopen: %+v %v", told, err)
	}
}

// Two finished items by one number and text (numbers restart): the newest comes back, the older stays finished.
func TestReopenNewest(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	for i, link := range []string{"https://x/old", "https://x/new"} {
		at := t0.Add(time.Duration(i) * time.Minute)
		if _, err := s.Add(Item{Text: "same", Link: link}, at); err != nil {
			t.Fatal(err)
		}
		if err := s.Done([]int{1}, at); err != nil {
			t.Fatal(err)
		}
		// A write past doneShown moves it out; the list empties, so the next add is #1 again
		if err := s.Clear(at.Add(doneShown)); err != nil {
			t.Fatal(err)
		}
	}
	it, err := s.Reopen(1, "same", t0.Add(time.Hour))
	if err != nil || it.Link != "https://x/new" {
		t.Fatalf("reopened %+v %v: want the newer", it, err)
	}
	if l := read(t, s, t0.Add(time.Hour)); len(l.Finished) != 1 || l.Finished[0].Link != "https://x/old" {
		t.Errorf("finished %+v: want the older left", l.Finished)
	}
}
