package cmd

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/serve"
)

// Reopen refuses a Claude Code session (not a board's button), no --question, --boss and a bad number before reaching
// tmux.
func TestTodoReopenRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		claude string
		args   []string
		want   string
	}{
		{"1", []string{"todo", "reopen", "boss-dev", "1", "--question", "a"}, "refused in a Claude Code session"},
		{"", []string{"todo", "reopen", "boss-dev", "1"}, "--question <the item's text> is needed"},
		{"", []string{"todo", "reopen", "boss-dev", "1", "--question", "a", "--boss", "boss-x"}, "leave out --boss"},
		{"", []string{"todo", "reopen", "boss-dev", "x", "--question", "a"}, `item number "x"`},
	} {
		t.Setenv("CLAUDECODE", tt.claude)
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

// The user reopens their ticked item with its number, or the next one once its number is taken; the page's reopen of
// a ticked item not theirs is a 409.
func TestTodoReopen(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("CLAUDECODE", "1")
	run(t, "", "todo", "add", "mine")
	run(t, "", "todo", "add", "theirs", "--waiting-for", "Carl")
	run(t, "", "todo", "done", "1", "2")
	// A board's button runs it inside a Claude Code session
	if out := run(t, "", "todo", "reopen", "local_boss", "1", "--question", "mine", "--from-board"); out != "#1 reopened\n" {
		t.Errorf("reopen = %q", out)
	}
	t.Setenv("CLAUDECODE", "")
	if out := run(t, "", "todo", "--json"); !strings.Contains(out, `"text": "mine"`) || !strings.Contains(out, `"column": "todo"`) {
		t.Errorf("--json after reopen:\n%s", out)
	}
	var ref serve.Refused
	if err := pageReopen(io.Discard)("local_boss", 2, "theirs"); !errors.As(err, &ref) || ref.Code != http.StatusConflict || !strings.Contains(err.Error(), "not the user's to reopen") {
		t.Errorf("not the user's: %v, want 409", err)
	}
	// #1 ticked again, the list emptied and a new #1 added: the old one comes back as #2
	t.Setenv("CLAUDECODE", "1")
	run(t, "", "todo", "done", "1")
	run(t, "", "todo", "clear")
	run(t, "", "todo", "add", "new")
	t.Setenv("CLAUDECODE", "")
	if out := run(t, "", "todo", "reopen", "local_boss", "1", "--question", "mine"); out != "#1 reopened as #2: #1 is another item now\n" {
		t.Errorf("reopen over a taken number = %q", out)
	}
}

// Overrule refuses a Claude Code session (not a board's key), no --question, --boss and a bad number before reaching
// tmux.
func TestTodoOverruleRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		claude string
		args   []string
		want   string
	}{
		{"1", []string{"todo", "overrule", "boss-dev", "1", "B", "--question", "a"}, "refused in a Claude Code session"},
		{"", []string{"todo", "overrule", "boss-dev", "1", "B"}, "--question <the item's text> is needed"},
		{"", []string{"todo", "overrule", "boss-dev", "1", "B", "--question", "a", "--boss", "boss-x"}, "leave out --boss"},
		{"", []string{"todo", "overrule", "boss-dev", "x", "B", "--question", "a"}, `item number "x"`},
	} {
		t.Setenv("CLAUDECODE", tt.claude)
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}
