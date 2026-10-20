//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dev herd opens the coordinator and previews panes; a worker the coordinator starts is marked as its
// own, shows as a tile with its screen, and Enter on the previews switches the client to it.
func TestHerd(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	env := d.scriptEnv(writeProjects(t, [2]string{"app", tempDir(t)}))

	herd := exec.Command(devBin, "boss")
	herd.Env = env
	if out, err := herd.CombinedOutput(); err != nil {
		t.Fatalf("dev boss: %v: %s", err, out)
	}
	check(t, "client switched", d.T("list-clients", "-F", "#{client_session}"), "boss")
	check(t, "herd option", d.T("show-option", "-qv", "-t", "=boss:", "@dev_herd"), "1")
	// Left to right: its sidebar, the coordinator, the previews
	panes := strings.Split(d.T("list-panes", "-t", "=boss:1", "-F", "#{pane_id} #{pane_start_command}"), "\n")
	if len(panes) != 3 || !strings.Contains(panes[0], "'sidebar'") || !strings.Contains(panes[1], "pty-wrap -- claude --append-system-prompt") ||
		!strings.HasSuffix(panes[1], "--remote-control=boss") ||
		!strings.Contains(panes[2], "boss --previews") {
		t.Fatalf("boss panes: %q", panes)
	}
	sidebarPane, coordinator, previews := strings.Fields(panes[0])[0], strings.Fields(panes[1])[0], strings.Fields(panes[2])[0]
	check(t, "boss sidebar recorded", d.T("show-option", "-qv", "-t", "=boss:", "@sidebar_pane"), sidebarPane)
	check(t, "coordinator focused", d.T("display", "-t", "=boss:", "-p", "#{pane_id}"), coordinator)

	again := exec.Command(devBin, "herd")
	again.Env = env
	if out, err := again.CombinedOutput(); err != nil {
		t.Fatalf("dev herd again: %v: %s", err, out)
	}
	check(t, "reused", d.T("list-sessions", "-F", "#{session_name}"), "boot\nboss")

	_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", "app", "--timeout", "30s")
	check(t, "start from the coordinator", status+errOut, "0")
	check(t, "started by the boss, by name", d.T("show-option", "-qv", "-t", "=app:", "@dev_started_by"), "boss")
	_, errOut, status = devAgent(env, "", "prompt", "app", "--wait", "--timeout", "30s", "hello")
	check(t, "prompt", status+errOut, "0")

	waitFor(t, "app tile with its screen", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "● app Claude · idle") && strings.Contains(screen, "got: hello")
	})
	screen := d.T("capture-pane", "-p", "-t", previews)
	if strings.Contains(screen, "● herd") {
		t.Errorf("the herd session shows as a tile:\n%s", screen)
	}
	d.T("send-keys", "-t", previews, "Enter")
	waitFor(t, "client on app", func() bool {
		c, _ := d.Try("list-clients", "-F", "#{client_session}")
		return c == "app"
	})

	// Hiding the boss's sidebar leaves the boss alone
	d.T("kill-pane", "-t", sidebarPane)
	time.Sleep(300 * time.Millisecond)
	if _, err := d.Try("has-session", "-t", "=boss:"); err != nil {
		t.Fatal("❌ closing the boss's sidebar ended the boss session")
	}

	d.T("switch-client", "-t", "=boss:")
	d.T("send-keys", "-t", previews, "C-c")
	waitFor(t, "boss gone with its previews pane", func() bool {
		_, err := d.Try("has-session", "-t", "=boss:")
		return err != nil
	})
	check(t, "client back on app", d.T("list-clients", "-F", "#{client_session}"), "app")

	// Reopened, the boss has a new session id but the same name: its workers are its own again
	reopen := exec.Command(devBin, "boss")
	reopen.Env = env
	if out, err := reopen.CombinedOutput(); err != nil {
		t.Fatalf("dev boss reopened: %v: %s", err, out)
	}
	panes = strings.Split(d.T("list-panes", "-t", "=boss:1", "-F", "#{pane_id}"), "\n")
	if len(panes) != 3 {
		t.Fatalf("reopened boss panes: %q", panes)
	}
	mine, errOut, status := devAgent(append(env, "TMUX_PANE="+panes[1]), "", "list", "--mine")
	check(t, "--mine in the reopened boss", status+errOut+" "+strings.Fields(mine + " -")[0], "0 app")
	waitFor(t, "app nested under the reopened boss in its sidebar", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", panes[0]), "└ ● app")
	})
	d.T("kill-session", "-t", "=boss:")

	// A session named like a herd that isn't one is refused
	d.T("new-session", "-d", "-s", "boss")
	refused := exec.Command(devBin, "herd")
	refused.Env = env
	out, err := refused.CombinedOutput()
	check(t, "foreign boss refused", errStatus(err)+" "+string(out), "1 ❌ session \"boss\" exists and isn't a boss session (a restored boss isn't one either): kill or rename it\n")
}
