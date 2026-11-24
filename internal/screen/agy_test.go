package screen

import (
	"strconv"
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
		{"agy-permission-narrow", false, false, ""}, // in a 58-column pane, its tool line far above
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
			if got, want := AgyBoxBottom(capture), tt.idle || tt.file == "agy-working"; got != want {
				t.Errorf("AgyBoxBottom = %v, want %v", got, want)
			}
			want := ""
			if tt.file == "agy-permission" || tt.file == "agy-permission-narrow" {
				want = "Bash|echo hi|Run this command?|true"
			}
			if tool, what, title, ok := AgyPermission(capture); ok || want != "" {
				if got := strings.Join([]string{tool, what, title, strconv.FormatBool(ok)}, "|"); got != want {
					t.Errorf("AgyPermission = %s, want %s", got, want)
				}
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

// A draft holding a rule of its own: the box is still the rules around its > row
func TestAgyBoxDraftRule(t *testing.T) {
	rule := strings.Repeat("─", 40)
	capture := strings.Join([]string{"  ok", rule, "> notes", "  " + strings.Repeat("─", 12), "  more", rule, "? for shortcuts", ""}, "\n")
	if top, bottom, ok := AgyBoxAt(plainScreen(capture)); !ok || top != 1 || bottom != 5 {
		t.Errorf("AgyBoxAt = %d, %d, %v; want 1, 5", top, bottom, ok)
	}
	if !AgyIdle(capture) {
		t.Error("AgyIdle = false")
	}
	// A draft past the box's usual height, while agy works: no dialog
	long := []string{rule, "> " + strings.Repeat("word ", 10)}
	for range 30 {
		long = append(long, "  "+strings.Repeat("word ", 10))
	}
	if !AgyBoxBottom(strings.Join(append(long, rule, "esc to cancel     auto · Gemini 3.8 Flash · low"), "\n")) {
		t.Error("AgyBoxBottom(a long draft) = false")
	}
	if AgyBoxBottom("$ \n") {
		t.Error("AgyBoxBottom(a shell) = true")
	}
}

func TestAgyPermission(t *testing.T) {
	rule := strings.Repeat("─", 40)
	prompt := func(asked ...string) []string {
		return append(append([]string{"● Bash(go test ./...) (ctrl+o to expand)", "", "Command", rule, "", "Requesting permission for:"}, asked...),
			"", "Run this command?", "> 1. Yes, run command", "  4. No, cancel", "", "  ↑/↓ Navigate", "esc to cancel     auto · Gemini 3.8 Flash · low")
	}
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"a command", prompt("   go test ./..."), "Bash|go test ./...|Run this command?|true"},
		{"a wrapped command", prompt("   go test ./internal/screen/ ./internal/control/", "   ./internal/agent/"),
			"Bash|go test ./internal/screen/ ./internal/control/ ./internal/agent/|Run this command?|true"},
		{"no tool line", prompt("   ls")[2:], "|ls|Run this command?|true"},
		// Its text in a reply (a worker reading these fixtures), above an input box
		{"quoted in a reply", append(prompt("   ls")[:10], rule, ">", rule, "? for shortcuts"), "|||false"},
		{"no options under it", append(prompt("   ls")[:8], "esc to cancel"), "|||false"},
		{"another footer", append(prompt("   ls")[:11], "? for shortcuts"), "|||false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, what, title, ok := AgyPermission(strings.Join(tt.lines, "\n") + "\n\n")
			if got := strings.Join([]string{tool, what, title, strconv.FormatBool(ok)}, "|"); got != tt.want {
				t.Errorf("AgyPermission = %s, want %s", got, tt.want)
			}
		})
	}
}
