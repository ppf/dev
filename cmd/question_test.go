package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Claude hook: never anything on stdout (Claude reads it as the decision), a failure only on stderr.
func TestQuestionHook(t *testing.T) {
	ask := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	t.Setenv("CLAUDE_PID", "")
	if out, errOut, err := execute(t, ask, "question"); out != "" || errOut != "" || err != nil {
		t.Errorf("outside tmux: %q %q %v", out, errOut, err)
	}
	// Under test tmux refuses the default server (ErrNoSocket), so recording fails without reaching one
	t.Setenv("TMUX_PANE", "%1")
	if out, errOut, err := execute(t, ask, "question"); out != "" || !strings.Contains(errOut, "❌ dev question: ") || err != nil {
		t.Errorf("failed record: %q %q %v", out, errOut, err)
	}
	if out, errOut, err := execute(t, `{"hook_event_name":"Stop"}`, "question"); out != "" || errOut != "" || err != nil {
		t.Errorf("another event: %q %q %v", out, errOut, err)
	}
}

// In a desktop app session the dialog goes to the session's hook file, read by dev desktop board.
func TestQuestionHookDesktop(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	ask := `{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`
	if out, errOut, err := execute(t, ask, "question"); out != "" || errOut != "" || err != nil {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
	data, err := os.ReadFile(filepath.Join(state, "dev", "desktop", "s1.json"))
	if err != nil || !strings.Contains(string(data), `"question":"allow Bash: ls"`) {
		t.Errorf("hook file = %s, %v; want the question", data, err)
	}
}
