//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

// The manager adopts a boss the user opened (dev boss <project> --background --adopt) only when no live session
// started it: from then its news goes up and it is in the manager's --mine list. dev agent close closes a boss of
// the manager's only when it is idle, with no workers and no open asks left, and keeps its list.
func TestManagerAdoptClose(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stateDir := filepath.Join(d.tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // inbox.Here runs in this process
	env := d.scriptEnv(writeProjects(t, [2]string{"hq", tempDir(t)}, [2]string{"app", tempDir(t)},
		[2]string{"web", tempDir(t)}, [2]string{"old", tempDir(t)}, [2]string{"api", tempDir(t)}), "XDG_STATE_HOME="+stateDir)
	dev := func(pane, stdin string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = append(env, "TMUX_PANE="+pane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	ok := func(pane string, argv ...string) string {
		t.Helper()
		out, err := dev(pane, "", argv...)
		if err != nil {
			t.Fatalf("❌ dev %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		return out
	}
	refused := func(pane, want string, argv ...string) {
		t.Helper()
		if out, err := dev(pane, "", argv...); err == nil || !strings.Contains(out, want) {
			t.Errorf("❌ dev %s = %v, %q; want an error with %q", strings.Join(argv, " "), err, out, want)
		}
	}
	state := func(pane, st, event string) {
		t.Helper()
		if out, err := dev(pane, event, "state", st); err != nil {
			t.Fatalf("❌ dev state %s: %v\n%s", st, err, out)
		}
	}
	idle, working := `{"hook_event_name":"Stop"}`, `{"hook_event_name":"UserPromptSubmit"}`
	session := func(name, startedBy string, herd bool) string {
		t.Helper()
		d.T("new-session", "-d", "-s", name, "sleep 600")
		if herd {
			d.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		}
		if startedBy != "" {
			d.T("set-option", "-t", "="+name+":", "@dev_started_by", startedBy)
		}
		pane := d.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		state(pane, "idle", idle)
		return pane
	}
	startedBy := func(name string) string {
		return d.T("show-option", "-qv", "-t", "="+name+":", "@dev_started_by")
	}

	manager := session("manager", "", true)
	state(manager, "working", working) // busy: its notes wait in its inbox
	hq := session("boss-hq", "", true)
	app := session("boss-app", "", true)
	session("boss-web", "boss-hq", true)
	session("boss-old", "boss-gone", true)
	ok(app, "todo", "add", "Before")
	box, found, err := inbox.Here(d.tmux, manager)
	if !found || err != nil {
		t.Fatalf("❌ no manager inbox: %v", err)
	}
	take := func() string {
		t.Helper()
		b, err := box.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	check(t, "nothing up before the adopt", take(), "")

	refused(manager, "--adopt needs --background", "boss", "app", "--adopt")
	refused(hq, "--adopt runs only in the manager's pane", "boss", "app", "--background", "--adopt")
	refused(manager, "boss-web was started by boss-hq: only a boss with no parent is adopted", "boss", "web", "--background", "--adopt")
	refused(manager, "boss-api isn't open: nothing to adopt", "boss", "api", "--background", "--adopt")
	if _, err := d.Try("has-session", "-t", "=boss-api:"); err == nil {
		t.Error("❌ --adopt started boss-api")
	}
	check(t, "web stays hq's", startedBy("boss-web"), "boss-hq")
	check(t, "adopt", ok(manager, "boss", "app", "--background", "--adopt"), "boss-app\n")
	check(t, "adopted", startedBy("boss-app"), "manager")
	check(t, "adopt again", ok(manager, "boss", "app", "--background", "--adopt"), "boss-app\n")
	check(t, "parent gone", ok(manager, "boss", "old", "--background", "--adopt"), "boss-old\n")
	check(t, "old adopted", startedBy("boss-old"), "manager")
	mine := ok(manager, "agent", "list", "--mine")
	if !strings.Contains(mine, "boss-app") || !strings.Contains(mine, "boss-old") || strings.Contains(mine, "boss-web") {
		t.Errorf("❌ manager's --mine:\n%s", mine)
	}

	ok(app, "todo", "add", "Pick a name")
	if got := take(); !strings.Contains(got, "boss-app waits on you: #2 Pick a name") || strings.Contains(got, "Before") {
		t.Errorf("❌ manager's batch:\n%s", got)
	}

	refused(hq, "boss-app is a boss: only the manager closes a boss", "agent", "close", "boss-app")
	refused(manager, `boss-web wasn't started or adopted by this session (manager), but by "boss-hq"`, "agent", "close", "boss-web")
	session("app-w", "boss-app", false)
	refused(manager, "boss-app still has workers (app-w)", "agent", "close", "boss-app")
	d.T("kill-session", "-t", "=app-w:")
	ok(app, "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait")
	refused(manager, "boss-app has open asks on its list (#3)", "agent", "close", "boss-app")
	ok(app, "todo", "done", "3")
	state(app, "working", working)
	refused(manager, "boss-app is working", "agent", "close", "boss-app")
	if _, err := d.Try("has-session", "-t", "=boss-app:"); err != nil {
		t.Fatal("❌ a refused close killed boss-app")
	}
	state(app, "idle", idle)

	check(t, "close", ok(manager, "agent", "close", "boss-app"), "Closed boss-app; its to do list stays\n")
	if _, err := d.Try("has-session", "-t", "=boss-app:"); err == nil {
		t.Error("❌ boss-app still there")
	}
	// Reopened under its name, it gets its list back
	again := session("boss-app", "", true)
	if list := ok(again, "todo"); !strings.Contains(list, "Before") || !strings.Contains(list, "Pick a name") {
		t.Errorf("❌ boss-app's list after the close:\n%s", list)
	}
}
