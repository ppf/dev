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
	out, _, err := execute(t, "", "agent", "skill")
	if err != nil || !strings.HasPrefix(out, "---\nname: dev-agents\n") {
		t.Errorf("agent skill = %q, %v", out, err)
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
		{[]string{"agent", "start", "app", ""}, "empty worktree name"},
		{[]string{"agent", "start", "app", "--agent", "gemini"}, `unknown --agent "gemini", want claude or codex`},
		{[]string{"agent", "wait", "app", "--until", "done"}, `unknown --until "done", want idle, waiting or settled`},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || err.Error() != tt.err {
			t.Errorf("dev %q = %v, want %q", tt.args, err, tt.err)
		}
	}
}
