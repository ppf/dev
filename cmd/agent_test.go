package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestTimedOut(t *testing.T) {
	working := control.Status{State: agent.Working, StateCmd: "claude", Command: "claude"}
	waiting := control.Status{State: agent.Waiting, StateCmd: "claude", Command: "claude"}
	run := func(st control.Status, err error) (string, bool) {
		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		handled := timedOut(cmd, st, err, 100*time.Second)
		return out.String(), handled
	}
	timeout := fmt.Errorf("%w after 1m40s", control.ErrTimeout)
	out, handled := run(working, timeout)
	if want := "working: still at it after 1m40s; wait again or read it\n"; out != want || !handled {
		t.Errorf("timedOut(working) = %q, %v, want %q, handled (exit 0)", out, handled, want)
	}
	// Not working (a prompt nobody picked up, a dialog): the caller keeps the error, exit 2
	if out, handled := run(waiting, timeout); handled || out != "" || exitCode(timeout) != exitTimeout {
		t.Errorf("timedOut(waiting) = %v, %q; want it left to the caller, exit 2", handled, out)
	}
	for _, err := range []error{errors.New("boom"), fmt.Errorf("x: %w", control.ErrStalled)} {
		if out, handled := run(working, err); handled || out != "" {
			t.Errorf("timedOut(%v) = %v, %q; want it left to the caller and silent", err, handled, out)
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
		{[]string{"agent", "start"}, "requires <project> [worktree], or --from <worker>"},
		{[]string{"agent", "start", "app", "a", "b"}, "accepts between 0 and 2 arg(s), received 3"},
		{[]string{"agent", "start", "app", "fix-2", "--from", "app-fix"}, "--from takes no <project> or [worktree]: the new session takes app-fix's"},
		{[]string{"agent", "start", "app", "--from", "app-fix"}, "--from takes no <project> or [worktree]: the new session takes app-fix's"},
		{[]string{"agent", "start", "--from", "app-fix", "--agent", "codex"}, "--from starts a fresh Claude, not --agent codex"},
		{[]string{"agent", "start", "--from", "app-fix", "--effort", "ultra"}, `unknown --effort "ultra", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "--from", "app-fix", "--model", "a b"}, `invalid --model "a b", want a model alias or id`},
		{[]string{"agent", "close"}, "accepts 1 arg(s), received 0"},
		{[]string{"agent", "start", "app", ""}, "empty worktree name"},
		{[]string{"agent", "start", "app", "--agent", "gemini"}, `unknown --agent "gemini", want claude, codex, grok or agy`},
		{[]string{"agent", "start", "app", "--effort", "ultra"}, `unknown --effort "ultra", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "app", "--effort", ""}, `unknown --effort "", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "app", "--model", ""}, `invalid --model "", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model", "son net"}, `invalid --model "son net", want a model alias or id`},
		{[]string{"agent", "start", "app", "--model", "a\tb"}, `invalid --model "a\tb", want a model alias or id`},
		{[]string{"agent", "start", "app", "--agent", "agy", "--effort", "ultra"}, `unknown --effort "ultra", want low, medium, high, xhigh, max`},
		{[]string{"agent", "start", "--from", "app-fix", "--agent", "agy"}, "--from starts a fresh Claude, not --agent agy"},
		{[]string{"agent", "start", "app", "--agent", "codex", "--effort", "minimal"}, `unknown --effort "minimal", want low, medium, high, xhigh, max, ultra`},
		{[]string{"agent", "start", "app", "--agent", "codex", "--model", "-x"}, `invalid --model "-x", want a model alias or id`},
		{[]string{"agent", "start", "app", "--agent", "grok", "--effort", "low"}, "--model and --effort only apply to Claude, Codex and agy, not --agent grok"},
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

func TestCheckStartAccepts(t *testing.T) {
	for _, args := range [][]string{nil, {"--model", "claude-opus-5-5[1m]", "--effort", "max"}, {"--model", "arn:aws:bedrock:us-east-1:1:inference-profile/x"}, {"--effort", "low"}} {
		var start agent.Start
		cmd := &cobra.Command{}
		addStartFlags(cmd, &start, "x")
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if err := checkStart(cmd, start, agent.Efforts); err != nil {
			t.Errorf("checkStart(%q) = %v, want none", args, err)
		}
	}
}

func TestCheckCodexStart(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	stub := func(script string) {
		if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub(`[ "$1 $2" = "debug models" ] || exit 9
echo '{"models":[{"slug":"gpt-a","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}]},{"slug":"gpt-b","supported_reasoning_levels":[{"effort":"medium"}]},{"slug":"gpt-c","supported_reasoning_levels":[]}]}'`)
	for _, tt := range []struct {
		start agent.Start
		err   string
	}{
		{agent.Start{Model: "gpt-a"}, ""},
		{agent.Start{Model: "gpt-a", Effort: "ultra"}, ""},
		{agent.Start{Model: "gpt-x", Effort: "low"}, `unknown --model "gpt-x" for codex, want one codex debug models lists: gpt-a, gpt-b, gpt-c`},
		{agent.Start{Model: "gpt-c"}, ""},
		{agent.Start{Model: "gpt-c", Effort: "low"}, "--model gpt-c takes no --effort"},
		{agent.Start{Model: "gpt-b", Effort: "ultra"}, "--model gpt-b doesn't take --effort ultra, want medium"},
	} {
		if err := checkCodexStart(tt.start, time.Minute); fmt.Sprint(err) != cmp.Or(tt.err, "<nil>") {
			t.Errorf("checkCodexStart(%+v) = %v, want %q", tt.start, err, tt.err)
		}
	}
	stub("echo 'not logged in' >&2; exit 1")
	if err := checkCodexStart(agent.Start{Model: "gpt-a"}, time.Minute); fmt.Sprint(err) != "checking --model and --effort: codex debug models: not logged in" {
		t.Errorf("checkCodexStart with a failing catalog = %v, want its stderr", err)
	}
	stub("echo '{\"models\":[]}'")
	if err := checkCodexStart(agent.Start{Model: "gpt-a"}, time.Minute); fmt.Sprint(err) != "agent: codex model catalog lists no models" {
		t.Errorf("checkCodexStart with an empty catalog = %v, want an error", err)
	}
	stub("exec sleep 5")
	if err := checkCodexStart(agent.Start{Model: "gpt-a"}, 100*time.Millisecond); fmt.Sprint(err) != "checking --model and --effort: codex debug models didn't answer within 100ms" {
		t.Errorf("checkCodexStart with a stuck catalog = %v, want a timeout", err)
	}
}

func TestShellLine(t *testing.T) {
	if got, want := shellLine([]string{"dev", "agent", "start", "My App", "fix", "--boss", "it's"}), `dev agent start 'My App' fix --boss 'it'\''s'`; got != want {
		t.Errorf("shellLine = %s, want %s", got, want)
	}
}

// Only an enabled dev state --agy hook counts, per event: a Stop alone would record no turn's start.
func TestAgyStateHooks(t *testing.T) {
	tests := []struct {
		name, file string
		want       []string
	}{
		{"start and end", `{"dev": {"PreInvocation": [{"type": "command", "command": "dev state --agy working"}],
			"Stop": [{"type": "command", "command": "dev state --agy idle"}]}}`, []string{"PreInvocation", "Stop"}},
		{"in matchers", `{"dev": {"PreToolUse": [{"matcher": "*", "hooks": [{"command": "dev state --agy working"}]}]}}`, []string{"PreToolUse"}},
		{"disabled hook", `{"dev": {"Stop": [{"command": "dev state --agy idle", "enabled": false}]}}`, nil},
		{"disabled matcher", `{"dev": {"PreToolUse": [{"matcher": "*", "enabled": false, "hooks": [{"command": "dev state --agy working"}]}]}}`, nil},
		{"another command", `{"log": {"Stop": [{"command": "logger stop"}]}}`, nil},
		{"a schema key and a hook of another shape", `{"$schema": "x", "dev": {"PreInvocation": [{"command": 1}, {"command": "dev state --agy working"}]}}`, []string{"PreInvocation"}},
		{"a group's enabled flag beside its events", `{"dev": {"enabled": true, "Stop": [{"command": "dev state --agy idle"}]},
			"old": {"enabled": false, "Stop": [{"command": "dev state --agy idle"}]}}`, []string{"Stop"}},
		{"a disabled group", `{"dev": {"enabled": false, "Stop": [{"command": "dev state --agy idle"}]}}`, nil},
	}
	for _, tt := range tests {
		got, err := agyStateHooks([]byte(tt.file))
		slices.Sort(got)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: agyStateHooks = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	for _, file := range []string{`{"dev": `, `null`} {
		if _, err := agyStateHooks([]byte(file)); err == nil {
			t.Errorf("%s: no error", file)
		}
	}
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]bool{
		`{"dev": {"Stop": [{"command": "dev state --agy idle"}]}}`:                                                            false,
		`{"dev": {"PreInvocation": [{"command": "dev state --agy working"}], "Stop": [{"command": "dev state --agy idle"}]}}`: true,
	} {
		if err := os.WriteFile(filepath.Join(dir, ".agents", "hooks.json"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := agyHooked(dir); got != want || err != nil {
			t.Errorf("agyHooked(%s) = %v, %v; want %v", file, got, err, want)
		}
	}
}

func TestAgyHookedBroken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "hooks.json"), []byte("// a comment\n{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hooked, err := agyHooked(dir); hooked || err == nil || !strings.Contains(err.Error(), "hooks.json") {
		t.Errorf("agyHooked(a broken file) = %v, %v; want false and why", hooked, err)
	}
}

// A message over the limit is refused before the boss, the pane or the hand-on gate are looked at; --help says the limit.
func TestMessageLimit(t *testing.T) {
	if _, _, err := execute(t, "", "agent", "message", "boss-a", strings.Repeat("x", 1501)); err == nil || !strings.Contains(err.Error(), "at most 1500") {
		t.Errorf("err = %v, want the limit", err)
	}
	if _, _, err := execute(t, "", "agent", "reply", "m1", strings.Repeat("x", 1501)); err == nil || !strings.Contains(err.Error(), "at most 1500") {
		t.Errorf("reply: err = %v, want the limit", err)
	}
	if help := run(t, "", "agent", "message", "--help"); !strings.Contains(help, "at most 1500 characters") {
		t.Errorf("help lacks the limit: %q", help)
	}
}

// prompt --help names the agents --wait works for.
func TestPromptWaitHelp(t *testing.T) {
	if help := run(t, "", "agent", "prompt", "--help"); strings.Contains(help, "--wait (Claude only)") || !strings.Contains(help, "Codex, Grok") {
		t.Errorf("help = %q", help)
	}
}
