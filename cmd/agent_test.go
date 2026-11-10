package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
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
		{[]string{"agent", "start", "app", "--effort", "ultra"}, `unknown --effort "ultra", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "app", "--effort", ""}, `unknown --effort "", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "app", "--model", ""}, `invalid --model "", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model", "son net"}, `invalid --model "son net", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model", "a\tb"}, `invalid --model "a\tb", want a model alias or id`},
		{[]string{"agent", "start", "app", "--agent", "codex", "--model", "sonnet"}, "--model and --effort only apply to Claude, not --agent codex"},
		{[]string{"agent", "start", "app", "--agent", "grok", "--effort", "low"}, "--model and --effort only apply to Claude, not --agent grok"},
		{[]string{"agent", "start", "app", "--model", "a\x1bb"}, `invalid --model "a\x1bb", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model", "opus;"}, `invalid --model "opus;", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model=--resume"}, `invalid --model "--resume", want a model alias or id`},
		{[]string{"boss", "--model", ";"}, `invalid --model ";", want a model alias or id`},
		{[]string{"boss", "--effort", "ultra"}, `unknown --effort "ultra", want low, medium, high, xhigh, max`},
		{[]string{"boss", "--model", " "}, `invalid --model " ", want a model alias or id`},
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

func TestCheckStartAccepts(t *testing.T) {
	for _, args := range [][]string{nil, {"--model", "claude-opus-5-5[1m]", "--effort", "max"}, {"--model", "arn:aws:bedrock:us-east-1:1:inference-profile/x"}, {"--effort", "low"}} {
		var start agent.Start
		cmd := &cobra.Command{}
		addStartFlags(cmd, &start, "x")
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if err := checkStart(cmd, start); err != nil {
			t.Errorf("checkStart(%q) = %v, want none", args, err)
		}
	}
}
