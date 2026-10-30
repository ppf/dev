//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSaveSameSecond: save.sh names a snapshot by its second, so a save in the second of an earlier one rewrites
// its file and, finding it identical to last (itself), deletes it. dev's Save hook, which every save runs, unlinks
// that last first, so save.sh links it again: a continuum-style save (save.sh run by the server) in the second of
// a dev snapshot, its hook waiting for the snapshot's lock, and two plain saves in one second, leave last valid.
func TestSaveSameSecond(t *testing.T) {
	save := filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts/save.sh")
	requireFiles(t, save)
	d := actServer(t, "a", "b", "c")
	// save.sh's date gives the second in stamp, so saves share a second whatever the clock does
	stamp := filepath.Join(d.tmp, "stamp")
	writeStubs(t, filepath.Join(d.tmp, "stubs"),
		`[ "$1" = +%Y%m%dT%H%M%S ] && exec cat `+shellQuote(stamp)+`; exec /bin/date "$@"`, "date")
	setStamp := func(s string) { writeFile(t, stamp, s+"\n") }
	setStamp("20260101T000000")
	// Each save's snapshot file, logged by its hook. A snapshot's hook (it holds the lock) lingers after Save, so
	// another save's hook comes in before the snapshot's save.sh links last
	log := filepath.Join(d.tmp, "saves.log")
	d.T("set-option", "-g", "@resurrect-hook-post-save-layout", `sh -c 'echo "$1" >> `+shellQuote(log)+
		`; `+shellQuote(devBin)+` resurrect save "$1"; [ -z "$DEV_SNAPSHOT" ] || sleep 2' sh`)
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
		// Overlapping dumps would repeat lines: the later save writes after the earlier one's
		data, _ = os.ReadFile(filepath.Join(d.tmp, "resurrect", last))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if seen := slices.Compact(slices.Sorted(slices.Values(lines))); len(seen) != len(lines) {
			t.Errorf("%s: repeated lines in %s:\n%s", name, last, data)
		}
		check(t, name+": no warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")
	}

	// The plain save starts once the snapshot's file is written (its hook logged)
	d.T("kill-session", "-t", "=a:")
	setStamp("20260101T000001")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	d.T("run-shell", "-b", snapshot)
	waitFor(t, "snapshot's file written", func() bool { _, err := os.Stat(log); return err == nil })
	d.T("run-shell", save+" quiet")
	waitFor(t, "snapshot done", func() bool { return exec.Command("pgrep", "-f", snapshot).Run() != nil })
	check(t, "a save in a dev snapshot's second: saved", d.saved(), "b boot c")
	sameFile("a save in a dev snapshot's second")

	d.T("kill-session", "-t", "=b:")
	setStamp("20260101T000002")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	d.T("run-shell", save+" quiet", ";", "run-shell", save+" quiet")
	check(t, "two plain saves in one second: saved", d.saved(), "boot c")
	sameFile("two plain saves in one second")
}
