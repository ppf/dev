//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dev todo show draws a boss's ask with its options and answers it by number, and opens it answered as Done; it refuses
// an item that reads otherwise or isn't there. The view runs in a pane as it does in its popup (the guard set), as a detached server has no client to
// pop up on.
func TestTodoShow(t *testing.T) {
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(t.TempDir(), "state"))
	dev := askingBoss(t, s)
	user := []string{"TMUX_PANE=", "CLAUDECODE="}

	if _, err := dev(user, "todo", "show", "--question", "Another?", "boss-a", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), `is "Merge #148?" now, not "Another?"`) {
		t.Errorf("❌ show with a changed question: %v, want refused", err)
	}
	if _, err := dev(user, "todo", "show", "boss-a", "2"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "has no item #2, open or ticked in the last day") {
		t.Errorf("❌ show of no item: %v, want refused", err)
	}

	// --print: the item as text, no popup, as the manager checks a note from a Claude pane
	if out, err := dev([]string{"TMUX_PANE=", "CLAUDECODE=1"}, "todo", "show", "--print", "boss-a", "1"); err != nil ||
		out != "1\t☐\tMerge #148?\toptions: Merge | ships today (Recommended) · Wait\n    - CI green\n" {
		t.Errorf("❌ show --print = %q, %v", out, err)
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_SHOW_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "show", "boss-a", "1")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	waitFor(t, "the view drawn", func() bool {
		return strings.Contains(s.T("capture-pane", "-p", "-t", view), "1-2 answer")
	})
	shown := s.T("capture-pane", "-p", "-t", view)
	for _, want := range []string{"a #1 · To do", "Merge #148?", "CI green", "▸ 1  ★ Merge", "ships today", "2    Wait", "m    In your own words…", "status  decide"} {
		if !strings.Contains(shown, want) {
			t.Errorf("❌ %q missing:\n%s", want, shown)
		}
	}
	s.T("send-keys", "-t", view, "2")
	waitFor(t, "#1 answered", func() bool {
		out, _ := dev(user, "todo", "--all", "--json")
		return strings.Contains(out, `"answer": "Wait"`)
	})
	// Answered, it opens as Done, to reopen
	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_SHOW_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "show", "boss-a", "1")
	done := s.T("display", "-t", "=boss-a:3", "-p", "#{pane_id}")
	waitFor(t, "the answered item drawn", func() bool {
		out := s.T("capture-pane", "-p", "-t", done)
		return strings.Contains(out, "a #1 · Done") && strings.Contains(out, "r  Reopen")
	})
	if _, err := dev(user, "todo", "show", "nobody", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "nobody has no to do list") {
		t.Errorf("❌ show of no list: %v, want refused", err)
	}

	// In its popup, an error before the view starts stays on screen until a key
	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_SHOW_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "show", "boss-a", "9")
	failed := s.T("display", "-t", "=boss-a:4", "-p", "#{pane_id}")
	waitFor(t, "the error kept", func() bool {
		out := s.T("capture-pane", "-p", "-t", failed)
		return strings.Contains(out, "has no item #9") && strings.Contains(out, "Press enter to close")
	})
}

// askingBoss starts boss session boss-a on s, its list holding ask #1 "Merge #148?", and returns dev run against s.
func askingBoss(t *testing.T, s *server) func(env []string, argv ...string) (string, error) {
	t.Helper()
	tmp := t.TempDir()
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
	return dev
}

// dev todo show's and board's popups have the terminal's colours the views are drawn for, not tmux's popup-style:
// with a light client theme that is mid grey (gray45), on which the views' dim text can't be read.
func TestTodoPopupColours(t *testing.T) {
	const rgb = "set -as terminal-features ',*:RGB'\n"
	in := newServer(t, rgb+"set -g theme light\nset -g escape-time 0\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(t.TempDir(), "state"))
	dev := askingBoss(t, in)
	out := newServer(t, rgb)
	out.T("new-session", "-d", "-s", "out", "-x", "160", "-y", "40")
	out.T("send-keys", "-t", "out", "TMUX= tmux -L "+in.socket+" attach -t boss-a", "Enter")
	var client string
	waitFor(t, "the client attached", func() bool { client, _ = in.Try("list-clients", "-F", "#{client_name}"); return client != "" })
	grey := "48;2;115;115;115m"
	// Each popup's own text, so a redraw still showing the last one isn't taken for the next
	for _, c := range []struct {
		argv []string
		mark string
	}{
		{[]string{"todo", "show", "--client", client, "boss-a", "1"}, "a #1 · To do"},
		{[]string{"todo", "board", "--client", client}, "TO DO board"},
	} {
		done := make(chan error, 1)
		go func() { _, err := dev([]string{"TMUX_PANE=", "CLAUDECODE="}, c.argv...); done <- err }()
		waitFor(t, c.argv[1]+" popup drawn", func() bool {
			return strings.Contains(out.T("capture-pane", "-p", "-t", "out"), c.mark)
		})
		if screen := out.T("capture-pane", "-e", "-p", "-t", "out"); strings.Contains(screen, grey) {
			t.Errorf("❌ %s popup on popup-style's grey:\n%s", c.argv[1], screen)
		}
		out.T("send-keys", "-t", "out", "Escape")
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("❌ %s: %v", c.argv[1], err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("❌ %s popup still open after esc", c.argv[1])
		}
	}
}
