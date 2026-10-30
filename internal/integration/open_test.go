//go:build integration

package integration

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

const popupGuard = "SIDEBAR_SELECT_IN_POPUP=1"

// projectScript is the bash reference for opening a session.
func projectScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(tmuxHome(t), "sessions/project.sh")
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// opened is what one implementation left behind after opening a session twice.
type opened struct {
	out, status       string
	first, second     string // snapshots after the new-session and the existing-session run
	out2, status2     string
	sessions, clients string
	sidebar           string // the sidebar pane's start command
	sidebarPath       string // and its directory
	agent             string // the agent pane's current command
	stub              string // the stub command line the agent pane ran
}

// devSidebarStart reports whether a sidebar start command runs the dev binary's sidebar.
func devSidebarStart(start string) string {
	return strconv.FormatBool(strings.Contains(start, "exec "+tmux.ShellJoin(devBin, "sidebar")))
}

// `dev <name>` against `project.sh <path> <name>` on identical servers: the new-session layout, then the
// existing-session path (attach, no duplicate).
func TestOpenParity(t *testing.T) {
	project := projectScript(t)
	dir := tempDir(t)
	cfg := writeProjects(t, [2]string{"app", dir})
	impls := map[string]func(d *devServer) (string, error){
		"bash": func(d *devServer) (string, error) { return runScript(d.scriptEnv(), project, dir, "app") },
		"dev":  func(d *devServer) (string, error) { return runScript(d.scriptEnv(cfg), devBin, "app") },
	}
	got := map[string]opened{}
	for name, open := range impls {
		d := newDevServer(t, "set -g @dev_restored 1\n")
		var r opened
		out, err := open(d)
		r.out, r.status = out, errStatus(err)
		d.waitForStubs("app")
		r.agent = d.T("display", "-t", "=app:1.2", "-p", "#{pane_current_command}")
		r.stub = d.agentStub("app")
		r.first = d.snapshot("app")
		out, err = open(d)
		r.out2, r.status2 = out, errStatus(err)
		r.second = d.snapshot("app")
		r.sessions = d.T("list-sessions", "-F", "#{session_name}")
		r.clients = d.T("list-clients", "-F", "#{client_session}")
		r.sidebar = d.sidebarStart("app")
		sidebarPane := d.T("show-option", "-qv", "-t", "=app:", "@sidebar_pane")
		r.sidebarPath = d.T("display", "-t", sidebarPane, "-p", "#{pane_current_path}")
		got[name] = r
		t.Logf("%s snapshot:\n%s", name, r.first)
	}

	dev, bash := got["dev"], got["bash"]
	check(t, "first run output", dev.out, bash.out)
	check(t, "first run status", dev.status, bash.status)
	check(t, "layout after first run", dev.first, bash.first)
	check(t, "second run output", dev.out2, bash.out2)
	check(t, "second run status", dev.status2, bash.status2)
	check(t, "layout after second run", dev.second, bash.second)
	check(t, "sessions", dev.sessions, bash.sessions)

	// Absolute checks, so a fault shared by both can't pass
	check(t, "dev: title escape", dev.out, "\033]2;app\007")
	check(t, "dev: status", dev.status+"/"+dev.status2, "0/0")
	check(t, "dev: no duplicate session", dev.sessions, "app\nboot")
	check(t, "dev: client switched", dev.clients, "app")
	// The deliberate difference: the same pane runs dev's sidebar instead of sidebar.sh
	check(t, "bash: sidebar runs sidebar.sh", strconv.FormatBool(strings.Contains(bash.sidebar, "exec "+filepath.Join(tmuxHome(t), "sessions/sidebar.sh"))), "true")
	check(t, "dev: sidebar runs dev sidebar", devSidebarStart(dev.sidebar), "true")
	// project.sh's sidebar starts in the caller's directory, dev's in the project's
	check(t, "dev: sidebar in the project's directory", dev.sidebarPath, dir)
	// And claude runs under dev pty-wrap
	check(t, "bash: claude runs as is", bash.agent, "sleep")
	check(t, "dev: claude runs under dev pty-wrap", dev.agent, filepath.Base(devBin))
	check(t, "dev: claude named app in Remote Control", dev.stub, "claude --remote-control=app")
	for _, want := range []string{
		"session app @project=app clients=app\n",
		"window 1 Agent active=1 panes=4\nwindow 2 Files active=0 panes=3\n",
		`cmd=<sidebar> start="<sidebar>"`,
		`pane 1.2 active=true 58x49+41+0 cmd=sleep start="/bin/sh" path=` + dir + ` stub="claude "`,
		`stub="lazygit status --screen-mode=half"`, `stub="nvim ."`,
	} {
		if !strings.Contains(dev.first, want) {
			t.Errorf("❌ dev snapshot lacks %q", want)
		}
	}
}

// The first dev of a server restores saved sessions before the has-session check, so `dev app` attaches to
// a restored app; it sets @dev_restored and prints then unsets the restore warnings. Later runs skip it.
func TestOpenFirstRunRestore(t *testing.T) {
	project := projectScript(t)
	requireFiles(t, filepath.Join(tmuxHome(t), "plugins/tmux-resurrect/scripts/restore.sh"))
	dir := tempDir(t)
	cfg := writeProjects(t, [2]string{"app", dir}, [2]string{"two", dir})
	impls := map[string]func(d *devServer, name string) (string, error){
		"bash": func(d *devServer, name string) (string, error) { return runScript(d.scriptEnv(), project, dir, name) },
		"dev":  func(d *devServer, name string) (string, error) { return runScript(d.scriptEnv(cfg), devBin, name) },
	}
	outs := map[string]string{}
	for name, open := range impls {
		// The warning stands in for one dev-resurrect.sh would append.
		d := newDevServer(t, "set -g @dev_resurrect_warnings 'devt: slot busy'\n")
		// A one-pane snapshot of session app in save.sh's tab-separated format.
		writeFile(t, filepath.Join(d.tmp, "resurrect/last"),
			"pane\tapp\t1\t1\t:*\t1\trestored\t:"+dir+"\t1\tsh\t:\nstate\tboot\tboot\n")
		hasApp := func() string { _, err := d.Try("has-session", "-t", "=app:"); return strconv.FormatBool(err == nil) }
		check(t, name+": no app before the first run", hasApp(), "false")
		out, err := open(d, "app")
		outs[name] = out
		check(t, name+": first run status", errStatus(err), "0")
		check(t, name+": app restored", hasApp(), "true")
		check(t, name+": restored app attached, not rebuilt", d.T("display", "-t", "=app:", "-p", "#{session_windows}"), "1")
		check(t, name+": client switched", d.T("list-clients", "-F", "#{client_session}"), "app")
		check(t, name+": @dev_restored set", d.T("show-option", "-gqv", "@dev_restored"), "1")
		check(t, name+": warnings unset", d.T("show-option", "-gqv", "@dev_resurrect_warnings"), "")
		d.T("set-option", "-g", "@dev_resurrect_warnings", "later")
		out, err = open(d, "two")
		check(t, name+": second run skips restore", out+errStatus(err), "\033]2;two\0070")
		check(t, name+": later warnings left alone", d.T("show-option", "-gqv", "@dev_resurrect_warnings"), "later")
	}
	check(t, "first run output (dev = bash)", outs["dev"], outs["bash"])
	check(t, "dev: warnings printed", outs["dev"], "⚠  Session restore: devt: slot busy\n\033]2;app\007")
}

// The picker opens the chosen project's boss: Enter on a project creates boss-<name> (coordinator in its
// directory) and switches the client to it, Enter again attaches to it, and a git repo gets no worktree step.
func TestPickerBoss(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	plain, repo := tempDir(t), newRepo(t, "", "")
	env := append([]string{writeProjects(t, [2]string{"app", plain}, [2]string{"repo", repo}), popupGuard}, gitEnv...)
	var statuses string
	for range 2 {
		_, status := d.runInPane(env, []string{devBin}, step{wait: "Select project", keys: []string{"Enter"}})
		statuses += status
		check(t, "client on the boss", d.T("list-clients", "-F", "#{client_session}"), "boss-app")
	}
	check(t, "attached, no duplicate", d.T("list-sessions", "-F", "#{session_name}"), "boot\nboss-app")
	check(t, "boss option", d.T("show-option", "-qv", "-t", "=boss-app:", "@dev_herd"), "1")
	// Panes left to right: sidebar, coordinator, previews
	coordinator := d.T("display", "-t", "=boss-app:1.2", "-p", "#{pane_current_path} #{pane_start_command}")
	if !strings.HasPrefix(coordinator, plain+" ") || !strings.HasSuffix(coordinator, "--remote-control=boss-app") {
		t.Errorf("❌ coordinator pane: %q", coordinator)
	}

	screen, status := d.runInPane(env, []string{devBin}, step{wait: "Select project", keys: []string{"repo", "Enter"}})
	statuses += status
	t.Logf("dev repo run:\n%s", screen)
	check(t, "statuses", statuses, "000")
	check(t, "repo boss, no worktree", d.T("list-sessions", "-F", "#{session_name}"), "boot\nboss-app\nboss-repo")
	check(t, "client on the repo boss", d.T("list-clients", "-F", "#{client_session}"), "boss-repo")
	check(t, "repo coordinator in the main checkout", d.T("display", "-t", "=boss-repo:1.2", "-p", "#{pane_current_path}"), repo)
}

// ctrl-o in the picker opens the project's plain session instead of its boss, as dev <name> does: created
// in the main checkout (a git repo too, no worktree step), attached the second time.
func TestPickerSession(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	plain, repo := tempDir(t), newRepo(t, "", "")
	env := append([]string{writeProjects(t, [2]string{"app", plain}, [2]string{"repo", repo}), popupGuard}, gitEnv...)
	var statuses string
	for range 2 {
		_, status := d.runInPane(env, []string{devBin}, step{wait: "Select project", keys: []string{"C-o"}})
		statuses += status
		check(t, "client on the session", d.T("list-clients", "-F", "#{client_session}"), "app")
	}
	_, status := d.runInPane(env, []string{devBin}, step{wait: "Select project", keys: []string{"repo", "C-o"}})
	statuses += status
	check(t, "statuses", statuses, "000")
	check(t, "plain sessions, no boss or duplicate", d.T("list-sessions", "-F", "#{session_name}"), "app\nboot\nrepo")
	check(t, "client on the repo session", d.T("list-clients", "-F", "#{client_session}"), "repo")
	d.waitForStubs("repo")
	check(t, "repo session in the main checkout", d.T("display", "-t", "=repo:1.2", "-p", "#{pane_current_path}"), repo)
}

// The picker restores saved sessions on the first dev of a server too, then opens the chosen boss.
func TestPickerFirstRunRestore(t *testing.T) {
	requireFiles(t, filepath.Join(tmuxHome(t), "plugins/tmux-resurrect/scripts/restore.sh"))
	dir := tempDir(t)
	d := newDevServer(t, "set -g @dev_resurrect_warnings 'devt: slot busy'\n")
	writeFile(t, filepath.Join(d.tmp, "resurrect/last"),
		"pane\tapp\t1\t1\t:*\t1\trestored\t:"+dir+"\t1\tsh\t:\nstate\tboot\tboot\n")
	env := []string{writeProjects(t, [2]string{"app", dir}), popupGuard}
	screen, status := d.runInPane(env, []string{devBin},
		step{wait: "Select project", keys: []string{"Enter"}},
		step{wait: "Press enter to continue", keys: []string{"Enter"}})
	check(t, "status", status, "0")
	check(t, "warnings printed", strconv.FormatBool(strings.Contains(screen, "⚠  Session restore: devt: slot busy")), "true")
	check(t, "sessions", d.T("list-sessions", "-F", "#{session_name}"), "app\nboot\nboss-app")
	check(t, "@dev_restored set", d.T("show-option", "-gqv", "@dev_restored"), "1")
	check(t, "client on the boss", d.T("list-clients", "-F", "#{client_session}"), "boss-app")
}

var headSHA = regexp.MustCompile(`HEAD is now at [0-9a-f]+`)

// `dev <name> <worktree>` against project.sh with DEV_WORKTREE: the first run creates the
// worktree and session <name>-<worktree with / as ->, the second attaches to it. A rerun after the session
// is gone reuses the worktree; a name sanitizing would change is rejected.
func TestOpenWorktreeArg(t *testing.T) {
	project := projectScript(t)
	type run struct{ out, out2, statuses, sessions, first, second string }
	got := map[string]run{}
	for _, impl := range []string{"bash", "dev"} {
		d := newDevServer(t, "set -g @dev_restored 1\n")
		repo := newRepo(t, "", "")
		open := func() (string, error) {
			if impl == "bash" {
				return runScript(d.scriptEnv(append(gitEnv, "DEV_WORKTREE=feature/x")...), project, repo, "repo")
			}
			return runScript(d.scriptEnv(append(gitEnv, writeProjects(t, [2]string{"repo", repo}))...), devBin, "repo", "feature/x")
		}
		var r run
		out, err := open()
		// Each run's repo has its own commit hash.
		r.out, r.statuses = headSHA.ReplaceAllString(out, "HEAD is now at <sha>"), errStatus(err)
		d.waitForStubs("repo-feature-x")
		r.first = d.snapshot("repo-feature-x", repo, "<repo>")
		out, err = open()
		r.out2, r.statuses = out, r.statuses+errStatus(err)
		r.second = d.snapshot("repo-feature-x", repo, "<repo>")
		r.sessions = d.T("list-sessions", "-F", "#{session_name}")
		got[impl] = r
		t.Logf("%s first run:\n%s", impl, r.out)

		if impl == "dev" {
			// Back on boot first: killing the viewed session would detach the only client.
			actSwitch(d, "boot")
			d.T("kill-session", "-t", "=repo-feature-x")
			out, err = open()
			check(t, "dev: rerun status", errStatus(err), "0")
			check(t, "dev: rerun reuses the worktree", strconv.FormatBool(strings.Contains(out, "Preparing worktree")), "false")
			d.waitForStubs("repo-feature-x")
			check(t, "dev: rerun session in the worktree", strconv.FormatBool(strings.Contains(
				d.snapshot("repo-feature-x", repo, "<repo>"), "path=<repo>/.claude/worktrees/feature/x")), "true")
			before := git(t, repo, "worktree", "list", "--porcelain")
			out, err = runScript(d.scriptEnv(writeProjects(t, [2]string{"repo", repo})), devBin, "repo", "bad name")
			check(t, "dev: bad name status", errStatus(err), "1")
			check(t, "dev: bad name output", out, "❌ invalid worktree name \"bad name\": only A-Za-z0-9._/- allowed\n")
			check(t, "dev: bad name creates nothing", git(t, repo, "worktree", "list", "--porcelain"), before)
		}
	}
	dev, bash := got["dev"], got["bash"]
	check(t, "first run output (dev = bash)", dev.out, bash.out)
	check(t, "second run output (dev = bash)", dev.out2, bash.out2)
	check(t, "layout after first run (dev = bash)", dev.first, bash.first)
	check(t, "layout after second run (dev = bash)", dev.second, bash.second)
	check(t, "sessions (dev = bash)", dev.sessions, bash.sessions)
	check(t, "dev: statuses", dev.statuses, "00")
	check(t, "dev: attached, no duplicate", dev.sessions, "boot\nrepo-feature-x")
	check(t, "dev: second run output", dev.out2, "\033]2;repo-feature-x\007")
	if want := "session repo-feature-x @project=repo clients=repo-feature-x\n"; !strings.HasPrefix(dev.first, want) {
		t.Errorf("❌ dev snapshot doesn't start with %q:\n%s", want, dev.first)
	}
	if want := "path=<repo>/.claude/worktrees/feature/x"; !strings.Contains(dev.first, want) {
		t.Errorf("❌ dev snapshot lacks %q:\n%s", want, dev.first)
	}
}

// errStatus is a command's exit status as runInPane reports it.
func errStatus(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strconv.Itoa(exitErr.ExitCode())
	}
	if err != nil {
		return err.Error()
	}
	return "0"
}

// Esc in the picker exits 0 without opening anything.
func TestPickerEsc(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	env := []string{writeProjects(t, [2]string{"app", tempDir(t)}), popupGuard}
	_, status := d.runInPane(env, []string{devBin}, step{wait: "Select project", keys: []string{"Escape"}})
	check(t, "status", status, "0")
	check(t, "nothing opened", d.T("list-sessions", "-F", "#{session_name}"), "boot")
}

// dev without arguments inside tmux re-runs itself in an 80%×60% rounded popup on the client, where the
// picker (not another popup) runs; its choice opens the project's boss and the popup's exit status comes back.
func TestPickerPopup(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	// The popup's command gets the server's environment, not the caller's
	key, dir, _ := strings.Cut(writeProjects(t, [2]string{"app", tempDir(t)}), "=")
	d.T("set-environment", "-g", key, dir)
	var popup string
	screen, status := d.runInPane(nil, []string{devBin},
		step{wait: "Select project", keys: []string{"Enter"}, onClient: true, screen: &popup})
	t.Logf("client screen with the popup:%s", popup)
	check(t, "popup size on a 200x50 client, border included", popupSize(popup), "160x30")
	check(t, "status", status, "0")
	check(t, "pane output (the popup has the terminal)", screen, "")
	check(t, "boss opened, client switched", d.T("list-clients", "-F", "#{client_session}"), "boss-app")
}

// A failing popup child keeps its error on screen until Enter, and its exit status comes back.
func TestPickerPopupFailure(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	d.T("set-environment", "-g", "XDG_CONFIG_HOME", t.TempDir()) // no projects.toml
	var popup string
	_, status := d.runInPane(nil, []string{devBin},
		step{wait: "Press enter to close", keys: []string{"Enter"}, onClient: true, screen: &popup})
	t.Logf("client screen with the popup:%s", popup)
	if !strings.Contains(popup, "❌ config:") {
		t.Errorf("❌ popup lacks the error:%s", popup)
	}
	check(t, "status", status, "1")
	check(t, "nothing opened", d.T("list-sessions", "-F", "#{session_name}"), "boot")
}

// A boss-<project> session the boss didn't create is refused in the popup, its error kept until Enter.
func TestPickerPopupNotBoss(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	key, dir, _ := strings.Cut(writeProjects(t, [2]string{"app", tempDir(t)}), "=")
	d.T("set-environment", "-g", key, dir)
	d.T("new-session", "-d", "-s", "boss-app")
	var popup string
	_, status := d.runInPane(nil, []string{devBin},
		step{wait: "Select project", keys: []string{"Enter"}, onClient: true},
		step{wait: "Press enter to close", keys: []string{"Enter"}, onClient: true, screen: &popup})
	if !strings.Contains(popup, "isn't a boss session") {
		t.Errorf("❌ popup lacks the refusal:%s", popup)
	}
	check(t, "status", status, "1")
	check(t, "client stays", d.T("list-clients", "-F", "#{client_session}"), "boot")
}

// popupSize measures the outermost rounded border on screen as WIDTHxHEIGHT.
func popupSize(screen string) string {
	lines := strings.Split(screen, "\n")
	for top, line := range lines {
		left := strings.Index(line, "╭")
		if left < 0 {
			continue
		}
		col := len([]rune(line[:left]))
		width := len([]rune(line[left:strings.LastIndex(line, "╮")])) + 1
		for bottom := top + 1; bottom < len(lines); bottom++ {
			if r := []rune(lines[bottom]); len(r) > col && r[col] == '╰' {
				return fmt.Sprintf("%dx%d", width, bottom-top+1)
			}
		}
	}
	return "no popup"
}
