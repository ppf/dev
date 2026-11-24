package screen

import (
	"strings"
	"testing"
)

// agy 1.5.16's screens (tmux capture-pane -p -e), from the live runs of 2026-11-24: its trust dialog in a new
// folder and in a worktree, its input box idle (empty, a placeholder, after a question), a pasted draft, working,
// after a declined permission prompt and after Esc, and a permission prompt and question open.
func TestAgyScreensGolden(t *testing.T) {
	const probe = "/private/tmp/claude-503/-Users-storm-PhpstormProjects-ppf-dev--claude-worktrees-agy-probe/6e301bb9-f540-49d3-8085-e35b5c363da1/scratchpad/agyws"
	tests := []struct {
		file              string
		idle, interrupted bool
		trusts            string // the directory its trust dialog asks for; "" when none
	}{
		{"agy-trust", false, false, probe},
		{"agy-trust-worktree", false, false, "/Users/storm/PhpstormProjects/ppf/dev/.claude/worktrees/agy-probe"},
		{"agy-idle", true, false, ""},
		{"agy-idle-placeholder", true, false, ""},
		{"agy-after-question", true, false, ""},
		{"agy-draft", true, false, ""},
		{"agy-working", false, false, ""},
		{"agy-esc", true, true, ""},
		{"agy-declined", true, true, ""},
		{"agy-permission", false, false, ""},
		{"agy-question", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			capture := golden(t, "testdata/"+tt.file+".txt")
			if got := AgyIdle(capture); got != tt.idle {
				t.Errorf("AgyIdle = %v, want %v", got, tt.idle)
			}
			if got, want := AgyBox(capture), tt.idle || tt.file == "agy-working"; got != want {
				t.Errorf("AgyBox = %v, want %v", got, want)
			}
			if got := AgyInterrupted(capture); got != tt.interrupted {
				t.Errorf("AgyInterrupted = %v, want %v", got, tt.interrupted)
			}
			if dir, yes, ok := AgyTrust(capture); dir != tt.trusts || yes != (tt.trusts != "") || ok != (tt.trusts != "") {
				t.Errorf("AgyTrust = %q, %v, %v; want %q", dir, yes, ok, tt.trusts)
			}
		})
	}
}

func TestAgyTrust(t *testing.T) {
	dialog := func(cursor string, path ...string) string {
		return strings.Join(append(append([]string{"Accessing workspace:", ""}, path...), "",
			"Do you trust the contents of this project?", "", cursor, "  No, exit", ""), "\n")
	}
	const dir = "/Users/me/src/app/.claude/worktrees/fix-login"
	tests := []struct {
		name, capture, dir string
		yes, ok            bool
	}{
		{"its folder", dialog("> Yes, I trust this folder", dir), dir, true, true},
		{"its folder wrapped", dialog("> Yes, I trust this folder", dir[:20], dir[20:]), dir, true, true},
		{"cursor on No", dialog("  Yes, I trust this folder", dir), dir, false, true},
		{"no dialog", strings.Repeat("─", 60) + "\n>\n" + strings.Repeat("─", 60) + "\n? for shortcuts\n", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, yes, ok := AgyTrust(tt.capture); got != tt.dir || yes != tt.yes || ok != tt.ok {
				t.Errorf("AgyTrust = %q, %v, %v; want %q, %v, %v", got, yes, ok, tt.dir, tt.yes, tt.ok)
			}
		})
	}
}

func TestAgyInterrupted(t *testing.T) {
	rule := strings.Repeat("─", 60)
	screen := func(above ...string) string {
		return strings.Join(append(append([]string{"● Bash(echo hi)"}, above...), rule, ">", rule, "? for shortcuts", ""), "\n")
	}
	interrupted := "  ⎿  Interrupted · What should Antigravity CLI do instead?"
	tests := []struct {
		name, capture string
		want          bool
	}{
		{"under the box", screen(interrupted, "", ""), true},
		{"a prompt sent since", screen(interrupted, rule, "> carry on"), false},
		{"no interrupt", screen("  ok"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AgyInterrupted(tt.capture); got != tt.want {
				t.Errorf("AgyInterrupted = %v, want %v", got, tt.want)
			}
		})
	}
}
