//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
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

	out, errOut, status := run(env, "boss", "hq", "--background", "--timeout", "30s")
	check(t, "new boss", status+" "+out+errOut, "0 boss-hq\n")
	check(t, "client stays", d.T("list-clients", "-F", "#{client_session}"), clients)
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
