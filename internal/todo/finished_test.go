package todo

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// stored is the ids of the items in s's list file and in its finished file, as written.
func stored(t *testing.T, s Store) (items, done []int) {
	items, done, _ = storedTexts(t, s)
	return items, done
}

// storedTexts is stored, with the texts of the finished items.
func storedTexts(t *testing.T, s Store) (items, done []int, texts []string) {
	t.Helper()
	f, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range f.Items {
		items = append(items, it.ID)
	}
	kept, err := s.readDone(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range kept {
		done = append(done, it.ID)
		texts = append(texts, it.Text)
	}
	return items, done, texts
}

// Each writer that rewrites a list keeps the ticked items it drops among the finished items, once.
func TestFinishedWriters(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	later := t0.Add(doneShown)
	for _, w := range []struct {
		name  string
		setup func(Store) error
		write func(Store) error
	}{
		{"update", nil, func(s Store) error { _, err := s.Add(Item{Text: "next"}, later); return err }},
		{"TakeTicked", func(s Store) error {
			_, err := s.Add(Item{Text: "until", Until: t0.Unix() + 1}, t0)
			return err
		}, func(s Store) error { _, err := s.TakeTicked(later.Add(time.Second), nil, nil); return err }},
		{"Lookup", func(s Store) error {
			_, err := s.Add(Item{Text: "on merge", Link: prURL, DoneOn: OnMerged}, t0)
			return err
		}, func(s Store) error {
			p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED"}}}
			return s.Lookup(later, LookupTTL, 0, p.view)
		}},
		{"AddNote", nil, func(s Store) error {
			_, err := s.AddNote(Note{Kind: NoteRisk, Text: "a risk"}, later)
			return err
		}},
		{"DropNote", func(s Store) error {
			_, err := s.AddNote(Note{Kind: NoteRisk, Text: "a risk"}, t0)
			return err
		}, func(s Store) error { return s.DropNote(1, later) }},
	} {
		t.Run(w.name, func(t *testing.T) {
			s := store(t)
			if _, err := s.Add(Item{Text: "ticked"}, t0); err != nil {
				t.Fatal(err)
			}
			if w.setup != nil {
				if err := w.setup(s); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Done([]int{1}, t0); err != nil {
				t.Fatal(err)
			}
			if err := w.write(s); err != nil {
				t.Fatal(err)
			}
			f, err := s.read()
			if err != nil {
				t.Fatal(err)
			}
			_, _, texts := storedTexts(t, s)
			if slices.ContainsFunc(f.Items, func(it Item) bool { return it.Text == "ticked" }) ||
				!slices.Equal(slices.DeleteFunc(texts, func(x string) bool { return x != "ticked" }), []string{"ticked"}) {
				t.Errorf("list %+v, finished %q: want #1 moved once", f.Items, texts)
			}
			l, err := s.Read(later)
			if err != nil {
				t.Fatal(err)
			}
			if i := slices.IndexFunc(l.Finished, func(f Finished) bool { return f.Text == "ticked" }); i < 0 || l.Finished[i].DoneAt != t0.Unix() {
				t.Errorf("Read's finished %+v: want #1 done at %d", l.Finished, t0.Unix())
			}
		})
	}
}

// A ticked item past doneShown shows among the finished items before a writer moved it, not as an item, and once
// after a writer kept it twice (a list write failing after it); its --until tick and its answer come along.
func TestFinishedRead(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{{Text: "a"}, {Text: "b?", Options: []string{"Yes", "No"}}, {Text: "c", Until: t0.Unix() + 60}, {Text: "d"}} {
		if _, err := s.Add(it, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(2, "b?", "Yes", 0, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(2 * time.Minute)
	l, err := s.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range l.Finished {
		got = append(got, fmt.Sprintf("%d %s %s %d", f.ID, f.Text, f.Answer, f.DoneAt-t0.Unix()))
	}
	if want := []string{"3 c  60", "2 b? Yes 1", "1 a  0"}; !slices.Equal(got, want) {
		t.Errorf("finished = %q, want %q", got, want)
	}
	if want := []string{"4 d"}; !slices.Equal(ids(l), want) {
		t.Errorf("items = %q, want %q", ids(l), want)
	}
	f, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	f.prune(now)
	if err := s.keepDone(f.dropped, now); err != nil {
		t.Fatal(err)
	}
	if l, err = s.Read(now); err != nil || len(l.Finished) != 3 {
		t.Errorf("kept and still in the list: %d finished, %v", len(l.Finished), err)
	}
	// Kept by a Set or Clear while Read held the list from before: shown as listed, not finished too
	if err := s.Done([]int{4}, now); err != nil {
		t.Fatal(err)
	}
	if f, err = s.read(); err != nil {
		t.Fatal(err)
	}
	if err := s.keepDone(f.Items[len(f.Items)-1:], now); err != nil {
		t.Fatal(err)
	}
	if l, err = s.Read(now); err != nil || len(l.Finished) != 3 || len(l.Items) != 1 {
		t.Errorf("kept while still listed: %d items, %d finished, %v", len(l.Items), len(l.Finished), err)
	}
	// Two items by one number, added and ticked in one second (a Set between): both kept
	twins := []Item{{ID: 9, Text: "x", Added: t0.Unix(), Done: t0.Unix()}, {ID: 9, Text: "y", Added: t0.Unix(), Done: t0.Unix()}}
	if got := finished(twins, now); len(got) != 2 {
		t.Errorf("twins: %+v", got)
	}
}

// Finished items leave a day after their tick, the oldest first past keptMax; none left removes the file.
func TestFinishedExpiryAndCap(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	var items []Item
	for i := range keptMax + 10 {
		items = append(items, Item{ID: i + 1, Text: "x", Done: t0.Unix() + int64(i)})
	}
	if err := s.keepDone(items, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, done := stored(t, s)
	if len(done) != keptMax || done[0] != keptMax+10 || done[keptMax-1] != 11 {
		t.Errorf("finished %v: want the newest %d, newest first", done, keptMax)
	}
	l, err := s.Read(t0.Add(keptFor + 30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Finished) != 29 || l.Finished[len(l.Finished)-1].ID != 32 {
		t.Errorf("a day on: %d finished, want 29 (ticked 31 s or less after t0, ids 32-60)", len(l.Finished))
	}
	if err := s.keepDone(nil, t0.Add(2*keptFor)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.donePath()); !os.IsNotExist(err) {
		t.Errorf("all expired: file stays (%v)", err)
	}
}

// Set and Clear keep the ticked items they drop with the rest, not the open ones.
func TestFinishedSetClear(t *testing.T) {
	for _, name := range []string{"Set", "Clear"} {
		t.Run(name, func(t *testing.T) {
			s := store(t)
			t0 := time.Unix(1_800_000_000, 0)
			for _, text := range []string{"ticked", "open"} {
				if _, err := s.Add(Item{Text: text}, t0); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Done([]int{1}, t0); err != nil {
				t.Fatal(err)
			}
			var err error
			if name == "Set" {
				err = s.Set([]string{"new"}, t0.Add(time.Second))
			} else {
				err = s.Clear(t0.Add(time.Second))
			}
			if err != nil {
				t.Fatal(err)
			}
			l, err := s.Read(t0.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if len(l.Finished) != 1 || l.Finished[0].Text != "ticked" {
				t.Errorf("finished %+v: want the ticked one only", l.Finished)
			}
			// Numbers restart: a new #1 ticked is kept beside the old one
			if _, err := s.Add(Item{Text: "new"}, t0.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := s.Done([]int{1}, t0.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r"}, t0.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			_, done, texts := storedTexts(t, s)
			if !slices.Equal(done, []int{1, 1}) || !slices.Equal(texts, []string{"new", "ticked"}) {
				t.Errorf("after a restart: finished %v %q, want both #1s, newest first", done, texts)
			}
		})
	}
}

// A list moved to a new key takes its finished items along.
func TestTakeMovesFinished(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	from, _ := Desktop("local_a")
	to, _ := Desktop("local_b")
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := from.Add(Item{Text: "a"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := from.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "b"}, t0.Add(doneShown)); err != nil {
		t.Fatal(err)
	}
	if taken, err := to.take(from); err != nil || !taken {
		t.Fatalf("take: %v %v", taken, err)
	}
	if _, err := os.Stat(from.donePath()); !os.IsNotExist(err) {
		t.Errorf("finished file left behind: %v", err)
	}
	if _, done := stored(t, to); !slices.Equal(done, []int{1}) {
		t.Errorf("moved list's finished = %v, want [1]", done)
	}
}

// A finished item's assignee is as stored: the user's own, its worker's, else its list's boss (a person's); a quiet
// one stays quiet.
func TestFinishedAssignee(t *testing.T) {
	items := []Item{{ID: 1, Quiet: true}, {ID: 2, On: "boss-a"}, {ID: 3, On: "w", Session: &Session{ID: "w", Name: "w"}}, {ID: 4, On: "Carl"}}
	var got []string
	for _, f := range shownFinished(items, "boss-a") {
		got = append(got, fmt.Sprintf("%d %s %s", f.ID, f.Assignee.Kind, f.Assignee.Name))
	}
	if want := []string{"1 you ", "2 you ", "3 worker w", "4 boss boss-a"}; !slices.Equal(got, want) {
		t.Errorf("assignees = %q, want %q", got, want)
	}
	if f := shownFinished(items[:1], ""); !f[0].Quiet {
		t.Error("a quiet item's finished entry isn't quiet")
	}
	if f := shownFinished(items[3:], ""); f[0].Assignee.Name != "boss" {
		t.Errorf("a desktop app boss's: %+v", f[0].Assignee)
	}
}

// A list with only finished items, live or closed, comes from All only with finished; Closed and ClosedStore never
// take it.
func TestAllFinishedOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sessions := []string{"boss-live", "boss-shut"}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			var out string
			for _, n := range sessions {
				out += tmux.FormatRow(n, "1")
			}
			return out, nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	t0 := time.Unix(1_800_000_000, 0)
	for _, n := range sessions {
		s, err := ForPane(c, tmux.SessionTarget(n))
		if err != nil {
			t.Fatal(err)
		}
		// An older dev's --on <own name> is the user's; a person's is its boss's
		for _, on := range []string{n, "Carl"} {
			if _, err := s.Add(Item{Text: n + "'s", On: on}, t0); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Done([]int{1, 2}, t0); err != nil {
			t.Fatal(err)
		}
	}
	sessions = []string{"boss-live"}
	// Ticked under doneShown ago: a closed list comes with them as items, not checked as boss closed
	soon, err := All(c, t0.Add(time.Second), nil, true, true)
	if err != nil || len(soon) != 2 || len(soon[1].Items) != 2 || !soon[1].Items[0].Done || soon[1].Items[0].Check != nil {
		t.Errorf("ticked just now, with finished: %+v, %v", soon, err)
	}
	if soon, err := All(c, t0.Add(time.Second), nil, true, false); err != nil || len(soon) != 1 {
		t.Errorf("ticked just now, without finished: %d lists, %v", len(soon), err)
	}
	now := t0.Add(time.Minute)
	names := func(finished bool) []string {
		all, err := All(c, now, nil, true, finished)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range all {
			out = append(out, fmt.Sprintf("%s %v items %d finished %d", l.Boss, l.Closed, len(l.Items), len(l.Finished)))
		}
		return out
	}
	if got := names(false); len(got) != 0 {
		t.Errorf("without finished: %q", got)
	}
	want := []string{"boss-live false items 0 finished 2", "boss-shut true items 0 finished 2"}
	if got := names(true); !slices.Equal(got, want) {
		t.Errorf("with finished = %q, want %q", got, want)
	}
	all, err := All(c, now, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var by []string
	for _, f := range all[1].Finished {
		by = append(by, f.On+" "+f.Assignee.Kind+" "+f.Assignee.Name)
	}
	if want := []string{"boss-shut you ", "Carl boss boss-shut"}; !slices.Equal(by, want) {
		t.Errorf("closed list's assignees = %q, want %q", by, want)
	}
	if lists, err := Closed(c, now); err != nil || len(lists) != 0 {
		t.Errorf("Closed = %d lists, %v", len(lists), err)
	}
	if _, ok, err := ClosedStore(c, "boss-shut", now); err != nil || ok {
		t.Errorf("ClosedStore = %v, %v: want not found", ok, err)
	}
}

// A finished file that isn't JSON reads as none and stops no write: the next one moves it aside, kept, and starts
// afresh.
func TestFinishedBadFile(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if err := os.MkdirAll(filepath.Dir(s.donePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.donePath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "a"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	if l, err := s.Read(t0.Add(time.Minute)); err != nil || len(l.Finished) != 1 {
		t.Errorf("Read: %+v, %v", l.Finished, err)
	}
	if _, err := s.Add(Item{Text: "b"}, t0.Add(time.Minute)); err != nil {
		t.Fatalf("a write with a bad finished file: %v", err)
	}
	bad, _ := filepath.Glob(s.donePath() + ".*.bad")
	if len(bad) != 1 {
		t.Fatalf("bad file not kept aside: %q", bad)
	}
	if data, err := os.ReadFile(bad[0]); err != nil || string(data) != "{" {
		t.Errorf("bad file kept as %q, %v", data, err)
	}
	if _, _, texts := storedTexts(t, s); !slices.Equal(texts, []string{"a"}) {
		t.Errorf("finished = %q, want [a]", texts)
	}
}
