//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The user marks their own item doing from a shell: its live boss gets "the user is on" and the item reads in
// progress, its old word yours; the boss marks its own. A closed boss's item tells the manager, or no one while none runs.
func TestTodoDoing(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(env []string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return string(out), stderr.String(), err
	}
	must := func(env []string, argv ...string) (string, string) {
		t.Helper()
		out, stderr, err := dev(env, argv...)
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, stderr)
		}
		return out, stderr
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
		out, _ := must([]string{"TMUX_PANE="}, "todo", "--all", "--json")
		return strings.Join(strings.Fields(out), "")
	}

	s.T("new-session", "-d", "-s", "other", "exec sleep 600")
	pane, log := open("boss-a")
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	must(boss, "todo", "add", "Send the cert")
	must(boss, "todo", "add", "Install the cert", "--on", "boss", "--part-of", "1")
	must(boss, "todo", "add", "Write the notes")

	if _, stderr, err := dev(boss, "todo", "doing", "boss-a", "1", "--question", "Send the cert"); err == nil || !strings.Contains(stderr, "refused in a Claude Code session") {
		t.Errorf("❌ doing in a Claude session: %v %s", err, stderr)
	}
	out, _ := must(user, "todo", "doing", "boss-a", "1", "mailing ops", "--question", "Send the cert")
	check(t, "doing", out, "#1 in progress, boss-a told\n")
	logged(log, `- the user is on #1 "Send the cert": mailing ops`)
	if j := json(); !strings.Contains(j, `"status":"yours","doing":{"text":"mailingops","at":`) || !strings.Contains(j, `"part_of":2,"column":"progress","assignee":{"kind":"you"}`) {
		t.Errorf("❌ --all --json after doing:\n%s", j)
	}
	out, _ = must(boss, "todo", "doing", "2", "drafting")
	check(t, "boss's own doing", out, "#2 in progress\n")
	if j := json(); !strings.Contains(j, `"status":"working","status_on":"boss-a","doing":{"text":"drafting","at":`) || !strings.Contains(j, `"part_of":1,"column":"progress"`) {
		t.Errorf("❌ --all --json after the boss's doing:\n%s", j)
	}

	// Closed: no manager, marked and no one told; with one, the manager is told, naming the boss
	s.T("kill-session", "-t", "=boss-a")
	out, stderr := must(user, "todo", "doing", "boss-a", "3", "--question", "Write the notes")
	check(t, "doing on a closed boss, no manager", out, "#3 in progress, no one told\n")
	if !strings.Contains(stderr, "no one told: no session manager") {
		t.Errorf("❌ stderr = %q", stderr)
	}
	_, mlog := open("manager")
	out, _ = must(user, "todo", "doing", "boss-a", "3", "--stop", "--question", "Write the notes")
	check(t, "stop on a closed boss", out, "#3 stopped, manager told\n")
	logged(mlog, `- the user is on boss-a #3 "Write the notes": no longer on it (boss-a is closed)`)
}
