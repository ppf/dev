package inbox

import (
	"fmt"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// A hand-on question's pick is dropped or corrected unless "Hand it on"; any other question's pick is none.
func TestHandOnOutcome(t *testing.T) {
	gate := []string{HandItOn, DropIt}
	for _, tt := range []struct {
		picks []agent.Pick
		want  string
	}{
		{[]agent.Pick{{Question: "Hand on?", Answer: HandItOn, Labels: gate}}, ""},
		// Typed under Other, the label's words are a correction too
		{[]agent.Pick{{Question: "Hand on?", Answer: "hand it on", Labels: []string{"Hand it on (Recommended)", DropIt}}}, Corrected},
		{[]agent.Pick{{Question: "Hand on?", Answer: DropIt, Labels: gate}}, Dropped},
		{[]agent.Pick{{Question: "Hand on?", Answer: "Drop it (Recommended)", Labels: []string{HandItOn, "Drop it (Recommended)"}}}, Dropped},
		{[]agent.Pick{{Question: "Hand on?", Answer: "Hand it on (Recommended)", Labels: []string{"Hand it on (Recommended)", DropIt}}}, ""},
		{[]agent.Pick{{Question: "Hand on?", Answer: "only the release builds", Labels: gate}}, Corrected},
		{[]agent.Pick{{Question: "dev #7: Merge?", Answer: "Wait", Labels: []string{"Merge", "Wait"}}}, ""},
		{[]agent.Pick{{Question: "Hand on?", Answer: DropIt, Labels: gate}, {Question: "B?", Answer: "y"}}, ""},
	} {
		if got := handOnOutcome(tt.picks); got != tt.want {
			t.Errorf("%+v: %q, want %q", tt.picks, got, tt.want)
		}
	}
}

// List puts ungated kinds first, then work, then hand-on questions that sent nothing, newest first in each; Tally
// counts them.
func TestHandoffsList(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := OpenHandoffs("/tmp/tmux-501/default")
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range []string{Dropped, KindWork, KindQuestion, KindOwn, KindWork, Corrected} {
		if err := s.Add(Handoff{Kind: k, At: int64(100 + i), Line: k}); err != nil {
			t.Fatal(err)
		}
	}
	hs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, h := range hs {
		got = append(got, h.At)
	}
	if want := []int64{103, 102, 104, 101, 105, 100}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v", got, want)
	}
	if got, want := Tally(hs), "gated 2 · ungated: question 1, pick 0, own 1 · dropped 1 · corrected 1"; got != want {
		t.Errorf("Tally = %q, want %q", got, want)
	}
	if got := FirstLine("\n  Cache   the builds.\ncontract first"); got != "Cache the builds." {
		t.Errorf("FirstLine = %q", got)
	}
}

// A pick off the question's options is typed under Other; without options in the payload it never reads as one.
func TestPickedTyped(t *testing.T) {
	b := testBox(t)
	at := time.Unix(1700000000, 0)
	for _, tt := range []struct {
		labels []string
		answer string
		want   bool
	}{
		{[]string{HandItOn, DropIt}, "only the release builds", true},
		{[]string{" Hand it on ", DropIt}, HandItOn, false},
		{nil, "only the release builds", false},
	} {
		if err := b.Picked([]agent.Pick{{Question: "Hand on?", Answer: tt.answer, Labels: tt.labels}}, at); err != nil {
			t.Fatal(err)
		}
		if p, err := b.UserPrompt(); err != nil || p == nil || p.Typed != tt.want {
			t.Errorf("%q of %q: %+v, %v; want typed %v", tt.answer, tt.labels, p, err, tt.want)
		}
	}
}
