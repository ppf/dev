//go:build integration

package integration

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/tmux"
)

// viewScript is the bash reference for the sidebar's screen.
func viewScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(tmuxHome(t), "sessions/sidebar.sh")
	requireFiles(t, p)
	requireExported(t, map[string]string{"tmux/sessions/sidebar.sh": ".tmux/sessions/sidebar.sh"})
	return p
}

// viewGitRepo returns a temp repository with one commit on branch.
func viewGitRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := tempDir(t)
	// No hooks: the user's global core.hooksPath must not run on a test commit
	git := []string{"-C", dir, "-c", "core.hooksPath=" + t.TempDir(), "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	for _, args := range [][]string{{"init", "-q", "-b", branch}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append(git, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// viewSame waits until the bash and dev sidebars show the same screen with every marker on it, then
// reports the comparison (both screens on a mismatch) and returns the dev screen.
func viewSame(d *devServer, name, bashPane, devPane string, markers ...string) string {
	d.t.Helper()
	var bash, dev string
	for range 40 {
		bash = viewCapture(d, bashPane)
		dev = viewCapture(d, devPane)
		if bash == dev && viewHasAll(dev, markers) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	check(d.t, name, dev, bash)
	if !viewHasAll(dev, markers) {
		d.t.Errorf("❌ %s: screen lacks one of %q:\n%s", name, markers, dev)
	}
	return dev
}

// trailingSGR is style codes after a line's last cell: they style nothing, and whether tmux emits them
// depends on what the cells held before (bash overwrites a line in place, Bubble Tea clears it).
var trailingSGR = regexp.MustCompile(`(\x1b\[[0-9;]*m)+$`)

// viewCapture is pane's screen with escapes, without trailingSGR.
func viewCapture(d *devServer, pane string) string {
	d.t.Helper()
	lines := strings.Split(d.T("capture-pane", "-p", "-e", "-t", pane), "\n")
	for i, l := range lines {
		lines[i] = trailingSGR.ReplaceAllString(l, "")
	}
	return strings.Join(lines, "\n")
}

// viewSelectedRow returns the session on the first line with the selection background ("" if none).
func viewSelectedRow(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		if !strings.Contains(line, "42;42;55") {
			continue
		}
		if f := strings.Fields(ansi.Strip(line)); len(f) > 1 {
			return f[1]
		}
	}
	return ""
}

func viewHasAll(screen string, markers []string) bool {
	for _, m := range markers {
		if !strings.Contains(screen, m) {
			return false
		}
	}
	return true
}

// viewHint is the focused sidebar's bottom row, unstyled.
const viewHint = " j/k ⏎  d new  x kill  r ren  q back"

// viewFocused focuses pane and returns its screen once the hint shows.
func viewFocused(d *devServer, pane string) string {
	d.t.Helper()
	d.T("select-pane", "-t", pane)
	var screen string
	waitFor(d.t, "hint in "+pane, func() bool {
		screen = viewCapture(d, pane)
		return strings.Contains(screen, "j/k")
	})
	return screen
}

// viewCodex builds a stand-in codex binary: the agent rule matches the pane's process name, which
// macOS takes from the executable, so a script or symlink would show as sh or sleep.
func viewCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n")
	build := exec.Command("go", "build", "-o", "codex", "main.go")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build codex stub: %v\n%s", err, out)
	}
	return filepath.Join(dir, "codex")
}

// sidebar.sh and `dev sidebar` side by side in one window, equal size, against the same sessions (one
// server, so both read identical state): at widths 40, 30 and 11, unfocused and each focused, then after
// a selection change.
func TestSidebarViewParity(t *testing.T) {
	script := viewScript(t)
	d := newDevServer(t, "")
	repo := viewGitRepo(t, "feature/login")
	longRepo := viewGitRepo(t, "feature/a-branch-name-long-enough-to-cut")
	plain := tempDir(t)
	codex := viewCodex(t)

	// Agent panes run sleep, hook-registered via @agent_cmd: bash calls any non-shell window-1 pane
	// Claude, the Go sidebar only a registered one. The Codex pane has no hook state and a "dir | task"
	// title. Explicit titles: tmux's default is the hostname, which only Go treats as generic (bash
	// hardcodes mac.home).
	for _, s := range []struct{ name, project, dir, cmd, state, title string }{
		{"alpha", "web", repo, "sleep", "working", "Fix the login form"},
		{"beta", "web", plain, "sleep", "waiting", "Claude Code"},
		{"gamma", "api", repo, "sleep", "idle", "Write the docs"},
		{"epsilon-with-a-long-session-name", "api", longRepo, "codex", "", "my-repo | Port the parser to Go"},
	} {
		run := "exec sleep 600"
		if s.cmd == "codex" {
			run = "exec " + shellQuote(codex)
		}
		pane := d.T("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", s.name, "-c", s.dir, run)
		d.T("set-option", "-t", tmux.SessionTarget(s.name), "@project", s.project)
		if s.state != "" {
			d.T("set-option", "-p", "-t", pane, "@agent_cmd", s.cmd)
			d.T("set-option", "-p", "-t", pane, "@agent_state", s.state)
		}
		d.T("select-pane", "-t", pane, "-T", s.title)
		waitFor(t, s.name+" runs "+s.cmd, func() bool {
			cmd, _ := d.Try("display", "-t", pane, "-p", "#{pane_current_command}")
			return cmd == s.cmd
		})
	}
	d.T("new-session", "-d", "-s", "delta", "-c", plain)
	d.T("set-option", "-t", tmux.SessionTarget("delta"), "@project", "api")

	// Window 2, so neither sidebar process is a candidate agent pane (only window 1 is scanned)
	d.T("new-session", "-d", "-s", "view")
	bashPane := d.T("new-window", "-P", "-F", "#{pane_id}", "-t", "=view:", "exec "+shellQuote(script))
	devPane := d.T("split-window", "-h", "-P", "-F", "#{pane_id}", "-t", bashPane, "exec "+shellQuote(devBin)+" sidebar")
	other := d.T("split-window", "-h", "-P", "-F", "#{pane_id}", "-t", devPane)
	size := func(p string) string { return d.T("display", "-t", p, "-p", "#{pane_width}x#{pane_height}") }

	for i, w := range []int{40, 30, 11} {
		name := func(s string) string { return fmt.Sprintf("W=%d %s", w, s) }
		d.T("resize-window", "-t", bashPane, "-x", strconv.Itoa(3*w+2), "-y", "30")
		d.T("select-layout", "-t", bashPane, "even-horizontal")
		want := fmt.Sprintf("%dx30", w)
		if size(bashPane) != want || size(devPane) != want {
			t.Fatalf("sidebar sizes %s and %s, want %s", size(bashPane), size(devPane), want)
		}
		d.T("select-pane", "-t", other)

		markers := []string{"SESSIONS"}
		switch w {
		case 40:
			markers = append(markers, "feature/login", "Claude · waiting", "Write the docs", "no agent",
				"Codex · Port the parser to Go on m…", "epsilon-with-a-long-session-na…")
		case 30:
			markers = append(markers, "Codex · Port the parser …", "epsilon-with-a-long-…")
		}
		unfocused := viewSame(d, name("unfocused"), bashPane, devPane, markers...)
		if i == 0 {
			// The first to refresh fills the empty @sidebar_selected_session with its session, and both draw it
			// (sidebar.TestModelSyncSelection covers dev filling it)
			check(t, "initial selection", viewSelectedRow(unfocused), "view")
		}
		bash := viewFocused(d, bashPane)
		dev := viewFocused(d, devPane)
		if w >= ansi.StringWidth(viewHint) {
			check(t, name("focused"), dev, bash)
			continue
		}
		// bash prints a hint wider than the pane unclipped: it wraps on the bottom row and scrolls the
		// screen up a line or more. dev clips it; the rows above stay as unfocused.
		above := func(screen string) string { l := strings.Split(screen, "\n"); return strings.Join(l[:len(l)-1], "\n") }
		check(t, name("focused: rows above the hint"), above(dev), above(unfocused))
		lines := strings.Split(dev, "\n")
		check(t, name("focused: hint clipped"), ansi.Strip(lines[len(lines)-1]), strings.TrimRight(ansi.Truncate(viewHint, w, ""), " "))
	}

	d.T("select-pane", "-t", other)
	d.T("set-option", "-g", "@sidebar_selected_session", "gamma")
	// Until both moved, the old equal screens would pass the comparison
	waitFor(t, "both sidebars select gamma", func() bool {
		return viewSelectedRow(d.T("capture-pane", "-p", "-e", "-t", bashPane)) == "gamma" &&
			viewSelectedRow(d.T("capture-pane", "-p", "-e", "-t", devPane)) == "gamma"
	})
	viewSame(d, "W=11 selection on gamma", bashPane, devPane, "SESSIONS")
}
