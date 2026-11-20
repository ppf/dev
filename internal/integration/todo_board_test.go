//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev todo board draws the lists as columns, opens an item over itself, answers and ticks through it and reads the
// lists again; esc closes the item, then the board. It runs in a pane as it does in its popup (the guard set).
func TestTodoBoard(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "boss-a", "-x", "120", "-y", "40", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	reg := exec.Command(devBin, "state", "idle")
	reg.Env = s.scriptEnv("TMUX_PANE=" + pane)
	reg.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("❌ register boss-a: %v %s", err, out)
	}
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, out)
		}
		return string(out)
	}
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	dev(boss, "todo", "ask", "Merge #148?", "--option", "Merge | ships today (Recommended)", "--option", "Wait")
	dev(boss, "todo", "add", "Restart the server")
	dev(boss, "todo", "add", "Review of the coupon change", "--on", "Carl")
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	// Another boss's list: not on boss-a's board
	s.T("new-session", "-d", "-s", "boss-b", "bash", script, "", filepath.Join(tmp, "boss-b.log"))
	s.T("set-option", "-t", "=boss-b:", "@dev_herd", "1")
	paneB := s.T("display", "-t", "=boss-b:", "-p", "#{pane_id}")
	regB := exec.Command(devBin, "state", "idle")
	regB.Env = s.scriptEnv("TMUX_PANE=" + paneB)
	regB.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := regB.CombinedOutput(); err != nil {
		t.Fatalf("❌ register boss-b: %v %s", err, out)
	}
	dev([]string{"TMUX_PANE=" + paneB, "CLAUDECODE=1"}, "todo", "add", "Only on boss-b")

	refuse := exec.Command(devBin, "todo", "board", "--", "nobody")
	refuse.Env = s.scriptEnv(user...)
	if out, err := refuse.CombinedOutput(); err == nil || !strings.Contains(string(out), "nobody: no such session and no to do list") {
		t.Errorf("❌ board of no boss: %v %s, want refused", err, out)
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_BOARD_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "board", "--client", "none", "--", "boss-a")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	capture := func() string { return s.T("capture-pane", "-p", "-t", view) }
	waitFor(t, "the board drawn", func() bool { return strings.Contains(capture(), "? a #1") })
	shown := capture()
	for _, want := range []string{"TO DO board · boss-a and its workers", "1 to decide · 1 yours · 3 open", "▸ For you", "Waiting on others",
		"☐ a #2", "a #3", "Carl", "h/l column"} {
		if !strings.Contains(shown, want) {
			t.Errorf("❌ %q missing:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, "Only on boss-b") {
		t.Errorf("❌ boss-b's item on boss-a's board:\n%s", shown)
	}

	// ⏎ opens the ask over the board; 2 answers it; the board reads the lists again
	s.T("send-keys", "-t", view, "Enter")
	waitFor(t, "the ask open", func() bool { return strings.Contains(capture(), "a #1 · To decide") })
	s.T("send-keys", "-t", view, "2")
	waitFor(t, "#1 answered and gone from the board", func() bool {
		return strings.Contains(dev(user, "todo", "--all", "--json"), `"answer": "Wait"`) && !strings.Contains(capture(), "? a #1")
	})

	// space opens the own item; d ticks it
	s.T("send-keys", "-t", view, "Space")
	waitFor(t, "the item open", func() bool { return strings.Contains(capture(), "a #2 · Yours") })
	s.T("send-keys", "-t", view, "d")
	waitFor(t, "#2 ticked and gone from the board", func() bool {
		return strings.Contains(dev(user, "todo", "--all", "--json"), `"done": true`) && !strings.Contains(capture(), "☐ a #2")
	})

	// esc closes the item view, then the board
	s.T("send-keys", "-t", view, "l", "l", "Enter") // For you, Starting · In progress, Waiting on others
	waitFor(t, "the person's item open", func() bool { return strings.Contains(capture(), "a #3 · waiting") })
	s.T("send-keys", "-t", view, "Escape")
	waitFor(t, "the item closed", func() bool { return !strings.Contains(capture(), "a #3 · waiting") })
	s.T("send-keys", "-t", view, "Escape")
	waitFor(t, "the board closed", func() bool {
		_, err := s.Try("capture-pane", "-p", "-t", view)
		return err != nil
	})
}
