//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The coordinator's exit closes the boss for good, as × does: its client moves to another session (detaching
// only from the last one) and an archive record keeps its conversation.
func TestHerdExit(t *testing.T) {
	requireFiles(t, filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts/save.sh"))
	d := newDevServer(t, "set -g @dev_notify off\n")
	d.T("set-option", "-g", "@resurrect-hook-post-save-layout", devBin+" resurrect save")
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
	// A coordinator killed by a signal (a shutdown) leaves the boss for the restore
	coordinator := boss()
	pid, err := strconv.Atoi(d.T("display", "-t", coordinator, "-p", "#{pane_pid}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "coordinator dead", func() bool { return d.T("display", "-t", coordinator, "-p", "#{pane_dead}") == "1" })
	time.Sleep(time.Second)
	check(t, "signal: boss stays", d.T("list-sessions", "-F", "#{session_name}"), "boot\nboss")
	d.T("kill-session", "-t", "=boss:")

	// The client goes to the sidebar's first other session (boot), not the newest one (worker)
	coordinator = boss()
	d.T("new-session", "-d", "-s", "worker")
	// The stub exits on EOF, like Claude on /exit
	d.T("send-keys", "-t", coordinator, "C-d")
	waitFor(t, "boss gone", func() bool {
		_, err := d.Try("has-session", "-t", "=boss:")
		return err != nil
	})
	check(t, "client back on boot", d.T("list-clients", "-F", "#{client_session}"), "boot")
	actWait(d, "saved without boss", d.saved, "boot worker")
	data, err := os.ReadFile(filepath.Join(d.tmp, "state/dev/archive/boss/session.json"))
	if err != nil || !strings.Contains(string(data), `"conversation": "s0"`) {
		t.Errorf("archive record: %v %s", err, data)
	}

	// With boot gone, the client goes to the next session in the sidebar's order
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
	// The last session's exit must still save, or the next start resumes the boss
	check(t, "saved without sessions", d.saved(), "")
}
