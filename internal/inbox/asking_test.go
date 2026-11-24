package inbox

import (
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// The question the manager has up holds AskingFor; another replaces it; its next pick and a new conversation clear it.
func TestAsking(t *testing.T) {
	b := testBox(t)
	t0 := time.Unix(1700000000, 0)
	set := func(id int) {
		t.Helper()
		if err := b.SetAsking(Asking{Boss: "boss-a", ID: id, Text: "Q?", At: t0.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) *Asking {
		t.Helper()
		a, err := b.Asking(t0.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	set(1)
	if a := at(29 * time.Minute); a == nil || a.ID != 1 {
		t.Errorf("29m on: %+v, want #1", a)
	}
	if a := at(AskingFor); a != nil {
		t.Errorf("%v on: %+v, want none", AskingFor, a)
	}
	set(2)
	if a := at(0); a == nil || a.ID != 2 {
		t.Errorf("another asking: %+v, want #2", a)
	}
	if err := b.Picked([]agent.Pick{{Question: "a #2: Q?", Answer: "A"}}, t0); err != nil {
		t.Fatal(err)
	}
	if a := at(0); a != nil {
		t.Errorf("after the pick: %+v, want none", a)
	}
	set(3)
	if err := b.ForgetPrompt(); err != nil {
		t.Fatal(err)
	}
	if a := at(0); a != nil {
		t.Errorf("after a new conversation: %+v, want none", a)
	}
}
