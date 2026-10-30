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

// TestSaveSameSecond: save.sh names a snapshot by its second, so a save in the second of an earlier one rewrites
// its file and, finding it identical to last (itself), deletes it. dev's Save hook, which every save runs, unlinks
// that last first, so save.sh links it again: a continuum-style save (save.sh run by the server) in the second of
// a dev snapshot, and two plain saves in one second, leave last valid.
func TestSaveSameSecond(t *testing.T) {
	save := filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts/save.sh")
	requireFiles(t, save)
	d := actServer(t, "a", "b", "c")
	// Each save's snapshot file, logged by its hook: two saves in one second log the same one
	log := filepath.Join(d.tmp, "saves.log")
	d.T("set-option", "-g", "@resurrect-hook-post-save-layout",
		`sh -c 'echo "$1" >> `+shellQuote(log)+`; exec `+shellQuote(devBin)+` resurrect save "$1"' sh`)
	snapshot := devBin + " resurrect snapshot"
	d.T("run-shell", snapshot)
	check(t, "saved", d.saved(), "a b boot c")
	sameFile := func(name string) {
		t.Helper()
		data, _ := os.ReadFile(log)
		saves := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(saves) != 2 || saves[0] != saves[1] {
			t.Fatalf("%s: saves didn't share a second's file: %q", name, saves)
		}
		last, err := os.Readlink(filepath.Join(d.tmp, "resurrect/last"))
		if _, statErr := os.Stat(filepath.Join(d.tmp, "resurrect", last+".dev")); err != nil || statErr != nil {
			t.Errorf("%s: last's .dev: %v %v", name, err, statErr)
		}
		check(t, name+": no warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")
	}

	// The snapshot saves at the next second's start; the plain save follows in that second, its hook waiting
	// for the snapshot's lock if it's still saving
	d.T("kill-session", "-t", "=a:")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Truncate(time.Second).Add(time.Second)
	d.T("run-shell", "-b", snapshot)
	time.Sleep(time.Until(next.Add(100 * time.Millisecond)))
	d.T("run-shell", save+" quiet")
	waitFor(t, "snapshot done", func() bool { return exec.Command("pgrep", "-f", snapshot).Run() != nil })
	check(t, "a save in a dev snapshot's second: saved", d.saved(), "b boot c")
	sameFile("a save in a dev snapshot's second")

	d.T("kill-session", "-t", "=b:")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	d.T("run-shell", save+" quiet", ";", "run-shell", save+" quiet")
	check(t, "two plain saves in one second: saved", d.saved(), "boot c")
	sameFile("two plain saves in one second")
}
