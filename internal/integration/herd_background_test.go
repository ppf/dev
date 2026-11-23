//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// dev boss <project> --background makes the boss detached: no client moves, it is marked started by the boss
// it ran in, and its name is printed once its Claude registered; an existing boss is only printed.
func TestHerdBackground(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	env := d.scriptEnv(writeProjects(t, [2]string{"hq", tempDir(t)}, [2]string{"app", tempDir(t)}, [2]string{"web", tempDir(t)}))
	run := func(env []string, args ...string) (string, string, string) {
		cmd := exec.Command(devBin, args...)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		status := errStatus(cmd.Run())
		return stdout.String(), stderr.String(), status
	}
	clients := d.T("list-clients", "-F", "#{client_session}")
	wasRestored := d.T("show-option", "-gqv", "@dev_restored")

	out, errOut, status := run(env, "boss", "hq", "--background", "--timeout", "30s")
	check(t, "new boss", status+" "+out+errOut, "0 boss-hq\n")
	check(t, "client stays", d.T("list-clients", "-F", "#{client_session}"), clients)
	check(t, "no first-run restore", d.T("show-option", "-gqv", "@dev_restored"), wasRestored)
	check(t, "herd option", d.T("show-option", "-qv", "-t", "=boss-hq:", "@dev_herd"), "1")
	check(t, "no starter from outside a boss", d.T("show-option", "-qv", "-t", "=boss-hq:", "@dev_started_by"), "")
	if _, err := d.Try("has-session", "-t", "=boss-hq:"); err != nil {
		t.Fatalf("boss-hq missing: %v", err)
	}

	out, errOut, status = run(env, "boss", "hq", "--background")
	check(t, "existing boss", status+" "+out+errOut, "0 boss-hq\n")

	coordinator := d.T("list-panes", "-t", "=boss-hq:1", "-F", "#{pane_id} #{pane_start_command}")
	pane := ""
	for _, l := range strings.Split(coordinator, "\n") {
		if strings.Contains(l, "pty-wrap") {
			pane = strings.Fields(l)[0]
		}
	}
	out, errOut, status = run(append(env, "TMUX_PANE="+pane), "boss", "app", "--background", "--timeout", "30s")
	check(t, "from a boss", status+" "+out+errOut, "0 boss-app\n")
	check(t, "started by the calling boss", d.T("show-option", "-qv", "-t", "=boss-app:", "@dev_started_by"), "boss-hq")
	check(t, "client stays again", d.T("list-clients", "-F", "#{client_session}"), clients)

	_, errOut, status = run(env, "boss", "--background")
	check(t, "no project", status+" "+errOut, "1 ❌ --background needs a project: dev boss <project> --background\n")
}

// A boss reopened with --background for a start on its closed list gets "you were asked to start" pasted in its
// Claude without anyone prompting it: its start reminder is SessionStart context only, which takes no turn.
func TestHerdBackgroundAsksStart(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	log := filepath.Join(d.tmp, "claude.log")
	// Registers, draws an empty input box and logs what is typed into it, by pane
	writeStubs(t, stubs, `printf '{"hook_event_name":"SessionStart","source":"startup","session_id":"s1"}' | `+shellQuote(devBin)+` state idle
set -- "" `+shellQuote(log)+`
`+strings.ReplaceAll(stubBoss, `>> "$2"`, `| sed "s|^|$TMUX_PANE |" >> "$2"`), "claude")
	env := slices.Clip(d.scriptEnv(writeProjects(t, [2]string{"a", tempDir(t)})))
	user := append(env, "TMUX_PANE=", "CLAUDECODE=")
	coordinator := func() string {
		t.Helper()
		for _, l := range strings.Split(d.T("list-panes", "-t", "=boss-a:1", "-F", "#{pane_id} #{pane_start_command}"), "\n") {
			if strings.Contains(l, "pty-wrap") {
				return strings.Fields(l)[0]
			}
		}
		t.Fatal("no coordinator pane in boss-a")
		return ""
	}
	ok := func(name, want string, env []string, args ...string) {
		t.Helper()
		out, errOut, status := runDev(env, "", args...)
		check(t, name, status+" "+out+errOut, "0 "+want)
	}

	ok("new boss", "boss-a\n", env, "boss", "a", "--background", "--no-serve", "--timeout", "30s")
	boss := append(env, "TMUX_PANE="+coordinator(), "CLAUDECODE=1")
	for i, text := range []string{"Ship it", "Read the plan", "Write the notes"} {
		ok("add", fmt.Sprintf("added %d · to do %d\n", i+1, i+1), boss, "todo", "add", text, "--details", "why")
	}
	// Started while open: the old boss got the note, the reopened one must not. Ahead of #3, so a stray note pastes first
	ok("start while open", "#1 sent to boss-a\n", user, "todo", "start", "boss-a", "1", "--question", "Ship it")
	d.T("kill-session", "-t", "=boss-a")
	d.T("new-session", "-d", "-s", "manager", filepath.Join(stubs, "claude"))
	d.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	waitFor(t, "manager registered", func() bool {
		out, _, _ := runDev(env, "", "agent", "list")
		return strings.Contains(out, "manager")
	})
	ok("start", "#3 sent to manager\n", user, "todo", "start", "boss-a", "3", "--question", "Write the notes")
	closed := func() bool {
		out, _, _ := runDev(user, "", "todo", "--all", "--json")
		return strings.Contains(strings.Join(strings.Fields(out), ""), `"boss":"boss-a","closed":true`)
	}
	if !closed() {
		t.Error("❌ boss-a's list not closed before the reopen")
	}

	ok("reopen", "boss-a\n", env, "boss", "a", "--background", "--no-serve", "--timeout", "30s")
	pane := coordinator()
	want := pane + ` - you were asked to start #3 "Write the notes": while it still reads so and is marked started on your list, start a worker on it`
	waitFor(t, "reopened boss asked to start #3", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), want)
	})
	if data, _ := os.ReadFile(log); strings.Contains(string(data), pane+" - you were asked to start #1") || strings.Contains(string(data), pane+" - you were asked to start #2") {
		t.Errorf("❌ reopened boss asked about an item not started while it was closed:\n%s", data)
	}
	if closed() {
		t.Error("❌ boss-a's list still closed after the reopen")
	}
	// The start is the boss's now: a later reopen or --background doesn't send it again
	if out, _, _ := runDev(user, "", "todo", "--all", "--json"); placedAs(t, out, "boss-a", 3) != "starting boss-a progress" {
		t.Errorf("❌ #3 after the reopen = %q, want starting on boss-a", placedAs(t, out, "boss-a", 3))
	}
}
