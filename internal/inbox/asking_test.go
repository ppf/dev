package inbox

import (
	"slices"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// The items the manager's dialog has up hold AskingFor; another dialog's replace them; its next pick and a new
// conversation clear them.
func TestAsking(t *testing.T) {
	b := testBox(t)
	t0 := time.Unix(1700000000, 0)
	set := func(ids ...int) {
		t.Helper()
		var as []Asking
		for _, id := range ids {
			as = append(as, Asking{Boss: "boss-a", ID: id, Text: "Q?", At: t0.Unix()})
		}
		if err := b.SetAsking(as); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) []int {
		t.Helper()
		as, err := b.Asking(t0.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		var ids []int
		for _, a := range as {
			ids = append(ids, a.ID)
		}
		return ids
	}
	set(1)
	if ids := at(29 * time.Minute); !slices.Equal(ids, []int{1}) {
		t.Errorf("29m on: %v, want #1", ids)
	}
	if ids := at(AskingFor); len(ids) != 0 {
		t.Errorf("%v on: %v, want none", AskingFor, ids)
	}
	// A dialog of several marks each of its items, in place of the earlier ones
	set(2, 4, 5)
	if ids := at(0); !slices.Equal(ids, []int{2, 4, 5}) {
		t.Errorf("another asking of three: %v, want #2 #4 #5", ids)
	}
	if err := b.Picked([]agent.Pick{{Question: "a #2: Q?", Answer: "A"}, {Question: "a #4: Q?", Answer: "B"}}, t0); err != nil {
		t.Fatal(err)
	}
	if ids := at(0); len(ids) != 0 {
		t.Errorf("after the pick: %v, want none", ids)
	}
	set(3)
	if err := b.ForgetPrompt(); err != nil {
		t.Fatal(err)
	}
	if ids := at(0); len(ids) != 0 {
		t.Errorf("after a new conversation: %v, want none", ids)
	}
}
