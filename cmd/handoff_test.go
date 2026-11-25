package cmd

import (
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

// --kind work passes only on a pick of "Hand it on" for one question naming the boss and the brief's first line.
func TestHandOnPick(t *testing.T) {
	pick := func(q, a string) *inbox.Prompt { return &inbox.Prompt{Pick: true, Question: q, Answer: a} }
	brief := "Cache the CI builds.\ncontract first"
	asked := "Hand on to boss-dev? Sound: faster CI.\n\nCache  the CI builds."
	for _, tt := range []struct {
		name string
		pick *inbox.Prompt
		want string
	}{
		{"its pick", pick(asked, "Hand it on"), ""},
		{"its pick, recommended", pick(asked, "Hand it on (Recommended)"), ""},
		{"Drop it, recommended", pick(asked, "Drop it (Recommended)"), `picked "Drop it": not handed on`},
		{"typed message", &inbox.Prompt{Text: "hand it on"}, "a typed message, not a pick"},
		{"several questions", pick("", ""), "wasn't one question: a hand-on question stays alone in its dialog"},
		{"beside an item's question", &inbox.Prompt{Pick: true, HandOnMixed: true, Asks: []inbox.Asked{{Question: asked, Answer: "Hand it on"}, {Question: "dev #7: Merge #148?", Answer: "Merge"}}},
			"a hand-on question stays alone in its dialog"},
		{"Drop it", pick(asked, "Drop it"), `picked "Drop it": not handed on`},
		{"correction under Other", pick(asked, "only the release builds"), `answered "only the release builds"`},
		{"the label typed under Other", &inbox.Prompt{Pick: true, Question: asked, Answer: "hand it on", Typed: true}, "typed \"hand it on\" under Other"},
		{"boss named only in the assessment", pick("Hand on to boss-api?\nSound, unlike boss-dev.\n\nCache the CI builds.", "Hand it on"), "not boss-dev"},
		{"brief inside a longer line", pick("Hand on to boss-dev?\n\nDo not cache the CI builds.", "Hand it on"), "the user's pick was for"},
		{"another brief", pick("Hand on to boss-dev?\n\nRename the repo.", "Hand it on"), "the user's pick was for"},
		{"another boss", pick("Hand on to boss-api?\n\nCache the CI builds.", "Hand it on"), "not boss-dev"},
		{"a second boss after the first", pick("Hand on to boss-api? This overlaps with boss-dev.\n\nCache the CI builds.", "Hand it on"), "not boss-dev"},
		{"a boss whose name holds this one", pick("Hand on to boss-devops?\n\nCache the CI builds.", "Hand it on"), "not boss-dev"},
		{"an item's pick", pick("dev #7: Merge #148?", "Merge"), "answered \"Merge\""},
	} {
		err := handOnPick(tt.pick, "boss-dev", brief)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	if err := handOnPick(pick(asked, "Hand it on"), "boss-dev", "\n  \n"); err == nil || !strings.Contains(err.Error(), "brief is empty") {
		t.Errorf("empty brief: %v", err)
	}
}

// --go and --command: only with --kind work, never both, a command only in a brief carrying its words; refused before tmux.
func TestGateHandOnFlags(t *testing.T) {
	for _, tt := range []struct{ kind, words, command, want string }{
		{"question", "", "zrób PR", "--command goes with --kind work"},
		{"", "", "zrób PR", "--command goes with --kind work"},
		{"question", "Tak", "", "--go goes with --kind work"},
		{"work", "Tak", "zrób PR", "--go or --command, not both"},
		{"work", "", "zrób PR z cache", `--command: the brief doesn't carry the user's words word for word: ask with the hand-on question ("Hand it on" / "Drop it")`},
	} {
		if _, err := gateHandOn(nil, tt.kind, tt.words, tt.command, "boss-dev", "Zrób PR"); err == nil || err.Error() != tt.want {
			t.Errorf("%+v: %v, want %q", tt, err, tt.want)
		}
	}
}
