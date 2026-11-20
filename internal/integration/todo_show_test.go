//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev todo show draws a boss's ask with its options and answers it by number; it refuses an item that reads
// otherwise. The view runs in a pane as it does in its popup (the guard set), as a detached server has no client to
// pop up on.
func TestTodoShow(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "boss-a", "-x", "120", "-y", "40", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	dev := func(env []string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), &exec.ExitError{Stderr: []byte(stderr.String())}
		}
		return string(out), nil
	}
	reg := exec.Command(devBin, "state", "idle")
	reg.Env = s.scriptEnv("TMUX_PANE=" + pane)
	reg.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("❌ register boss-a: %v %s", err, out)
	}
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	if _, err := dev(boss, "todo", "ask", "Merge #148?", "--option", "Merge | ships today (Recommended)", "--option", "Wait", "--context", "CI green"); err != nil {
		t.Fatal(err)
	}

	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	if _, err := dev(user, "todo", "show", "--question", "Another?", "boss-a", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), `is "Merge #148?" now, not "Another?"`) {
		t.Errorf("❌ show with a changed question: %v, want refused", err)
	}
	if _, err := dev(user, "todo", "show", "boss-a", "2"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "no open item #2") {
		t.Errorf("❌ show of no item: %v, want refused", err)
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_SHOW_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "show", "boss-a", "1")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	waitFor(t, "the view drawn", func() bool {
		return strings.Contains(s.T("capture-pane", "-p", "-t", view), "1-2 answer")
	})
	shown := s.T("capture-pane", "-p", "-t", view)
	for _, want := range []string{"a #1 · To decide", "Merge #148?", "CI green", "▸ 1  ★ Merge", "ships today", "2    Wait", "m    In your own words…", "status  decide"} {
		if !strings.Contains(shown, want) {
			t.Errorf("❌ %q missing:\n%s", want, shown)
		}
	}
	s.T("send-keys", "-t", view, "2")
	waitFor(t, "#1 answered", func() bool {
		out, _ := dev(user, "todo", "--all", "--json")
		return strings.Contains(out, `"answer": "Wait"`)
	})
	if _, err := dev(user, "todo", "show", "boss-a", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "no open item #1") {
		t.Errorf("❌ show of an answered item: %v, want refused", err)
	}
	if _, err := dev(user, "todo", "show", "nobody", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "nobody has no to do list") {
		t.Errorf("❌ show of no list: %v, want refused", err)
	}

	// In its popup, an error before the view starts stays on screen until a key
	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_SHOW_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "show", "boss-a", "1")
	failed := s.T("display", "-t", "=boss-a:3", "-p", "#{pane_id}")
	waitFor(t, "the error kept", func() bool {
		out := s.T("capture-pane", "-p", "-t", failed)
		return strings.Contains(out, "no open item #1") && strings.Contains(out, "Press enter to close")
	})
}
