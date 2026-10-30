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
	env := d.scriptEnv(writeProjects(t, [2]string{"app", tempDir(t)}, [2]string{"web", tempDir(t)}))

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
	if args := agentArgs(t, d, "=app:1.2"); !strings.Contains(args, `pty-wrap -- claude --settings {"remoteControlAtStartup":false}`) {
		t.Errorf("worker's claude not started without Remote Control: %q", args)
	}
	_, errOut, status = devAgent(env, "", "prompt", "app", "--wait", "--timeout", "30s", "hello")
	check(t, "prompt", status+errOut, "0")

	// From outside tmux, --boss names the boss: refused before opening anything unless it is one, its worker
	// is the same as one its coordinator started
	_, errOut, status = devAgent(env, "", "start", "web", "--boss", "boot", "--timeout", "30s")
	check(t, "start --boss of a plain session", status+" "+errOut, "1 ❌ boot is not a boss session (dev boss)\n")
	if _, err := d.Try("has-session", "-t", "=web:"); err == nil {
		t.Error("❌ refused start --boss opened web")
	}
	_, errOut, status = devAgent(env, "", "start", "web", "--boss", "boss", "--timeout", "30s")
	check(t, "start --boss outside tmux", status+errOut, "0")
	check(t, "started by the named boss", d.T("show-option", "-qv", "-t", "=web:", "@dev_started_by"), "boss")
	check(t, "marked started outside tmux", d.T("show-option", "-qv", "-t", "=web:", "@dev_started_outside")+
		"|"+d.T("show-option", "-qv", "-t", "=app:", "@dev_started_outside"), "1|")
	// No tty here: sized like the boss's window, not 80x24
	size := "#{window_width}x#{window_height}"
	if got, want := d.T("display", "-t", "=web:1", "-p", size), d.T("display", "-t", "=boss:1", "-p", size); got != want || got == "80x24" {
		t.Errorf("--boss worker's window %s, want the boss's %s", got, want)
	}
	_, errOut, status = devAgent(append(env, "TMUX_PANE="+coordinator), "", "close", "web")
	check(t, "boss's own coordinator can't close a --boss worker", status+" "+errOut,
		"1 ❌ web was started by a coordinator outside tmux (--boss): leave it to that coordinator\n")
	_, errOut, status = devAgent(append(env, "TMUX_PANE="+coordinator), "", "close", "web", "--boss", "boss")
	check(t, "not even with --boss", status+" "+errOut, "1 ❌ web was started by a coordinator outside tmux (--boss): leave it to that coordinator\n")
	if args := agentArgs(t, d, "=web:1.2"); !strings.Contains(args, `pty-wrap -- claude --settings {"remoteControlAtStartup":false}`) {
		t.Errorf("--boss worker's claude not started without Remote Control: %q", args)
	}
	mine, errOut, status := devAgent(env, "", "list", "--mine", "--boss", "boss")
	var sessions []string
	for _, l := range strings.Split(strings.TrimSpace(mine), "\n") {
		sessions = append(sessions, strings.Fields(l + " -")[0])
	}
	check(t, "--mine --boss outside tmux", status+errOut+" "+strings.Join(sessions, " "), "0 app web")

	waitFor(t, "app and web tiles", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "● app Claude · idle") && strings.Contains(screen, "got: hello") && strings.Contains(screen, "● web Claude")
	})
	waitFor(t, "web nested under the boss in its sidebar", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", sidebarPane), "└ ● web")
	})
	d.T("kill-session", "-t", "=web:")
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
	mine, errOut, status = devAgent(append(env, "TMUX_PANE="+panes[1]), "", "list", "--mine")
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
	check(t, "foreign boss refused", errStatus(err)+" "+string(out), "1 ❌ session \"boss\" exists and isn't a boss session (nor is a boss a restore brought back plain): kill or rename it\n")
}

// x, y in the previews closes a worker for good: the next snapshot leaves it out, so a restore after a
// reboot brings back the boss and its other worker, not that one.
func TestHerdKillForgets(t *testing.T) {
	scripts := filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts")
	restore := filepath.Join(scripts, "restore.sh")
	requireFiles(t, filepath.Join(scripts, "save.sh"), restore)
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	env := d.scriptEnv(writeProjects(t, [2]string{"app", tempDir(t)}, [2]string{"web", tempDir(t)}))
	boss := exec.Command(devBin, "boss")
	boss.Env = env
	if out, err := boss.CombinedOutput(); err != nil {
		t.Fatalf("dev boss: %v: %s", err, out)
	}
	panes := strings.Fields(d.T("list-panes", "-t", "=boss:1", "-F", "#{pane_id}"))
	if len(panes) != 3 {
		t.Fatalf("boss panes: %q", panes)
	}
	coordinator, previews := panes[1], panes[2]
	for _, worker := range []string{"app", "web"} {
		_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", worker, "--timeout", "30s")
		check(t, "start "+worker, status+errOut, "0")
	}
	waitFor(t, "app and web tiles", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "● app Claude") && strings.Contains(screen, "● web Claude")
	})
	d.T("run-shell", devBin+" resurrect snapshot")
	check(t, "saved", d.saved(), "app boot boss web")

	for _, key := range []string{"j", "x"} {
		d.T("send-keys", "-t", previews, key)
	}
	waitScreen(t, d.server, previews, "Kill web? y/n")
	d.T("send-keys", "-t", previews, "y")
	actWait(d, "x, y on web: saved without it", d.saved, "app boot boss")

	d.kill()
	d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	check(t, "restored without web", strings.ReplaceAll(d.T("list-sessions", "-F", "#{session_name}"), "\n", " "), "app boot boss")
}
