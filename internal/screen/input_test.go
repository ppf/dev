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
		// Older captures: a dim hint, placeholder or suggestion is Claude Code's, never the user's
		{"claude-queued", Input{Text: "Press up to edit queued messages", Suggestion: true}},
		{"claude-notice", Input{Text: `Try "create a util logging.py that..."`, Suggestion: true}},
		{"claude-asks", Input{Text: "yes", Suggestion: true}},
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
		// A long suggestion wraps onto the row under its prompt row: still Claude Code's, never typed
		// Dim text only under a blank prompt row is read as the user's: a pasted text's chip may be drawn dim
		"dim under a blank row": {rule + "❯ \n  \x1b[2m[Pasted text #1 +12 lines]\x1b[0m\n" + rule, Input{Text: "[Pasted text #1 +12 lines]"}},
		"pasted text chip":      {rule + "❯ \x1b[2m[Pasted text #1 +12 lines]\x1b[0m\n" + rule, Input{Text: "[Pasted text #1 +12 lines]"}},
		"wrapped suggestion":    {rule + "❯ \x1b[2mrun the full suite\x1b[0m\n  \x1b[2mand report\x1b[0m\n" + rule, Input{Text: "run the full suite\n  and report", Suggestion: true}},
	} {
		if got, ok := InputBox(tt.screen); !ok || got != tt.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, tt.want)
		}
	}
	// A dialog drawn between two rules: its numbered option is no draft
	if got, ok := InputBox(rule + "❯ 1. Yes\n  2. No\n" + rule); ok {
		t.Errorf("a numbered dialog option between rules: %+v, want no box", got)
	}
	// A draft that starts like a list item is still the user's
	if got, ok := InputBox(rule + "❯ 1. fix the login\n" + rule); !ok || got.Text != "1. fix the login" {
		t.Errorf("a numbered draft: %+v %v, want it read", got, ok)
	}
	if _, ok := InputBox("Do you want to proceed?\n❯ 1. Yes\n  2. No\n"); ok {
		t.Error("a dialog: want no box")
	}
}
