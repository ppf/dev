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
		{"claude-teammates", agent.Idle},
		{"claude-interrupted", agent.Idle},
		{"claude-effort-confirm", ""},
		{"claude-model-confirm", ""},
		{"claude-effort-done", agent.Idle},
		{"claude-queued", agent.Working}, // 2.1.304's spinner line has no "esc to interrupt"
		{"claude-session-named", agent.Idle},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			if got := ClaudeState(golden(t, "testdata/"+tt.file+".txt")); got != tt.want {
				t.Errorf("ClaudeState = %q, want %q", got, tt.want)
			}
		})
	}
}

// A dialog leaves out the conversation above it, where the same command shows as a tool call.
func TestClaudeDialogGolden(t *testing.T) {
	tests := []struct{ file, first, last string }{
		{"testdata/claude-permission-open.txt", "Bash command", "Esc to cancel · Tab to amend"},
		{"testdata/claude-ask-open.txt", "☐ Size", "Enter to select · ↑/↓ to navigate · Esc to cancel"},
		{"../herd/testdata/claude-ask-q1.txt", "←  ☐ Color  ☐ Extras  ✔ Submit  →", "Enter to select · Tab/Arrow keys to navigate · Esc to cancel"},
		{"testdata/claude-working.txt", "", ""},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.file), func(t *testing.T) {
			got := ClaudeDialog(golden(t, tt.file))
			lines := strings.Split(got, "\n")
			if got != "" && (lines[0] != tt.first || lines[len(lines)-1] != tt.last) || got == "" && tt.first != "" {
				t.Errorf("ClaudeDialog = %q", got)
			}
			if strings.Contains(got, "Run this exact bash command") {
				t.Errorf("ClaudeDialog has the conversation: %q", got)
			}
		})
	}
	rule := strings.Repeat("─", MinRule)
	if got := ClaudeDialog(rule + "\nWhich size?\n  1. Small\n" + rule + "\n❯ 2. Chat about this\n"); !strings.HasPrefix(got, "Which size?") {
		t.Errorf("cursor on Chat about this: ClaudeDialog = %q", got)
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

// Claude Code 2.1.304's screens: Esc ending a turn (with its teammates' panel under the box), /effort and
// /model (in a narrow pane) mid-conversation, a prompt queued while working, and teammates' panel.
func TestClaudeScreensGolden(t *testing.T) {
	tests := []struct {
		file                       string
		interrupted, cache, queued bool
	}{
		{file: "claude-interrupted", interrupted: true},
		{file: "claude-effort-confirm", cache: true},
		{file: "claude-model-confirm", cache: true},
		{file: "claude-queued", queued: true},
		{file: "claude-teammates"},
		{file: "claude-effort-done"},
		{file: "claude-working"},
		{file: "claude-after-esc", interrupted: true}, // Esc at a permission dialog ends the turn too
		{file: "claude-permission-open"},
		{file: "claude-session-named"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			capture := golden(t, "testdata/"+tt.file+".txt")
			if got := ClaudeInterrupted(capture); got != tt.interrupted {
				t.Errorf("ClaudeInterrupted = %v, want %v", got, tt.interrupted)
			}
			if got := ClaudeCacheDialog(capture); got != tt.cache {
				t.Errorf("ClaudeCacheDialog = %v, want %v", got, tt.cache)
			}
			if got := ClaudeQueued(capture); got != tt.queued {
				t.Errorf("ClaudeQueued = %v, want %v", got, tt.queued)
			}
		})
	}
	if box, ok := ClaudeBox(golden(t, "testdata/claude-teammates.txt")); !ok || box != "wait for sleep-a and sleep-b" {
		t.Errorf("ClaudeBox = %q, %v", box, ok)
	}
	if box, ok := ClaudeBox(golden(t, "testdata/claude-session-named.txt")); !ok || box != "post the PR in the channel" {
		t.Errorf("named session: ClaudeBox = %q, %v", box, ok)
	}
}

func TestClaudeInterrupted(t *testing.T) {
	rule := strings.Repeat("─", 60)
	screen := func(above ...string) string {
		return strings.Join(append(append([]string{"⏺ Writing."}, above...), rule, "❯ ", rule, "  ⏵⏵ auto mode on", ""), "\n")
	}
	interrupted := "  ⎿  Interrupted · What should Claude do instead?"
	tests := []struct {
		name, capture string
		want          bool
	}{
		{"under the box", screen(interrupted), true},
		{"a status line between", screen(interrupted, "", "◐ medium · /effort"), true},
		{"a prompt sent since", screen(interrupted, "❯ carry on", "⏺ ok"), false},
		{"working again", screen(interrupted, "✻ Thinking… (esc to interrupt)"), false},
		{"long output since", screen(interrupted, "a", "b", "c"), false},
		{"no interrupt", screen("⏺ Done."), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeInterrupted(tt.capture); got != tt.want {
				t.Errorf("ClaudeInterrupted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaudeState(t *testing.T) {
	rule := strings.Repeat("─", 60)
	box := func(above string, draft ...string) string {
		return strings.Join(append(append([]string{"⏺ Done.", "", above, "", rule}, draft...), rule, "  ⏸ manual mode on · ? for shortcuts", "", ""), "\n")
	}
	// Claude Code draws a named session's name into the box's top rule
	namedTop := strings.Repeat("─", 40) + " nightly-scan ─"
	named := func(capture string) string { return strings.Replace(capture, rule, namedTop, 1) }
	panel := "\n  ⏺ main\n  ◯ pr-review-toolkit:code-revi…  Searching Ticket.php  48s · ↓ 96.1k tokens\n  ◯ pr-review-toolkit:pr-test-a…  Searching callers  48s · ↓ 86.1k tokens\n"
	tests := []struct{ name, capture, want string }{
		{"empty box", box("✻ Worked for 2s", "❯ "), agent.Idle},
		{"typed draft", box("✻ Worked for 2s", "❯ rebase onto main", "  and push"), agent.Idle},
		{"spinner above the box", box("✻ Thinking… (esc to interrupt)", "❯ "), agent.Working},
		{"spinner without esc to interrupt", box("✳ Tempering… (thinking with low effort)", "❯ "), agent.Working},
		{"spinner with a tip under it", box("✻ Brewing… (5s)\n  ⎿  Tip: Run /init", "❯ "), agent.Working},
		{"a finished turn's line", box("✻ Cooked for 2s · done 20:10", "❯ "), agent.Idle},
		{"a reply's list item", box("  * Retrying the build…", "❯ "), agent.Idle},
		{"permission options in the box", box("", "❯ 1. Yes", "  2. No"), ""},
		{"dialog text in the box", box("", "❯ Option A", "", "Do you want to use option A?"), ""},
		{"agent panel under the status lines", box("✻ Worked for 2s", "❯ ") + "\n  ⏺ main\n  ◯ a  A  1s\n  ◯ b  B  1s\n❯ ◯ c  C  1s\n  ◯ d  D  1s\n", agent.Idle},
		{"background agents' panel, working", box("✻ Searching… (48s)", "❯ Also run the e2e after reviews") + panel, agent.Working},
		{"session name in the top rule", named(box("✻ Worked for 2s", "❯ ")), agent.Idle},
		{"session name in a narrow pane's top rule", strings.Replace(box("✻ Worked for 2s", "❯ "), rule, "── a-long-session-name ─", 1), agent.Idle},
		{"session name and agent panel, working", named(box("✻ Searching… (48s)", "❯ Also run the e2e after reviews")) + panel, agent.Working},
		{"session name and agent panel, empty box", named(box("✻ Worked for 2s", "❯ ")) + panel, agent.Idle},
		{"session name over permission options", named(box("", "❯ 1. Yes", "  2. No")), ""},
		{"session name over dialog text", named(box("", "❯ Option A", "", "Do you want to use option A?")), ""},
		{"a named rule isn't the bottom rule", strings.Join([]string{"⏺ Done.", "", rule, "❯ ", namedTop, "  ⏸ manual mode on"}, "\n"), ""},
		{"short dashes aren't the box", "text\n──\n❯ x\n──\nstatus", ""},
		{"a short named rule isn't the box's top", "text\n─ x ─\n❯ x\n" + rule + "\nstatus", ""},
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
