package cmd

import (
	"strings"
	"testing"
)

// dev serve pair --popup runs in Claude Code, from the manager's coordinator pane only.
func TestServePairPopupRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("TMUX_PANE", "")
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "runs only in the manager's coordinator pane") || out != "" {
		t.Errorf("--popup outside the manager: %q %v, want refused", out, err)
	}
}
