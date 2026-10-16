//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentStub writes a fake Claude: it registers like the SessionStart hook, then echoes each input line as
// "got: <line>" and runs the UserPromptSubmit and Stop hooks around it (a "slow" line takes 3s, an "ask"
// line ends waiting on a permission prompt instead). With STUB_DEAF set it echoes but runs no hooks, like
// an agent that never picked the prompt up.
func agentStub(t *testing.T, dir, name string) string {
	t.Helper()
	dev := shellQuote(devBin)
	return filepath.Join(writeStubs(t, dir, `hook() { printf '{"hook_event_name":"%s","notification_type":"%s"}' "$2" "$3" | `+dev+` state "$1"; }
hook idle SessionStart
while IFS= read -r line; do
	echo "got: $line"
	[ -n "$STUB_DEAF" ] && continue
	hook working UserPromptSubmit
	case "$line" in
	slow*) sleep 3 ;;
	ask*) hook waiting Notification permission_prompt; continue ;;
	esac
	hook idle Stop
done`, name), name)
}

// devAgent runs dev agent args with env and returns stdout, stderr and the exit status.
func devAgent(env []string, stdin string, args ...string) (string, string, string) {
	cmd := exec.Command(devBin, append([]string{"agent"}, args...)...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), errStatus(err)
}

// The control loop on a real server: list, prompt --wait, read, busy and timeout, stall, send-keys,
// self-target and a pane without hook state.
func TestAgentControl(t *testing.T) {
	// @dev_notify off: the stub's Stop hook must never reach a real notifier
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n")
	stub := agentStub(t, t.TempDir(), "agent")
	codex := sleeper(t, "codex")
	s.T("new-session", "-d", "-s", "w1", stub)
	s.T("new-session", "-d", "-s", "deaf", "-e", "STUB_DEAF=1", stub)
	s.T("new-session", "-d", "-s", "cx", codex)
	s.T("new-session", "-d", "-s", "plain", "sleep 600")
	env := s.scriptEnv()
	w1 := s.T("display", "-t", "=w1:", "-p", "#{pane_id}")
	waitFor(t, "stubs registered", func() bool {
		out, _, _ := devAgent(env, "", "list")
		return strings.Count(out, "\tidle\t") == 2
	})

	t.Run("list", func(t *testing.T) {
		out, errOut, status := devAgent(env, "", "list", "--json")
		var agents []map[string]string
		if err := json.Unmarshal([]byte(out), &agents); err != nil || status != "0" {
			t.Fatalf("list --json: %v, exit %s, %q %q", err, status, out, errOut)
		}
		var got []string
		for _, a := range agents {
			got = append(got, a["session"]+"="+a["agent"]+"/"+a["state"])
		}
		check(t, "list", strings.Join(got, " "), "cx=Codex/ deaf=Claude/idle w1=Claude/idle")
		out, _, _ = devAgent(env, "", "get", w1)
		check(t, "get by pane id", strings.Join(strings.Split(out, "\t")[:4], " "), "w1 "+w1+" Claude idle")
	})

	t.Run("prompt --wait and read", func(t *testing.T) {
		out, errOut, status := devAgent(env, "", "prompt", "w1", "--wait", "--timeout", "30s", "hello", "there;")
		check(t, "prompt --wait", status+" "+out+errOut, "0 idle\n")
		out, _, _ = devAgent(env, "", "read", "w1", "--lines", "1")
		check(t, "read", out, "got: hello there;\n")
		// Multi-line from stdin pastes as one piece; the line-reading stub sees each line
		devAgent(env, "line one\nline two\n", "prompt", "w1")
		waitFor(t, "both lines read", func() bool {
			out, _, _ := devAgent(env, "", "read", "w1", "--lines", "5")
			return strings.Contains(out, "got: line one\n") && strings.HasSuffix(out, "got: line two\n")
		})
	})

	t.Run("busy, timeout, wait", func(t *testing.T) {
		devAgent(env, "", "prompt", "w1", "slow")
		waitFor(t, "w1 working", func() bool {
			out, _, _ := devAgent(env, "", "get", "w1")
			return strings.Contains(out, "\tworking\t")
		})
		_, errOut, status := devAgent(env, "", "prompt", "w1", "--wait", "again")
		check(t, "prompt --wait on a busy agent", status+" "+errOut, "1 ❌ agent is working: wait for it first\n")
		_, _, status = devAgent(env, "", "wait", "w1", "--until", "idle", "--timeout", "500ms")
		check(t, "wait timeout exit", status, "2")
		out, _, status := devAgent(env, "", "wait", "w1", "--timeout", "30s")
		check(t, "wait settled", status+" "+out, "0 idle\n")
	})

	t.Run("permission prompt", func(t *testing.T) {
		devAgent(env, "", "prompt", "w1", "ask")
		out, _, status := devAgent(env, "", "wait", "w1", "--until", "waiting", "--timeout", "30s")
		check(t, "wait --until waiting", status+" "+out, "0 waiting\n")
		for _, args := range [][]string{{"prompt", "w1", "2"}, {"prompt", "w1", "--wait", "2"}} {
			_, errOut, status := devAgent(env, "", args...)
			check(t, strings.Join(args, " ")+" refused", status+" "+errOut,
				"1 ❌ agent is waiting for a decision (permission_prompt): read it and answer with send-keys\n")
		}
		devAgent(env, "", "send-keys", "w1", "y", "enter")
		out, _, status = devAgent(env, "", "wait", "w1", "--until", "idle", "--timeout", "30s")
		check(t, "answered", status+" "+out, "0 idle\n")
	})

	t.Run("stall", func(t *testing.T) {
		start := time.Now()
		_, errOut, status := devAgent(env, "", "prompt", "deaf", "--wait", "--stall", "1s", "anyone?")
		check(t, "stall exit", status, "3")
		check(t, "stall message", errOut, "❌ stalled: the agent never picked up the prompt within 1s\n")
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("❌ stall took %s", d)
		}
	})

	t.Run("send-keys", func(t *testing.T) {
		_, errOut, status := devAgent(env, "", "send-keys", "deaf", "x", ";", "enter")
		check(t, "send-keys", status+errOut, "0")
		waitFor(t, "keys read", func() bool {
			out, _, _ := devAgent(env, "", "read", "deaf", "--lines", "1")
			return out == "got: x;\n"
		})
	})

	t.Run("refusals", func(t *testing.T) {
		_, errOut, status := devAgent(append(env, "TMUX_PANE="+w1), "", "wait", "w1")
		check(t, "own pane", status+" "+errOut, "1 ❌ w1 is this agent's own pane\n")
		_, errOut, status = devAgent(env, "", "wait", "cx")
		check(t, "codex wait", status+" "+errOut, "1 ❌ agent has no hook state (Codex without its hooks): use read\n")
		_, errOut, status = devAgent(env, "", "get", "plain")
		check(t, "no agent", status+" "+errOut, "1 ❌ session \"plain\" has no agent pane\n")
		_, errOut, status = devAgent(env, "", "get", "w")
		check(t, "no prefix match", status+" "+errOut, "1 ❌ no session \"w\"\n")
	})
}

// A Codex pane with the dev state --codex hooks has a state: wait works and an approval request refuses prompts.
func TestAgentCodexHooks(t *testing.T) {
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n")
	s.T("new-session", "-d", "-s", "cx", sleeper(t, "codex"))
	env := s.scriptEnv()
	cwd := s.T("display", "-t", "=cx:", "-p", "#{pane_current_path}")
	hook := func(state, event string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", "--codex", state)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `","cwd":"` + cwd + `"}`)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("dev state --codex %s: %v %s", state, err, out)
		}
	}
	hook("idle", "SessionStart")
	out, _, _ := devAgent(env, "", "get", "cx")
	check(t, "codex state", strings.Join(strings.Split(out, "\t")[:4], " "), "cx "+s.T("display", "-t", "=cx:", "-p", "#{pane_id}")+" Codex idle")
	hook("waiting", "PermissionRequest")
	out, _, status := devAgent(env, "", "wait", "cx", "--until", "waiting", "--timeout", "5s")
	check(t, "codex wait", status+" "+out, "0 waiting\n")
	_, errOut, status := devAgent(env, "", "prompt", "cx", "yes")
	check(t, "codex approval refuses prompts", status+" "+errOut,
		"1 ❌ agent is waiting for a decision (PermissionRequest): read it and answer with send-keys\n")
}

// dev agent start opens a project detached, returns once the agent registered, and reuses the session.
// It skips the first-run restore, whose switch-client would move the user's client.
func TestAgentStart(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	// The layout's claude becomes the fake agent
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	dir := tempDir(t)
	env := d.scriptEnv(writeProjects(t, [2]string{"app", dir}))

	start := func() map[string]string {
		t.Helper()
		out, errOut, status := devAgent(env, "", "start", "app", "--json", "--timeout", "30s")
		if status != "0" {
			t.Fatalf("start: exit %s: %s", status, errOut)
		}
		var a map[string]string
		if err := json.Unmarshal([]byte(out), &a); err != nil {
			t.Fatalf("start output %q: %v", out, err)
		}
		return a
	}
	first := start()
	check(t, "agent", first["session"]+" "+first["agent"]+" "+first["state"]+" "+first["path"], "app Claude idle "+dir)
	check(t, "agent pane is the layout's claude", d.T("display", "-t", first["pane"], "-p", "#{window_index}.#{pane_index}"), "1.2")
	check(t, "client not switched", d.T("list-clients", "-F", "#{client_session}"), "boot")
	check(t, "no first-run restore", d.T("show-option", "-gqv", "@dev_restored"), "")
	second := start()
	check(t, "reused", second["pane"], first["pane"])
	check(t, "sessions", d.T("list-sessions", "-F", "#{session_name}"), "app\nboot")

	repo := newRepo(t, "", "")
	env = d.scriptEnv(append(gitEnv, writeProjects(t, [2]string{"repo", repo}))...)
	out, errOut, status := devAgent(env, "", "start", "repo", "feature/x", "--json", "--timeout", "30s")
	var wt map[string]string
	if err := json.Unmarshal([]byte(out), &wt); err != nil || status != "0" {
		t.Fatalf("start worktree: exit %s, %v: %q %q", status, err, out, errOut)
	}
	check(t, "worktree agent", wt["session"]+" "+wt["path"], "repo-feature-x "+filepath.Join(repo, ".claude/worktrees/feature/x"))
	check(t, "worktree: client not switched", d.T("list-clients", "-F", "#{client_session}"), "boot")
}
