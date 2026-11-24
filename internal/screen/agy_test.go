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
			if got := AgyInterrupted(capture); got != tt.interrupted {
				t.Errorf("AgyInterrupted = %v, want %v", got, tt.interrupted)
			}
			if tt.trusts != "" && !AgyTrust(capture, tt.trusts) {
				t.Errorf("AgyTrust(%s) = false, want true", tt.trusts)
			}
			if AgyTrust(capture, "/elsewhere") {
				t.Error("AgyTrust(/elsewhere) = true, want false")
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
		name, capture string
		want          bool
	}{
		{"its folder", dialog("> Yes, I trust this folder", dir), true},
		{"its folder wrapped", dialog("> Yes, I trust this folder", dir[:20], dir[20:]), true},
		{"another folder", dialog("> Yes, I trust this folder", "/Users/me/src/app"), false},
		{"a folder under it", dialog("> Yes, I trust this folder", dir+"/sub"), false},
		{"cursor on No", dialog("  Yes, I trust this folder", dir), false},
		{"no dialog", strings.Repeat("─", 60) + "\n>\n" + strings.Repeat("─", 60) + "\n? for shortcuts\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AgyTrust(tt.capture, dir); got != tt.want {
				t.Errorf("AgyTrust = %v, want %v", got, tt.want)
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
