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

// Port of ppf/terminal tmux/tests/restore-test.sh: dev session save/restore (dev-resurrect.sh + resurrect
// hooks) on the bash scripts. The agent pane runs a stand-in claude binary; the restore
// relaunches it as `sleep`.
func TestRestore(t *testing.T) {
	home := tmuxHome()
	restore := filepath.Join(home, "plugins/tmux-resurrect/scripts/restore.sh")
	save := filepath.Join(home, "plugins/tmux-resurrect/scripts/save.sh")
	project := filepath.Join(home, "sessions/project.sh")
	toggle := filepath.Join(home, "sessions/toggle-sidebar.sh")
	requireFiles(t, restore, save)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	for _, d := range []string{"resurrect", "state"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stateHome := filepath.Join(tmp, "state")
	// Same claude pattern as .tmux.conf with a harmless relaunch command (lazygit isn't asserted)
	s := newServer(t, `source-file ~/.tmux.conf
set -g @resurrect-dir '`+tmp+`/resurrect'
set -g @resurrect-processes '"~^([^ ]*/)?claude( |$)->sleep 600"'
`, "XDG_STATE_HOME="+stateHome, "PATH="+filepath.Dir(sleeper(t, "claude"))+":"+os.Getenv("PATH"))
	display := func(target, format string) string {
		out, _ := s.Try("display", "-t", target, "-p", format)
		return out
	}
	sidebarOption := func() string { return s.T("show-option", "-qv", "-t", "devt:", "@sidebar_pane") }

	s.T("new-session", "-d", "-s", "boot", "-x", "200", "-y", "50")
	s.T("send-keys", "-t", "boot", project+" "+shellQuote(repo)+" devt", "Enter")
	waitFor(t, "devt session", func() bool { _, err := s.Try("has-session", "-t", "devt"); return err == nil })
	// Layout done: sidebar registered and claude running in the agent pane
	waitFor(t, "sidebar and claude", func() bool {
		pid := display("devt:1.2", "#{pane_pid}")
		return pid != "" && sidebarOption() != "" && exec.Command("pgrep", "-f", "-P", pid, "claude").Run() == nil
	})
	s.T("resize-pane", "-Z", "-t", "devt:2.1") // a compact-mode zoom at save time
	s.T("new-session", "-d", "-s", "plain")
	s.T("run-shell", save+" quiet")
	last, _ := os.Readlink(filepath.Join(tmp, "resurrect/last"))
	state, _ := os.ReadFile(filepath.Join(tmp, "resurrect", last+".dev"))
	check(t, "dev state saved next to snapshot", string(state), "devt\tdevt\t1.1\n")
	check(t, "no save warnings", s.T("show", "-gqv", "@dev_resurrect_warnings"), "")

	// Cold start: same command project.sh runs on the first `dev` of a server
	s.kill()
	s.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	waitFor(t, "claude pane relaunched", func() bool { return display("devt:1.2", "#{pane_current_command}") == "sleep" })
	sessions := strings.Split(s.T("ls", "-F", "#{session_name}"), "\n")
	sort.Strings(sessions)
	check(t, "sessions restored", strings.Join(sessions, " "), "boot devt plain")
	sidebar := sidebarOption()
	check(t, "sidebar rebuilt", boolStr(sidebar != "" && strings.Contains(display(sidebar, "#{pane_start_command}"), "sidebar.sh")), "1")
	check(t, "@project restored", s.T("show-option", "-qv", "-t", "devt:", "@project"), "devt")
	check(t, "claude pane relaunched by its restore pattern", display("devt:1.2", "#{pane_current_command}"), "sleep")
	check(t, "restored zoom undone", display("devt:2", "#{window_zoomed_flag}"), "0")
	check(t, "focus stays on claude pane", display("devt:1", "#{pane_index}"), "2")
	check(t, "no restore warnings", s.T("show", "-gqv", "@dev_resurrect_warnings"), "")
	// The dir must exist (pre-restore creates it), proving the hooks used the temp state dir
	live, err := os.ReadDir(filepath.Join(stateHome, "dev"))
	check(t, "live list cleaned up", dirListing(live, err), "")

	// Manual restore with sidebars hidden must not kill or change any live pane (resurrect itself may
	// add back panes that existed at save time, e.g. the hidden sidebar's slot)
	panes := func() []string {
		return strings.Split(s.T("list-panes", "-a", "-F", "#{pane_id} #{session_name} #{pane_current_command}"), "\n")
	}
	// Toggle mode resolves "current session" from the pane env, as when pressed in devt
	pane := display("devt:1.2", "#{pane_id}")
	if pane == "" {
		t.Fatal("no devt:1.2 pane")
	}
	if out, err := runScript(s.scriptEnv("TMUX_PANE="+pane), toggle); err != nil {
		t.Fatalf("toggle-sidebar.sh: %v\n%s", err, out)
	}
	check(t, "sidebars hidden", s.T("show", "-gqv", "@sidebar_hidden")+"/"+sidebarOption(), "1/")
	before := panes()
	s.T("run-shell", restore)
	time.Sleep(2 * time.Second)
	after := panes()
	var gone []string
	for _, p := range before {
		if !slices.Contains(after, p) {
			gone = append(gone, p)
		}
	}
	check(t, "manual restore leaves live panes alone", strings.Join(gone, "\n"), "")
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func dirListing(entries []os.DirEntry, err error) string {
	if err != nil {
		return "missing"
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}
