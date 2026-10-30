//go:build integration

package integration

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCloseForgets: × closes a session for good, so a cold-start restore leaves it out: `dev resurrect
// snapshot` saves once it is gone, after the sidebar's own session and after the last one too, whose kill
// holds the server up for that save. Closing the terminal tab (the client detaching) and a shutdown
// (kill-server) keep every session.
func TestCloseForgets(t *testing.T) {
	scripts := filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts")
	save, restore := filepath.Join(scripts, "save.sh"), filepath.Join(scripts, "restore.sh")
	requireFiles(t, save, restore)
	d := actServer(t, "a", "b", "c", "d", "e", "f")
	hook := func() { d.T("set-option", "-g", "@resurrect-hook-post-save-layout", devBin+" resurrect save") }
	hook()
	saved := d.saved
	sessions := func() string {
		out, err := d.Try("list-sessions", "-F", "#{session_name}")
		if err != nil {
			return "no server"
		}
		return strings.ReplaceAll(out, "\n", " ")
	}
	coldStart := func() {
		t.Helper()
		// What `dev` runs first on a new server (open.RestoreOnce)
		d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	}
	// Through dev's lock, like every save here: a plain save.sh in the second of a close's snapshot would delete last
	snapshot := devBin + " resurrect snapshot"
	d.T("run-shell", snapshot)
	check(t, "saved", saved(), "a b boot c d e f")

	actSwitch(d, "a")
	sidebar := actEnsure(d, "a")
	for _, s := range []string{"b", "c"} {
		actSelect(d, sidebar, s)
		d.T("send-keys", "-t", sidebar, "x")
	}
	actWait(d, "x on b, then c: sessions", sessions, "a boot d e f")
	actWait(d, "x on b, then c: saved without them", saved, "a boot d e f")

	// Two closes at once: their snapshots take turns, each in a second of its own, as the second save in
	// the second of the first's would find its file identical to last's (the same file) and delete it
	d.T("kill-session", "-t", "=e:", ";", "run-shell", "-b", snapshot, ";", "kill-session", "-t", "=f:", ";", "run-shell", "-b", snapshot)
	// Not polling saved(): the first save shows the right sessions until a second one in its second deletes it
	waitFor(t, "both snapshots done", func() bool { return exec.Command("pgrep", "-f", snapshot).Run() != nil })
	check(t, "two closes at once: saved without them", saved(), "a boot d")

	actSelect(d, sidebar, "a")
	d.T("send-keys", "-t", sidebar, "x")
	actWait(d, "x on the sidebar's own session: client moved", func() string { return actClientSession(d) }, "boot")
	actWait(d, "x on the sidebar's own session: saved without it", saved, "boot d")

	// The terminal tab closes: its client goes, the sessions stay
	d.outer.T("kill-session", "-t", "=out:")
	waitFor(t, "client gone", func() bool { c, _ := d.Try("list-clients"); return c == "" })
	time.Sleep(2 * time.Second)
	check(t, "detached: sessions", sessions(), "boot d")
	d.T("run-shell", snapshot)
	check(t, "detached: saved", saved(), "boot d")

	d.kill()
	check(t, "kill-server: saved", saved(), "boot d")
	coldStart()
	actWait(d, "restored after kill-server", sessions, "boot d")

	hook()
	d.T("kill-session", "-t", "=boot:")
	own := actEnsure(d, "d")
	actSelect(d, own, "d")
	d.T("send-keys", "-t", own, "x")
	actWait(d, "x on the last session: server gone", sessions, "no server")
	check(t, "x on the last session: saved without sessions", saved(), "")
	coldStart()
	check(t, "nothing restored", sessions(), "no server")
}

// saved is the sessions in d's last tmux-resurrect snapshot, sorted, or why it can't be read.
func (d *devServer) saved() string {
	data, err := os.ReadFile(filepath.Join(d.tmp, "resurrect/last"))
	if err != nil {
		return err.Error()
	}
	sessions := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Split(line, "\t"); f[0] == "pane" {
			sessions[f[1]] = true
		}
	}
	return strings.Join(slices.Sorted(maps.Keys(sessions)), " ")
}
