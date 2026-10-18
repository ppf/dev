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
