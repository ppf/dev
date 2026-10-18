package cmd

import (
	"strings"
	"testing"
)

// A Claude hook: never anything on stdout (Claude reads it as the decision), a failure only on stderr.
func TestQuestionHook(t *testing.T) {
	ask := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
	t.Setenv("TMUX_PANE", "")
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
