package inbox

import (
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
