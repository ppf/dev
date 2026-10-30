//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// resurrectConf points tmux-resurrect's snapshots at resurrectDir and its hooks at `dev resurrect`.
// Overrides follow source-file, so nothing in ~/.tmux.conf points the hooks or snapshots elsewhere.
// Restore wants the slot's command to be basename(default-shell), and macOS's /bin/sh runs as bash.
func resurrectConf(resurrectDir string) string {
	conf := `source-file ~/.tmux.conf
set -g default-shell /bin/bash
set -g default-command '/bin/bash --norc'
set -g @dev_restored 1
set -g @resurrect-dir ` + shellQuote(resurrectDir) + `
set -g @resurrect-processes '"~^([^ ]*/)?lazygit( |$)->sleep 600"'
`
	for hook, arg := range map[string]string{
		"post-save-layout": "save", "post-save-all": "prune", "pre-restore-all": "pre-restore", "post-restore-all": "restore",
	} {
		conf += "set -g @resurrect-hook-" + hook + " " + shellQuote(devBin+" resurrect "+arg) + "\n"
	}
	return conf
}

// TestRestore on dev: the resurrect hooks run `dev resurrect`, the sidebar comes back as a Go sidebar
// and hiding sidebars goes through `dev sidebar toggle`. Resurrect doesn't restart claude, so dev types
// each agent's resume command into its restored bare shell: its own conversation while the transcript
// exists, else a fresh claude.
func TestResurrectDev(t *testing.T) {
	home := tmuxHome(t)
	restore := filepath.Join(home, "plugins/tmux-resurrect/scripts/restore.sh")
	save := filepath.Join(home, "plugins/tmux-resurrect/scripts/save.sh")
	requireFiles(t, restore, save, filepath.Join(home, "plugins/tmux-resurrect/scripts/helpers.sh"))
	dir, dirU := tempDir(t), tempDir(t)
	resurrectDir := tempDir(t)
	cfg := writeProjects(t, [2]string{"devt", dir}, [2]string{"devu", dirU})

	d := newDevServer(t, resurrectConf(resurrectDir))
	display := func(target, format string) string {
		out, _ := d.Try("display", "-t", target, "-p", format)
		return out
	}
	sidebarOption := func() string { return d.T("show-option", "-qv", "-t", "=devt:", "@sidebar_pane") }

	for _, name := range []string{"devt", "devu"} {
		if out, err := runScript(d.scriptEnv(cfg), devBin, name); err != nil {
			t.Fatalf("dev %s: %v\n%s", name, err, out)
		}
		d.waitForStubs(name)
	}
	waitFor(t, "devt sidebar", func() bool { return sidebarOption() != "" })
	// The Claude hook as the agents' first event; devu's transcript is gone by restore time
	transcripts := tempDir(t)
	hook := func(session, id string) string {
		transcript := filepath.Join(transcripts, id+".jsonl")
		writeFile(t, transcript, "{}\n")
		input := `{"session_id":"` + id + `","transcript_path":"` + transcript + `"}`
		cmd := exec.Command(devBin, "state", "idle")
		cmd.Env = d.scriptEnv("TMUX_PANE=" + display("="+session+":1.2", "#{pane_id}"))
		cmd.Stdin = strings.NewReader(input)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dev state idle in %s: %v\n%s", session, err, out)
		}
		return transcript
	}
	hook("devt", "s0")
	goneTranscript := hook("devu", "u1")
	d.T("resize-pane", "-Z", "-t", "=devt:2.1") // a compact-mode zoom at save time
	d.T("new-session", "-d", "-s", "plain")
	// Workers' bosses: devu names its boss, devt holds an older version's id of a live boss (plain)
	d.T("set-option", "-t", "=devu:", "@dev_started_by", "boss-x")
	d.T("set-option", "-t", "=plain:", "@dev_herd", "1")
	d.T("set-option", "-t", "=devt:", "@dev_started_by", display("=plain:", "#{session_id}"))
	d.T("run-shell", save+" quiet")
	// A new conversation in the same layout: last's .dev holds its state whether save.sh drops the new
	// snapshot as identical or keeps it (resurrect's ps of the sidebar pane may catch a sidebar tmux call)
	transcript := hook("devt", "s1")
	d.T("run-shell", save+" quiet")
	last, _ := os.Readlink(filepath.Join(resurrectDir, "last"))
	state, _ := os.ReadFile(filepath.Join(resurrectDir, last+".dev"))
	check(t, "dev state saved next to snapshot", string(state), "devt\tdevt\t1.1\ndevu\tdevu\t1.1\nplain\t\t\n"+
		":started_by\tdevt\tplain\n:started_by\tdevu\tboss-x\n"+
		":agent\tdevt\t1.2\tclaude\ts1\t"+transcript+"\t"+dir+"\n"+
		":agent\tdevu\t1.2\tclaude\tu1\t"+goneTranscript+"\t"+dirU+"\n")
	check(t, "no save warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")
	if err := os.Remove(goneTranscript); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(d.stubLog, 0); err != nil {
		t.Fatal(err)
	}

	// Cold start: the command the first `dev` of a server runs
	d.kill()
	d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	sessions := strings.Split(d.T("ls", "-F", "#{session_name}"), "\n")
	sort.Strings(sessions)
	check(t, "sessions restored", strings.Join(sessions, " "), "boot devt devu plain")
	noRC := ` --settings {"remoteControlAtStartup":false}`
	agents := display("=devt:1.2", "#{pane_id}") + " claude --resume s1" + noRC + "\n" + display("=devu:1.2", "#{pane_id}") + " claude" + noRC + "\n"
	claudes := func() []string {
		log, _ := os.ReadFile(d.stubLog)
		var lines []string
		for _, l := range strings.SplitAfter(string(log), "\n") {
			if strings.Contains(l, " claude") {
				lines = append(lines, l)
			}
		}
		return lines
	}
	waitFor(t, "agents restarted", func() bool { return len(claudes()) == 2 })
	check(t, "each agent pane resumes its conversation, else a fresh claude, a worker without Remote Control", strings.Join(claudes(), ""), agents)
	sidebar := sidebarOption()
	start := display(sidebar, "#{pane_start_command}")
	t.Logf("rebuilt sidebar %s: %s", sidebar, start)
	check(t, "Go sidebar rebuilt", devSidebarStart(start), "true")
	check(t, "@project restored", d.T("show-option", "-qv", "-t", "=devt:", "@project"), "devt")
	check(t, "bosses restored by name", d.T("show-option", "-qv", "-t", "=devt:", "@dev_started_by")+" "+
		d.T("show-option", "-qv", "-t", "=devu:", "@dev_started_by"), "plain boss-x")
	check(t, "restored zoom undone", display("=devt:2", "#{window_zoomed_flag}"), "0")
	check(t, "focus stays on agent pane", display("=devt:1", "#{pane_index}"), "2")
	check(t, "restore warns only about devu's lost conversation", d.T("show", "-gqv", "@dev_resurrect_warnings"),
		"devu:1.2: conversation u1 not resumed (directory changed or transcript gone), fresh agent; ")
	d.T("set-option", "-gu", "@dev_resurrect_warnings")
	// The dir must exist (pre-restore creates it), proving the hooks used the temp state dir; the saves' lock stays
	live, err := os.ReadDir(filepath.Join(d.tmp, "state/dev"))
	check(t, "live list cleaned up", dirListing(live, err), "snapshot.lock")

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
