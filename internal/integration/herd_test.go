//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
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

	// Inside tmux without a terminal, no project: the manager started detached in its own folder, on opus ·
	// medium, and how to reach it printed; again, only printed
	xdg := writeProjects(t, [2]string{"hq", tempDir(t)}, [2]string{"app", tempDir(t)}, [2]string{"web", tempDir(t)})
	env := d.scriptEnv(xdg)
	clients := d.T("list-clients", "-F", "#{client_session}")
	for _, call := range []string{"first", "second"} {
		bare := exec.Command(devBin, "boss")
		bare.Env = env
		out, err := bare.Output()
		check(t, call+" bare boss without a terminal", errStatus(err)+" "+string(out), "0 manager running: dev goto manager\n")
	}
	check(t, "client stays", d.T("list-clients", "-F", "#{client_session}"), clients)
	check(t, "manager made", d.T("list-sessions", "-F", "#{session_name}"), "boot\nmanager")
	// Its defaults apply to a new manager only: flags given are checked against what it was started with
	for _, tt := range [][2]string{
		{"--effort high", "1 ❌ session manager was started with --effort medium, not high: --model and --effort only apply to a new session\n"},
		{"--model sonnet", "1 ❌ session manager was started with --model opus, not sonnet: --model and --effort only apply to a new session\n"},
		{"--model opus --effort medium", "0 manager running: dev goto manager\n"},
	} {
		flagged := exec.Command(devBin, append([]string{"boss"}, strings.Fields(tt[0])...)...)
		flagged.Env = env
		out, err := flagged.CombinedOutput()
		check(t, "bare boss "+tt[0], errStatus(err)+" "+string(out), tt[1])
	}
	check(t, "manager is a boss", d.T("show-option", "-qv", "-t", "=manager:", "@dev_herd")+"|"+
		d.T("show-option", "-qv", "-t", "=manager:", "@project")+"|"+d.T("show-option", "-qv", "-t", "=manager:", "@dev_started_by"), "1|manager|")
	managerDir, err := filepath.EvalSymlinks(filepath.Join(strings.TrimPrefix(xdg, "XDG_CONFIG_HOME="), "dev", "manager"))
	if err != nil {
		t.Fatal(err)
	}
	managerPanes := strings.Split(d.T("list-panes", "-t", "=manager:1", "-F", "#{pane_current_path} #{pane_start_command}"), "\n")
	if len(managerPanes) != 2 || !strings.HasPrefix(managerPanes[0], managerDir+" ") ||
		!strings.Contains(managerPanes[0], `'claude' '--model' 'opus' '--effort' 'medium' '--settings' '{\"disableClaudeAiConnectors\":true,\"hooks\":`) ||
		!strings.Contains(managerPanes[0], `\"matcher\":\"compact\"}]}}' '--autocompact' '300000' '--append-system-prompt' 'You are the manager`) ||
		!strings.HasSuffix(managerPanes[0], `'--remote-control=manager'"`) || !strings.Contains(managerPanes[1], "boss --previews") {
		t.Fatalf("manager panes: %q", managerPanes)
	}
	// Typed in the user's pane, as the printed line says
	gotoManager := exec.Command(devBin, "goto", "manager")
	gotoManager.Env = append(env, "TMUX_PANE="+d.T("display", "-t", "=boot:", "-p", "#{pane_id}"))
	if out, err := gotoManager.CombinedOutput(); err != nil {
		t.Fatalf("dev goto manager: %v: %s", err, out)
	}
	check(t, "client on the manager", d.T("list-clients", "-F", "#{client_session}"), "manager")
	waitFor(t, "manager's sidebar", func() bool { return d.T("show-option", "-qv", "-t", "=manager:", "@sidebar_pane") != "" })
	d.T("switch-client", "-t", "=boot:")
	missing := exec.Command(devBin, "goto", "nope")
	missing.Env = env
	out, err := missing.CombinedOutput()
	if !strings.HasPrefix(errStatus(err)+" "+string(out), "1 ❌ no session nope: ") {
		t.Errorf("goto nope: %s %s", errStatus(err), out)
	}
	// With a terminal inside tmux (typed in boot's pane): the existing manager attached, no defaults checked
	d.T("send-keys", "-t", "=boot:", "env "+tmux.ShellJoin(xdg, devBin, "boss"), "Enter")
	waitFor(t, "client on the manager from a terminal", func() bool {
		c, _ := d.Try("list-clients", "-F", "#{client_session}")
		return c == "manager"
	})
	// A boss the manager starts is its card
	roles := map[string]string{}
	for _, l := range strings.Split(d.T("list-panes", "-t", "=manager:1", "-F", "#{@dev_boss_pane} #{pane_id}"), "\n") {
		if role, id, ok := strings.Cut(l, " "); ok {
			roles[role] = id
		}
	}
	web := exec.Command(devBin, "boss", "web", "--background", "--timeout", "30s")
	web.Env = append(env, "TMUX_PANE="+roles["coordinator"])
	out, err = web.CombinedOutput()
	check(t, "boss from the manager", errStatus(err)+" "+string(out), "0 boss-web\n")
	check(t, "started by the manager", d.T("show-option", "-qv", "-t", "=boss-web:", "@dev_started_by"), "manager")
	waitFor(t, "boss-web card in the manager's previews", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", roles["previews"]), "boss-web")
	})
	d.T("switch-client", "-t", "=boot:")
	d.T("kill-session", "-t", "=boss-web:")
	d.T("kill-session", "-t", "=manager:")
	// manager_autocompact from projects.toml: under Claude Code's minimum refused, 0 starts it with no --autocompact
	projects := filepath.Join(strings.TrimPrefix(xdg, "XDG_CONFIG_HOME="), "dev", "projects.toml")
	listed, err := os.ReadFile(projects)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range [][2]string{
		{"300", "1 ❌ config: " + projects + ": manager_autocompact = 300: 0 (Claude Code's own window) or at least 100000 tokens\n"},
		{"0", "0 manager running: dev goto manager\n"},
	} {
		writeFile(t, projects, "manager_autocompact = "+tt[0]+"\n\n"+string(listed))
		bare := exec.Command(devBin, "boss")
		bare.Env = env
		out, err := bare.CombinedOutput()
		check(t, "manager_autocompact = "+tt[0], errStatus(err)+" "+string(out), tt[1])
	}
	if start := d.T("display", "-t", "=manager:1.1", "-p", "#{pane_start_command}"); strings.Contains(start, "--autocompact") || !strings.Contains(start, "You are the manager") {
		t.Errorf("manager with manager_autocompact = 0: %q", start)
	}
	d.T("kill-session", "-t", "=manager:")

	herd := exec.Command(devBin, "boss", "hq")
	herd.Env = env
	if out, err := herd.CombinedOutput(); err != nil {
		t.Fatalf("dev boss: %v: %s", err, out)
	}
	check(t, "client switched", d.T("list-clients", "-F", "#{client_session}"), "boss-hq")
	check(t, "herd option", d.T("show-option", "-qv", "-t", "=boss-hq:", "@dev_herd"), "1")
	// Left to right: its sidebar, the coordinator, the previews
	panes := strings.Split(d.T("list-panes", "-t", "=boss-hq:1", "-F", "#{pane_id} #{pane_start_command}"), "\n")
	if len(panes) != 3 || !strings.Contains(panes[0], "'sidebar'") || !strings.Contains(panes[1], `-l -i -c "exec `) || !strings.Contains(panes[1], `'pty-wrap' '--' 'claude' '--settings' '{\"hooks\":`) ||
		!strings.Contains(panes[1], `\"matcher\":\"compact\"}]}}' '--append-system-prompt'`) ||
		!strings.HasSuffix(panes[1], `'--remote-control=boss-hq'"`) || strings.Contains(panes[1], "--autocompact") ||
		!strings.Contains(panes[2], "boss --previews") {
		t.Fatalf("boss panes: %q", panes)
	}
	sidebarPane, coordinator, previews := strings.Fields(panes[0])[0], strings.Fields(panes[1])[0], strings.Fields(panes[2])[0]
	check(t, "boss sidebar recorded", d.T("show-option", "-qv", "-t", "=boss-hq:", "@sidebar_pane"), sidebarPane)
	check(t, "coordinator focused", d.T("display", "-t", "=boss-hq:", "-p", "#{pane_id}"), coordinator)

	again := exec.Command(devBin, "herd", "hq")
	again.Env = env
	if out, err := again.CombinedOutput(); err != nil {
		t.Fatalf("dev herd again: %v: %s", err, out)
	}
	check(t, "reused", d.T("list-sessions", "-F", "#{session_name}"), "boot\nboss-hq")

	_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", "app", "--timeout", "30s")
	check(t, "start from the coordinator", status+errOut, "0")
	check(t, "started by the boss, by name", d.T("show-option", "-qv", "-t", "=app:", "@dev_started_by"), "boss-hq")
	if args := agentArgs(t, d, "=app:1.2"); !strings.Contains(args, `pty-wrap --close-on-exit -- claude --settings {"remoteControlAtStartup":false}`) || strings.Contains(args, "--autocompact") {
		t.Errorf("worker's claude not started without Remote Control and the manager's window: %q", args)
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
	_, errOut, status = devAgent(env, "", "start", "web", "--boss", "boss-hq", "--timeout", "30s")
	check(t, "start --boss outside tmux", status+errOut, "0")
	check(t, "started by the named boss", d.T("show-option", "-qv", "-t", "=web:", "@dev_started_by"), "boss-hq")
	check(t, "marked started outside tmux", d.T("show-option", "-qv", "-t", "=web:", "@dev_started_outside")+
		"|"+d.T("show-option", "-qv", "-t", "=app:", "@dev_started_outside"), "1|")
	// No tty here: sized like the boss's window, not 80x24
	size := "#{window_width}x#{window_height}"
	if got, want := d.T("display", "-t", "=web:1", "-p", size), d.T("display", "-t", "=boss-hq:1", "-p", size); got != want || got == "80x24" {
		t.Errorf("--boss worker's window %s, want the boss's %s", got, want)
	}
	_, errOut, status = devAgent(append(env, "TMUX_PANE="+coordinator), "", "close", "web")
	check(t, "boss's own coordinator can't close a --boss worker", status+" "+errOut,
		"1 ❌ web was started by a coordinator outside tmux (--boss): leave it to that coordinator\n")
	_, errOut, status = devAgent(append(env, "TMUX_PANE="+coordinator), "", "close", "web", "--boss", "boss-hq")
	check(t, "not even with --boss", status+" "+errOut, "1 ❌ web was started by a coordinator outside tmux (--boss): leave it to that coordinator\n")
	if args := agentArgs(t, d, "=web:1.2"); !strings.Contains(args, `pty-wrap --close-on-exit -- claude --settings {"remoteControlAtStartup":false}`) {
		t.Errorf("--boss worker's claude not started without Remote Control: %q", args)
	}
	mine, errOut, status := devAgent(env, "", "list", "--mine", "--boss", "boss-hq")
	var sessions []string
	for _, l := range strings.Split(strings.TrimSpace(mine), "\n") {
		sessions = append(sessions, strings.Fields(l + " -")[0])
	}
	check(t, "--mine --boss outside tmux", status+errOut+" "+strings.Join(sessions, " "), "0 app web")

	waitFor(t, "app and web cards", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "▸ ● app  finished") && strings.Contains(screen, "● web")
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
	if _, err := d.Try("has-session", "-t", "=boss-hq:"); err != nil {
		t.Fatal("❌ closing the boss's sidebar ended the boss session")
	}

	d.T("switch-client", "-t", "=boss-hq:")
	d.T("send-keys", "-t", previews, "C-c")
	waitFor(t, "boss gone with its previews pane", func() bool {
		_, err := d.Try("has-session", "-t", "=boss-hq:")
		return err != nil
	})
	// Like ×: to the first other session in the sidebar's order
	check(t, "client on the first other session", d.T("list-clients", "-F", "#{client_session}"), "boot")

	// Reopened, the boss has a new session id but the same name: its workers are its own again
	reopen := exec.Command(devBin, "boss", "hq")
	reopen.Env = env
	if out, err := reopen.CombinedOutput(); err != nil {
		t.Fatalf("dev boss reopened: %v: %s", err, out)
	}
	panes = strings.Split(d.T("list-panes", "-t", "=boss-hq:1", "-F", "#{pane_id}"), "\n")
	if len(panes) != 3 {
		t.Fatalf("reopened boss panes: %q", panes)
	}
	mine, errOut, status = devAgent(append(env, "TMUX_PANE="+panes[1]), "", "list", "--mine")
	check(t, "--mine in the reopened boss", status+errOut+" "+strings.Fields(mine + " -")[0], "0 app")
	waitFor(t, "app nested under the reopened boss in its sidebar", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", panes[0]), "└ ● app")
	})
	d.T("kill-session", "-t", "=boss-hq:")

	// A session named like a herd that isn't one is refused
	d.T("new-session", "-d", "-s", "boss-hq")
	refused := exec.Command(devBin, "herd", "hq")
	refused.Env = env
	out, err = refused.CombinedOutput()
	check(t, "foreign boss refused", errStatus(err)+" "+string(out), "1 ❌ session \"boss-hq\" exists and isn't a boss session (nor is a boss a restore brought back plain): kill or rename it\n")
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
	env := d.scriptEnv(writeProjects(t, [2]string{"hq", tempDir(t)}, [2]string{"app", tempDir(t)}, [2]string{"web", tempDir(t)}))
	boss := exec.Command(devBin, "boss", "hq")
	boss.Env = env
	if out, err := boss.CombinedOutput(); err != nil {
		t.Fatalf("dev boss: %v: %s", err, out)
	}
	panes := strings.Fields(d.T("list-panes", "-t", "=boss-hq:1", "-F", "#{pane_id}"))
	if len(panes) != 3 {
		t.Fatalf("boss panes: %q", panes)
	}
	coordinator, previews := panes[1], panes[2]
	for _, worker := range []string{"app", "web"} {
		_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", worker, "--timeout", "30s")
		check(t, "start "+worker, status+errOut, "0")
	}
	waitFor(t, "app and web cards", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "● app") && strings.Contains(screen, "● web")
	})
	d.T("run-shell", devBin+" resurrect snapshot")
	check(t, "saved", d.saved(), "app boot boss-hq web")

	for _, key := range []string{"j", "x"} {
		d.T("send-keys", "-t", previews, key)
	}
	waitScreen(t, d.server, previews, "Kill web? y/n")
	d.T("send-keys", "-t", previews, "y")
	actWait(d, "x, y on web: saved without it", d.saved, "app boot boss-hq")
	if _, err := os.Stat(filepath.Join(d.tmp, "state/dev/archive/web/session.json")); err != nil {
		t.Errorf("x, y on web: no archive record: %v", err)
	}

	d.kill()
	d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	check(t, "restored without web", strings.ReplaceAll(d.T("list-sessions", "-F", "#{session_name}"), "\n", " "), "app boot boss-hq")
}
