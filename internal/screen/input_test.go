package screen

import "testing"

// Claude Code 2.1.331 draws its prompt suggestion and placeholder dim (SGR 2), which capture-pane -e keeps: the same
// words typed by the user are not dim (real captures).
func TestInputBox(t *testing.T) {
	for _, tt := range []struct {
		file string
		want Input
	}{
		{"claude-input-suggestion", Input{Text: "run it", Suggestion: true}},
		{"claude-input-typed", Input{Text: "run it"}},
		{"claude-input-placeholder", Input{Text: `Try "fix typecheck errors"`, Suggestion: true}},
	} {
		if got, ok := InputBox(golden(t, "testdata/"+tt.file+".txt")); !ok || got != tt.want {
			t.Errorf("%s: %+v %v, want %+v", tt.file, got, ok, tt.want)
		}
	}
	const rule = "────────────────────────────────────────\n"
	for name, tt := range map[string]struct {
		screen string
		want   Input
	}{
		"blank":            {rule + "❯\u00a0\n" + rule, Input{}},
		"multi-line draft": {rule + "❯\u00a0fix\nthe login\n" + rule, Input{Text: "fix\nthe login"}},
		"typed then dim":   {rule + "❯ ru\x1b[2mn it\x1b[0m\n" + rule, Input{Text: "ru"}},
	} {
		if got, ok := InputBox(tt.screen); !ok || got != tt.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, tt.want)
		}
	}
	if _, ok := InputBox("Do you want to proceed?\n❯ 1. Yes\n  2. No\n"); ok {
		t.Error("a dialog: want no box")
	}
}
