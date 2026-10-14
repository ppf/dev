//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev state against the bash hook (tmux-agent-state.sh) on two panes of a real server, step by step.
// Steps marked goOnly are the deliberate JSON fixes, where bash would record a different state.
func TestState(t *testing.T) {
	hook := filepath.Join(filepath.Dir(tmuxHome()), ".claude/hooks/tmux-agent-state.sh")
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "agents", "sleep 600")
	goPane := s.T("display", "-t", "=agents:", "-p", "#{pane_id}")
	bashPane := s.T("split-window", "-t", goPane, "-PF", "#{pane_id}", "sleep 600")
	state := func(pane string) string {
		return s.T("show-option", "-pqv", "-t", pane, "@agent_state") + "/" + s.T("show-option", "-pqv", "-t", pane, "@agent_cmd")
	}
	run := func(pane, stdin string, withPane bool, argv ...string) (string, error) {
		env := s.scriptEnv()
		if withPane {
			env = append(env, "TMUX_PANE="+pane)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	steps := []struct {
		name, state, stdin string
		noPane, goOnly     bool
		want, wantOut      string
	}{
		{name: "working", state: "working", stdin: `{"session_id":"s"}`, want: "working/sleep"},
		{name: "waiting", state: "waiting", stdin: `{}`, want: "waiting/sleep"},
		{name: "subagent working skipped", state: "working", stdin: `{"agent_id":"a1"}`, want: "waiting/sleep"},
		{name: "subagent tool-failed skipped", state: "tool-failed", stdin: `{"agent_id":"a1","is_interrupt":true}`, want: "waiting/sleep"},
		{name: "interrupt ends the turn", state: "tool-failed", stdin: `{"is_interrupt": true}`, want: "idle/sleep"},
		{name: "tool-failed keeps working", state: "tool-failed", stdin: `{"is_interrupt":false}`, want: "working/sleep"},
		{name: "idle", state: "idle", stdin: `{}`, want: "idle/sleep"},
		{name: "no TMUX_PANE is a no-op", state: "working", stdin: `{}`, noPane: true, want: "idle/sleep"},
		{name: "clear", state: "clear", stdin: `{}`, want: "/"},
		{name: "malformed JSON leaves the pane alone", state: "working", stdin: `{`, goOnly: true, want: "/",
			wantOut: "❌ dev state: agent: hook input: unexpected end of JSON input\n"},
		{name: "nested agent_id is not a subagent", state: "working", stdin: `{"tool_input":{"agent_id":"x"}}`, goOnly: true, want: "working/sleep"},
		{name: "nested is_interrupt is not an interrupt", state: "tool-failed", stdin: `{"tool_response":{"is_interrupt":true}}`, goOnly: true, want: "working/sleep"},
	}
	for _, st := range steps {
		out, err := run(goPane, st.stdin, !st.noPane, devBin, "state", st.state)
		if err != nil || out != st.wantOut {
			t.Errorf("❌ %s: dev state exit %v, output %q; want success, %q", st.name, err, out, st.wantOut)
		}
		check(t, st.name+" (dev)", state(goPane), st.want)
		if st.goOnly {
			continue
		}
		if out, err := run(bashPane, st.stdin, !st.noPane, hook, st.state); err != nil {
			t.Errorf("❌ %s: bash hook: %v\n%s", st.name, err, out)
		}
		check(t, st.name+" (dev = bash)", state(goPane), state(bashPane))
	}
}
