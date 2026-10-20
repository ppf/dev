package screen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
)

func golden(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), `\x1b`, "\x1b")
}

// Claude Code 2.1.302's screens (tmux capture-pane -p -e, ESC written as \x1b), captured on a throwaway tmux
// server: after a dialog answered No or Esc, while working, and with its dialogs open (feedback typed too).
func TestClaudeStateGolden(t *testing.T) {
	tests := []struct{ file, want string }{
		{"claude-after-no", agent.Idle},
		{"claude-after-esc", agent.Idle},
		{"claude-ask-after-esc", agent.Idle},
		{"claude-working", agent.Working},
		{"claude-permission-open", ""},
		{"claude-ask-open", ""},
		{"claude-plan-open", ""},
		{"claude-plan-feedback", ""},
		{"claude-plan-after-esc", agent.Idle},
		{"claude-permission-amend", ""},
		{"claude-permission-no-feedback", ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			if got := ClaudeState(golden(t, "testdata/"+tt.file+".txt")); got != tt.want {
				t.Errorf("ClaudeState = %q, want %q", got, tt.want)
			}
		})
	}
}

// No dialog (Claude's questions, permissions and trust prompt, Codex's and Grok's menus) reads
// as Claude's input box, as a prompt would answer it; nor do Codex's and Grok's other screens.
func TestClaudeStateDialogs(t *testing.T) {
	files, err := filepath.Glob("../herd/testdata/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("no goldens: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			if got := ClaudeState(golden(t, f)); got != "" {
				t.Errorf("ClaudeState = %q, want none", got)
			}
		})
	}
}

func TestClaudeState(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := func(above string, draft ...string) string {
		return strings.Join(append(append([]string{"⏺ Done.", "", above, "", rule}, draft...), rule, "  ⏸ manual mode on · ? for shortcuts", "", ""), "\n")
	}
	tests := []struct{ name, capture, want string }{
		{"empty box", box("✻ Worked for 2s", "❯ "), agent.Idle},
		{"typed draft", box("✻ Worked for 2s", "❯ rebase onto main", "  and push"), agent.Idle},
		{"spinner above the box", box("✻ Thinking… (esc to interrupt)", "❯ "), agent.Working},
		{"permission options in the box", box("", "❯ 1. Yes", "  2. No"), ""},
		{"dialog text in the box", box("", "❯ Option A", "", "Do you want to use option A?"), ""},
		{"short dashes aren't the box", "text\n──\n❯ x\n──\nstatus", ""},
		{"no box", "⏺ Done.\n", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeState(tt.capture); got != tt.want {
				t.Errorf("ClaudeState = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnapshot(t *testing.T) {
	rule := strings.Repeat("─", 60)
	claude := func(spin, tool, draft, status string, out ...string) string {
		lines := append([]string{"⏺ Running the tests.", ""}, out...)
		return strings.Join(append(lines, "⏺ Bash(go test ./...)", "  ⎿  "+tool, "", spin, "", rule, "❯ "+draft, rule, "  "+status, ""), "\n")
	}
	base := claude("✻ Brewing… (5m 6s · ↓ 2.8k tokens · esc to interrupt)", "Running… (3m 2s)", "", "~/repo · 12:41 · $1.24")
	tests := []struct {
		name, capture string
		same          bool
	}{
		{"timers, spinner and status line tick",
			claude("✶ Percolating… (6m 40s · ↑ 4.1k tokens · esc to interrupt)", "Running… (4m 36s)", "", "~/repo · 12:43 · $1.31"), true},
		{"a custom verb and a new tip",
			claude("✻ Thinking it over… (6m 40s · esc to interrupt)\n  ⎿  Tip: Run /init", "Running… (4m 36s)", "", "~/repo · 12:43 · $1.31"), true},
		{"a draft typed in the box", claude("✻ Brewing… (5m 6s · ↓ 2.8k tokens · esc to interrupt)", "Running… (3m 2s)", "check CI", "~/repo · 12:41 · $1.24"), true},
		{"new output", claude("✻ Brewing… (5m 6s · ↓ 2.8k tokens · esc to interrupt)", "Running… (3m 2s)", "", "~/repo · 12:41 · $1.24", "⏺ Tests pass.", ""), false},
		{"tool output changed", claude("✻ Brewing… (5m 6s · ↓ 2.8k tokens · esc to interrupt)", "ok  ./internal (3m 2s)", "", "~/repo · 12:41 · $1.24"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Snapshot(tt.capture) == Snapshot(base); got != tt.same {
				t.Errorf("same = %v, want %v:\n%s\nvs\n%s", got, tt.same, Snapshot(tt.capture), Snapshot(base))
			}
		})
	}
	if Snapshot("main.go:12:34: undefined: x\n") == Snapshot("main.go:40:21: undefined: x\n") {
		t.Error("a file position taken for a clock")
	}
	if Snapshot("done 6:58 PM\n") != Snapshot("done 7:00 PM\n") {
		t.Error("clock counted")
	}
	if Snapshot("⎿  Running… (59s)\n") != Snapshot("⎿  Running… (1m 0s)\n") {
		t.Error("a timer gaining a unit counted")
	}
	grok := golden(t, "../herd/testdata/grok-working.txt")
	if Snapshot(grok) != Snapshot(strings.NewReplacer("⠧", "⠦", "⇣25.6k", "⇣31.2k").Replace(grok)) {
		t.Error("Grok's spinner frame counted")
	}
	// Without Claude's box (Codex, Grok) the timers still don't count
	if Snapshot("• Working (5m 03s • esc to interrupt)\n› ") != Snapshot("• Working (12m 40s • esc to interrupt)\n› ") {
		t.Error("Codex timer counted")
	}
	working := golden(t, "testdata/claude-working.txt")
	if Snapshot(working) != Snapshot(strings.ReplaceAll(working, "done 7:00 PM", "done 7:42 PM")) {
		t.Error("golden: clock counted")
	}
	if Snapshot(working) == Snapshot(strings.Replace(working, "keeper's", "keeper's cottage", 1)) {
		t.Error("golden: new text not counted")
	}
}
