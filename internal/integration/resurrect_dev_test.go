//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestRestore on dev: the resurrect hooks run `dev resurrect`, the sidebar comes back as a Go sidebar
// and hiding sidebars goes through `dev sidebar toggle`. Panes run stubs, so resurrect restores the agent
// pane as a bare shell.
func TestResurrectDev(t *testing.T) {
	home := tmuxHome()
	restore := filepath.Join(home, "plugins/tmux-resurrect/scripts/restore.sh")
	save := filepath.Join(home, "plugins/tmux-resurrect/scripts/save.sh")
	requireFiles(t, restore, save, filepath.Join(home, "plugins/tmux-resurrect/scripts/helpers.sh"))
	dir := tempDir(t)
	resurrectDir := tempDir(t)
	cfg := writeProjects(t, [2]string{"devt", dir})

	// Overrides follow source-file, so nothing in ~/.tmux.conf points the hooks or snapshots elsewhere.
	// Restore wants the slot's command to be basename(default-shell), and macOS's /bin/sh runs as bash.
	conf := `source-file ~/.tmux.conf
set -g default-shell /bin/bash
set -g default-command '/bin/bash --norc'
set -g @dev_restored 1
set -g @resurrect-dir ` + shellQuote(resurrectDir) + `
set -g @resurrect-processes '"~^([^ ]*/)?claude( |$)->sleep 600"'
`
	for hook, arg := range map[string]string{
		"post-save-layout": "save", "post-save-all": "prune", "pre-restore-all": "pre-restore", "post-restore-all": "restore",
	} {
		conf += "set -g @resurrect-hook-" + hook + " " + shellQuote(devBin+" resurrect "+arg) + "\n"
	}
	d := newDevServer(t, conf)
	display := func(target, format string) string {
		out, _ := d.Try("display", "-t", target, "-p", format)
		return out
	}
	sidebarOption := func() string { return d.T("show-option", "-qv", "-t", "=devt:", "@sidebar_pane") }

	if out, err := runScript(d.scriptEnv(cfg), devBin, "devt"); err != nil {
		t.Fatalf("dev devt: %v\n%s", err, out)
	}
	d.waitForStubs("devt")
	waitFor(t, "devt sidebar", func() bool { return sidebarOption() != "" })
	d.T("resize-pane", "-Z", "-t", "=devt:2.1") // a compact-mode zoom at save time
	d.T("new-session", "-d", "-s", "plain")
	d.T("run-shell", save+" quiet")
	last, _ := os.Readlink(filepath.Join(resurrectDir, "last"))
	state, _ := os.ReadFile(filepath.Join(resurrectDir, last+".dev"))
	check(t, "dev state saved next to snapshot", string(state), "devt\tdevt\t1.1\n")
	check(t, "no save warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")

	// Cold start: the command the first `dev` of a server runs
	d.kill()
	d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	sessions := strings.Split(d.T("ls", "-F", "#{session_name}"), "\n")
	sort.Strings(sessions)
	check(t, "sessions restored", strings.Join(sessions, " "), "boot devt plain")
	sidebar := sidebarOption()
	start := display(sidebar, "#{pane_start_command}")
	t.Logf("rebuilt sidebar %s: %s", sidebar, start)
	check(t, "Go sidebar rebuilt", devSidebarStart(start), "true")
	check(t, "@project restored", d.T("show-option", "-qv", "-t", "=devt:", "@project"), "devt")
	check(t, "restored zoom undone", display("=devt:2", "#{window_zoomed_flag}"), "0")
	check(t, "focus stays on agent pane", display("=devt:1", "#{pane_index}"), "2")
	check(t, "no restore warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")
	// The dir must exist (pre-restore creates it), proving the hooks used the temp state dir
	live, err := os.ReadDir(filepath.Join(d.tmp, "state/dev"))
	check(t, "live list cleaned up", dirListing(live, err), "")

	// Manual restore with sidebars hidden must not kill or change any live pane (resurrect itself may
	// add back panes that existed at save time, e.g. the hidden sidebar's slot)
	panes := func() []string {
		return strings.Split(d.T("list-panes", "-a", "-F", "#{pane_id} #{session_name} #{pane_current_command}"), "\n")
	}
	// Toggle resolves "current session" from the pane env, as when pressed in devt
	pane := display("=devt:1.2", "#{pane_id}")
	if pane == "" {
		t.Fatal("no devt:1.2 pane")
	}
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+pane), devBin, "sidebar", "toggle"); err != nil {
		t.Fatalf("dev sidebar toggle: %v\n%s", err, out)
	}
	check(t, "sidebars hidden", d.T("show", "-gqv", "@sidebar_hidden")+"/"+sidebarOption(), "1/")
	before := panes()
	d.T("run-shell", restore)
	time.Sleep(2 * time.Second)
	after := panes()
	var gone []string
	for _, p := range before {
		if !slices.Contains(after, p) {
			gone = append(gone, p)
		}
	}
	check(t, "manual restore leaves live panes alone", strings.Join(gone, "\n"), "")
	check(t, "no manual restore warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")
}
