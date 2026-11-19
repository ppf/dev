//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The user starts their own item from a shell: its live boss gets "you were asked to start" and the item reads
// starting until a --replaces; a second start is refused. A closed boss's item goes to the manager, refused while
// none runs.
func TestTodoStart(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
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
	refused := func(env []string, want string, argv ...string) {
		t.Helper()
		if _, err := dev(env, argv...); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), want) {
			t.Errorf("❌ dev %s: %v, want %q", strings.Join(argv, " "), err, want)
		}
	}
	open := func(name string) (pane, log string) {
		t.Helper()
		log = filepath.Join(tmp, name+".log")
		s.T("new-session", "-d", "-s", name, "bash", script, "", log)
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		cmd := exec.Command(devBin, "state", "idle")
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, out)
		}
		waitFor(t, name+" stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane), "Try") })
		return pane, log
	}
	logged := func(log, want string) {
		t.Helper()
		waitFor(t, "note "+want, func() bool {
			data, _ := os.ReadFile(log)
			return strings.Contains(string(data), want)
		})
	}
	json := func() string {
		t.Helper()
		out, err := dev([]string{"TMUX_PANE="}, "todo", "--all", "--json")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(out), "")
	}

	s.T("new-session", "-d", "-s", "other", "exec sleep 600")
	pane, log := open("boss-a")
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	for _, text := range []string{"Deploy the fix", "Write the notes"} {
		if _, err := dev(boss, "todo", "add", text); err != nil {
			t.Fatal(err)
		}
	}

	refused(boss, "refused in a Claude Code session", "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	refused(user, "--question <the item's text> is needed", "todo", "start", "boss-a", "1")
	refused(user, `is "Deploy the fix" now, not "Other"`, "todo", "start", "boss-a", "1", "--question", "Other")
	out, err := dev(user, "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	check(t, "start", out, "#1 sent to boss-a\n")
	if err != nil {
		t.Fatal(err)
	}
	logged(log, `- you were asked to start #1 "Deploy the fix": while it still reads so and is marked started on your list, start a worker on it and link it with dev todo add --replaces 1 --on <worker>`)
	if j := json(); !strings.Contains(j, `"started":{"at":`) || !strings.Contains(j, `"status":"starting","status_on":"boss-a"`) {
		t.Errorf("❌ --all --json after start:\n%s", j)
	}
	refused(user, "item 1: already sent to boss-a 0m ago", "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	if _, err := dev(boss, "todo", "add", "--replaces", "1", "--on", "me"); err != nil {
		t.Fatal(err)
	}
	if j := json(); strings.Contains(j, `"started"`) || strings.Contains(j, `"starting"`) {
		t.Errorf("❌ --replaces kept the start:\n%s", j)
	}

	// Closed: refused with no manager, then sent to the manager, naming the boss
	s.T("kill-session", "-t", "=boss-a")
	refused(user, "boss-a is closed and no manager takes it (no session manager): dev boss a reopens it", "todo", "start", "boss-a", "2", "--question", "Write the notes")
	_, mlog := open("manager")
	out, err = dev(boss[1:], "todo", "start", "boss-a", "2", "--question", "Write the notes", "--from-board")
	check(t, "start a closed boss's item", out, "#2 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	logged(mlog, `- you were asked to start boss-a #2 "Write the notes": boss-a is closed: reopen it (dev boss a --background), its start list asks it to start #2`)
	if j := json(); !strings.Contains(j, `"status":"starting","status_on":"manager"`) {
		t.Errorf("❌ --all --json after a closed boss's start:\n%s", j)
	}
}
