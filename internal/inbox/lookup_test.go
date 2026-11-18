package inbox

import (
	"strconv"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// A watch looks up at most listLookups of the boss's linked items, then the rest, then none within todo.LookupTTL.
func TestLookupList(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "display-message" {
			return tmux.FormatRow("/tmp/tmux-1/default", "boss-orbit", "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	s, err := todo.ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	for _, n := range []string{"1", "2", "3"} {
		if _, err := s.Add(todo.Item{Text: "PR " + n, Link: "https://github.com/acme/orbit/pull/" + n}, now); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	old := viewPR
	viewPR = func(string) (sidebar.PRView, error) { calls++; return sidebar.PRView{State: "OPEN"}, nil }
	t.Cleanup(func() { viewPR = old })
	for i, want := range []int{2, 3, 3} {
		if err := lookupList(c, "%1", now); err != nil {
			t.Fatal(err)
		}
		if calls != want {
			t.Errorf("watch %d: %d lookups, want %d", i+1, calls, want)
		}
	}
}

// A watch queues one to do note per item ticked by its PR's move and per reason to re-check one, each once, with the
// batch's hint; an --until tick waits for the reminder. To do notes are dropped before others past maxNotes.
func TestNudgeList(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "display-message" {
			return tmux.FormatRow("/tmp/tmux-1/default", "boss-orbit", "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	s, err := todo.ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open("/tmp/tmux-1/default", "$1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	url := "https://github.com/acme/orbit/pull/415"
	for _, it := range []todo.Item{
		{Text: "Second review of #415", On: "Ana", Link: url, DoneOn: todo.OnApproved},
		{Text: "Rework rounding", On: "fix", Session: &todo.Session{ID: "fix", Name: "fix"}},
		{Text: "Sync", Until: now.Add(time.Minute).Unix()},
	} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Lookup(now, todo.LookupTTL, 0, func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN", Review: "APPROVED"}, nil }); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	for i, want := range []bool{true, false} {
		queued, err := nudgeList(c, Boss{Name: "boss-orbit", Box: b}, "%1", map[string]bool{}, later)
		if err != nil || queued != want {
			t.Fatalf("watch %d: queued %v (%v), want %v", i+1, queued, err, want)
		}
	}
	bt, err := b.Take()
	if err != nil {
		t.Fatal(err)
	}
	want := "[dev boss inbox] 2 worker update(s):\n" +
		`- to do #1 "Second review of #415" ticked by itself: PR #415 approved (--done-on approved)` + "\n" +
		`- to do #2 "Rework rounding" (on fix): its session is gone` + "\n" + toDoHint
	if got := bt.String(); got != want {
		t.Errorf("batch =\n%s\nwant\n%s", got, want)
	}
	if ticked, _ := s.TakeTicked(later, nil, nil); len(ticked) != 1 || ticked[0].PR != 0 {
		t.Errorf("left for the reminder: %+v, want the --until tick", ticked)
	}
	for i := range maxNotes {
		kind := Finished
		if i < 2 {
			kind = ToDo
		}
		if _, err := b.Add(Note{Worker: "w" + strconv.Itoa(i), Kind: kind, Text: "n", Key: strconv.Itoa(i)}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Add(Note{Worker: "w", Kind: Finished, Text: "last", Key: "last"}, now); err != nil {
		t.Fatal(err)
	}
	bt, _ = b.Take()
	if bt.Notes[0].Kind != ToDo || bt.Notes[0].Worker != "w1" {
		t.Errorf("past maxNotes the first to do note went first: kept %+v", bt.Notes[0])
	}
}
