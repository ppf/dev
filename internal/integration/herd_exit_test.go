//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The coordinator exiting ends the boss session; its client moves to the session it came from (else to
// another) rather than detaching, unless the boss was the last session.
func TestHerdExit(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	env := d.scriptEnv(writeProjects(t, [2]string{"app", tempDir(t)}))
	boss := func() string {
		cmd := exec.Command(devBin, "boss")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dev boss: %v: %s", err, out)
		}
		check(t, "client on boss", d.T("list-clients", "-F", "#{client_session}"), "boss")
		return d.T("list-panes", "-t", "=boss:1", "-F", "#{pane_id}", "-f", "#{m:*pty-wrap*,#{pane_start_command}}")
	}
	// A session started after the client left boot (like a worker) doesn't take the client
	coordinator := boss()
	d.T("new-session", "-d", "-s", "worker")
	// The stub exits on EOF, like Claude on /exit
	d.T("send-keys", "-t", coordinator, "C-d")
	waitFor(t, "boss gone", func() bool {
		_, err := d.Try("has-session", "-t", "=boss:")
		return err != nil
	})
	check(t, "client back on boot", d.T("list-clients", "-F", "#{client_session}"), "boot")

	// With the session it came from gone, the client goes to another one
	coordinator = boss()
	d.T("kill-session", "-t", "=boot:")
	d.T("send-keys", "-t", coordinator, "C-d")
	waitFor(t, "boss gone again", func() bool {
		_, err := d.Try("has-session", "-t", "=boss:")
		return err != nil
	})
	check(t, "client on worker", d.T("list-clients", "-F", "#{client_session}"), "worker")

	// The last session going takes the client with it, as before
	coordinator = boss()
	d.T("kill-session", "-t", "=worker:")
	d.T("send-keys", "-t", coordinator, "C-d")
	waitFor(t, "server gone with the last session", func() bool {
		_, err := d.Try("list-sessions")
		return err != nil
	})
}
