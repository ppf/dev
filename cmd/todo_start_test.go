package cmd

import (
	"strings"
	"testing"
)

// start refuses a Claude Code session (not a board's button), no --question, --boss, a desktop app boss and a bad
// number before reaching tmux.
func TestTodoStartRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		claude string
		args   []string
		want   string
	}{
		{"1", []string{"todo", "start", "boss-dev", "1", "--question", "a"}, "refused in a Claude Code session"},
		{"", []string{"todo", "start", "boss-dev", "1"}, "--question <the item's text> is needed"},
		{"", []string{"todo", "start", "boss-dev", "1", "--question", "a", "--boss", "boss-x"}, "leave out --boss"},
		{"1", []string{"todo", "start", "local_boss", "1", "--question", "a", "--from-board"}, "has no dev inbox"},
		{"", []string{"todo", "start", "boss-dev", "x", "--question", "a"}, `item number "x"`},
	} {
		t.Setenv("CLAUDECODE", tt.claude)
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}
