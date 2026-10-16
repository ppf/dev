//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
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

// dev state --codex finds the pane by the hook's cwd, not TMUX_PANE (the Codex daemon's, here a plain pane).
func TestCodexState(t *testing.T) {
	s := newServer(t, "")
	codex := sleeper(t, "codex")
	dirA, dirB, other := t.TempDir(), t.TempDir(), t.TempDir()
	s.T("new-session", "-d", "-s", "agents", "-c", dirA, codex)
	a := s.T("display", "-t", "=agents:", "-p", "#{pane_id}")
	daemon := s.T("split-window", "-t", a, "-PF", "#{pane_id}", "sleep 600")
	b1 := s.T("split-window", "-t", a, "-c", dirB, "-PF", "#{pane_id}", codex)
	b2 := s.T("split-window", "-t", a, "-c", dirB, "-PF", "#{pane_id}", codex)
	waitFor(t, "codex stubs running", func() bool {
		return s.T("display", "-t", b2, "-p", "#{pane_current_command}") == "codex"
	})
	cwdA, cwdB := s.T("display", "-t", a, "-p", "#{pane_current_path}"), s.T("display", "-t", b1, "-p", "#{pane_current_path}")
	r := newNotifyRig(t, s)
	state := func(pane string) string {
		return s.T("show-option", "-pqv", "-t", pane, "@agent_state") + "/" + s.T("show-option", "-pqv", "-t", pane, "@agent_cmd")
	}
	run := func(env []string, st, stdin string) string {
		t.Helper()
		cmd := exec.Command(devBin, "state", "--codex", st)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev state --codex %s: %v\n%s", st, err, out)
		}
		return string(out)
	}
	env := s.scriptEnv("TMUX_PANE="+daemon, "PATH="+r.bin+":/usr/bin:/bin")
	hook := func(st, event, cwd, extra string) {
		t.Helper()
		if out := run(env, st, `{"hook_event_name":"`+event+`","cwd":"`+cwd+`"`+extra+`}`); out != "" {
			t.Errorf("❌ %s: output %q", event, out)
		}
	}

	hook("working", "PostToolUse", cwdA, "")
	check(t, "state on the cwd's codex pane", state(a), "working/codex")
	check(t, "TMUX_PANE's pane untouched", state(daemon), "/")

	hook("waiting", "PermissionRequest", cwdA, `,"tool_name":"Bash","tool_input":{"command":"touch x","description":"May I run touch x?"}`)
	check(t, "approval waits", state(a), "waiting/codex")
	n := r.notes(t, 1)[0]
	check(t, "approval note", arg(n, "-subtitle")+"/"+arg(n, "-message")+"/"+arg(n, "-sound"), "Needs approval/May I run touch x?/default")
	check(t, "click switches to the codex pane", arg(n, "-execute"),
		tmux.ShellJoin(filepath.Join(r.bin, "tmux"), "-S", s.socketPath(), "switch-client", "-t", a))

	hook("idle", "Stop", other, `,"last_assistant_message":"memories consolidated"`)
	hook("working", "PostToolUse", cwdB, "")
	check(t, "no pane in cwd (memory agent) leaves panes alone", state(a), "waiting/codex")
	check(t, "two codex panes in cwd leave both alone", state(b1)+" "+state(b2), "/ /")

	hook("idle", "Stop", cwdA, `,"last_assistant_message":"All tests pass."`)
	check(t, "stop", state(a), "idle/codex")
	n = r.notes(t, 2)[1]
	check(t, "unmatched Stop skipped, matched Stop notifies", arg(n, "-subtitle")+"/"+arg(n, "-message"), "Done/All tests pass.")

	hook("working", "UserPromptSubmit", cwdA, "")
	hook("idle", "Interrupt", cwdA, "")
	check(t, "interrupt", state(a), "idle/codex")
	hook("clear", "SessionEnd", cwdA, "")
	check(t, "session end clears", state(a), "/")
	r.notes(t, 2)

	noServer := append(s.tmux.Env(), "TMUX_TMPDIR="+t.TempDir(), "PATH="+r.bin+":/usr/bin:/bin")
	if out := run(noServer, "working", `{"hook_event_name":"PostToolUse","cwd":"`+cwdA+`"}`); out != "" {
		t.Errorf("❌ no tmux server: output %q, want none", out)
	}
}
