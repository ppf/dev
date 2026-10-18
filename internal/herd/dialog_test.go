package herd

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), `\x1b`, "\x1b")
}

// The claude-* goldens are Claude Code 2.1.302's screens, captured on a throwaway tmux server.
func TestParseDialogGolden(t *testing.T) {
	tests := []struct {
		file string
		want dialog
	}{
		{"claude-ask-q1", dialog{title: "Which color should the theme use?", tabs: []string{"Color", "Extras", "Submit"}, cursor: 0, opts: []option{
			{num: 1, label: "Red", desc: "Warm and loud"}, {num: 2, label: "Green", desc: "Calm"}, {num: 3, label: "Blue", desc: "Cool and safe"},
			{num: 4, label: "Type something."}, {num: 5, label: "Chat about this"}}}},
		{"claude-ask-q2-checked", dialog{title: "Which extras do you want?", tabs: []string{"Color", "Extras", "Submit"}, cursor: 0, multi: true, submitAt: 4, opts: []option{
			{num: 1, label: "Tests", desc: "Add unit tests", checkbox: true, checked: true}, {num: 2, label: "Docs", desc: "Write README", checkbox: true},
			{num: 3, label: "Lint", desc: "Run linter", checkbox: true, checked: true}, {num: 4, label: "Type something", checkbox: true}, {num: 5, label: "Chat about this"}}}},
		{"claude-ask-multi-other-typed", dialog{title: "Which pets?", tabs: []string{"Pets", "Submit"}, cursor: 2, multi: true, submitAt: 3, opts: []option{
			{num: 1, label: "Cat", desc: "Independent", checkbox: true}, {num: 2, label: "Dog", desc: "Loyal", checkbox: true, checked: true},
			{num: 3, label: "a fish", checkbox: true, checked: true}, {num: 4, label: "Chat about this"}}}},
		{"claude-ask-single", dialog{title: "Which size?", tabs: []string{"Size"}, cursor: 0, opts: []option{
			{num: 1, label: "Small", desc: "Fast"}, {num: 2, label: "Large", desc: "Thorough"}, {num: 3, label: "Type something."}, {num: 4, label: "Chat about this"}}}},
		{"claude-ask-review", dialog{title: "Ready to submit your answers?", what: "←☒Color☒Extras✔Submit→Reviewyouranswers●Whichcolorshouldthethemeuse?→Green●Whichextrasdoyouwant?→Tests,Lint", cursor: 0, opts: []option{{num: 1, label: "Submit answers"}, {num: 2, label: "Cancel"}}}},
		{"claude-permission-write", dialog{title: "Do you want to create notes.txt?", what: "Createfilenotes.txt1hithere", cursor: 0, opts: []option{{num: 1, label: "Yes"},
			{num: 2, label: "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)"}, {num: 3, label: "No"}}}},
		{"codex-trust", dialog{what: "Folderaccess/private/tmp/claude-503/-Users-storm-PhpstormProjects-ppf-dev--claude-worktrees-tile-crop-codex-grok/bff0037d-9894-44df-8196-ded6b8137fb9/scratchpad/play", title: "Trust this folder? Codex can read, edit, and run files here, subject to your permission settings. Folder settings can run code automatically, even without a model request. Continue only if you trust these files. Your trust decision will be saved.",
			cursor: 0, opts: []option{{num: 1, label: "Trust and continue"}, {num: 2, label: "Back to Agent Command Center"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, ok := parseDialog(golden(t, tt.file))
			if !ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDialog = %v %+v\nwant %+v", ok, got, tt.want)
			}
		})
	}
}

func TestParseDialogBash(t *testing.T) {
	d, ok := parseDialog(golden(t, "claude-permission-bash"))
	if !ok || d.title != "Do you want to proceed?" || len(d.opts) != 4 || d.opts[3].label != "No" || d.cursor != 0 {
		t.Fatalf("parseDialog = %v %+v", ok, d)
	}
	// The wrapped rest of a long label is its desc
	if o := d.opts[1]; o.label != "Yes, and don't ask again for mkdir -p build/out and touch build/out/x.txt commands in" || !strings.HasPrefix(o.desc, "/private/tmp/") {
		t.Errorf("option 2 = %+v", o)
	}
}

func TestParseDialogNone(t *testing.T) {
	for _, capture := range []string{
		golden(t, "claude-trust"), // unnumbered
		golden(t, "codex-idle"),
		"1. a numbered list\n2. in the conversation\nthen\nmore\nlines\nof\ntext\nunder it\n",
		"  1. one\n  3. skips two\n",
		"",
	} {
		if d, ok := parseDialog(capture); ok {
			t.Errorf("parseDialog found %+v in:\n%s", d, capture)
		}
	}
}

func TestParseDialogGrokBox(t *testing.T) {
	capture := "╭──────────────────────╮\n│ Allow bash: ls?      │\n│                      │\n│ ❯ 1. Yes             │\n│   2. No              │\n╰──────────────────────╯\n"
	d, ok := parseDialog(capture)
	if !ok || d.title != "Allow bash: ls?" || len(d.opts) != 2 || d.opts[1].label != "No" || d.cursor != 0 {
		t.Errorf("parseDialog = %v %+v", ok, d)
	}
}
