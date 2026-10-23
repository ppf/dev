package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/control"
)

func TestExitCode(t *testing.T) {
	for err, want := range map[error]int{
		errors.New("boom"):                      1,
		fmt.Errorf("x: %w", control.ErrTimeout): exitTimeout,
		fmt.Errorf("x: %w", control.ErrStalled): exitStalled,
	} {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestAgentSkill(t *testing.T) {
	for _, tt := range []struct {
		args []string
		name string
	}{
		{[]string{"agent", "skill"}, "dev-agents"},
		{[]string{"agent", "skill", "dev-agents"}, "dev-agents"},
		{[]string{"agent", "skill", "desktop-boss"}, "desktop-boss"},
	} {
		out, _, err := execute(t, "", tt.args...)
		if err != nil || !strings.HasPrefix(out, "---\nname: "+tt.name+"\n") {
			t.Errorf("dev %q = %.40q, %v, want skill %s", tt.args, out, err, tt.name)
		}
	}
	if _, _, err := execute(t, "", "agent", "skill", "boss"); err == nil || err.Error() != `unknown skill "boss", want dev-agents or desktop-boss` {
		t.Errorf("agent skill boss = %v, want an unknown-skill error", err)
	}
}

func TestAgentArgs(t *testing.T) {
	for _, tt := range []struct {
		args []string
		err  string
	}{
		{[]string{"agent", "get"}, "accepts 1 arg(s), received 0"},
		{[]string{"agent", "send-keys", "app"}, "requires at least 2 arg(s), only received 1"},
		{[]string{"agent", "prompt"}, "requires at least 1 arg(s), only received 0"},
		{[]string{"agent", "start"}, "accepts between 1 and 2 arg(s), received 0"},
		{[]string{"agent", "close"}, "accepts 1 arg(s), received 0"},
		{[]string{"agent", "start", "app", ""}, "empty worktree name"},
		{[]string{"agent", "start", "app", "--agent", "gemini"}, `unknown --agent "gemini", want claude, codex or grok`},
		{[]string{"agent", "wait", "app", "--until", "done"}, `unknown --until "done", want idle, waiting or settled`},
		{[]string{"agent", "list", "--boss", "herd"}, "--boss needs --mine"},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || err.Error() != tt.err {
			t.Errorf("dev %q = %v, want %q", tt.args, err, tt.err)
		}
	}
}

func TestAgentCloseOutsideTmux(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	if _, _, err := execute(t, "", "agent", "close", "app"); err == nil || !strings.Contains(err.Error(), "run it inside tmux") {
		t.Errorf("agent close outside tmux = %v, want an inside-tmux error", err)
	}
}
