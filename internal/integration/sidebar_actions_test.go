//go:build integration

package integration

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// actServer is a dev server with mouse on and emacs prompt keys (EDITOR=*vi* would pick vi keys), the
// given sessions besides boot, and a projects.toml in the global environment for the picker popup.
func actServer(t *testing.T, sessions ...string) *devServer {
	t.Helper()
	d := newDevServer(t, "set -g mouse on\nset -g status-keys emacs\n")
	key, dir, _ := strings.Cut(writeProjects(t, [2]string{"app", tempDir(t)}), "=")
	d.T("set-environment", "-g", key, dir)
	for _, kv := range gitEnv {
		k, v, _ := strings.Cut(kv, "=")
		d.T("set-environment", "-g", k, v)
	}
	for _, s := range sessions {
		d.T("new-session", "-d", "-s", s)
	}
	return d
}

// actSwitch moves the client to session.
func actSwitch(d *devServer, session string) {
	d.t.Helper()
	d.T("switch-client", "-c", d.T("list-clients", "-F", "#{client_tty}"), "-t", "="+session+":")
	waitFor(d.t, "client on "+session, func() bool { return actClientSession(d) == session })
}

func actClientSession(d *devServer) string {
	s, _ := d.Try("list-clients", "-F", "#{client_session}")
	return s
}

// actEnsure starts session's Go sidebar the way the hooks do (dev sidebar ensure, from session's active
// pane) and returns its pane once it shows every session.
func actEnsure(d *devServer, session string) string {
	d.t.Helper()
	from := d.T("display", "-t", "="+session+":", "-p", "#{pane_id}")
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+from), devBin, "sidebar", "ensure", session); err != nil {
		d.t.Fatalf("dev sidebar ensure %s: %v\n%s", session, err, out)
	}
	pane := d.T("show-option", "-qv", "-t", "="+session+":", "@sidebar_pane")
	if pane == "" {
		d.t.Fatalf("no sidebar in %s", session)
	}
	for _, s := range strings.Split(d.T("list-sessions", "-F", "#{session_name}"), "\n") {
		waitScreen(d.t, d.server, pane, s)
	}
	return pane
}

// actSelect focuses sidebar, waits until it knows (its hint row shows), selects name through the shared
// option and waits until the sidebar draws name's row selected (its background). The option is set again
// while it isn't: a sidebar refresh that read the option before may still write its own fallback over it.
func actSelect(d *devServer, sidebar, name string) {
	d.t.Helper()
	d.T("select-pane", "-t", sidebar)
	waitScreen(d.t, d.server, sidebar, "j/k")
	for i := range 60 {
		if i%4 == 0 {
			d.T("set-option", "-g", "@sidebar_selected_session", name)
		}
		line, ok := actRow(d, sidebar, name)
		styled := strings.Split(d.T("capture-pane", "-p", "-e", "-t", sidebar), "\n")
		if ok && strings.Contains(styled[line], "48;2;42;42;55") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.t.Fatalf("❌ timeout waiting for %s drawn selected (option %q):\n%s", name, actSelected(d)(), strings.Join(actScreen(d, sidebar), "\n"))
}

func actScreen(d *devServer, pane string) []string {
	return strings.Split(d.T("capture-pane", "-p", "-t", pane), "\n")
}

// actRow is the line of session's name row (status dot, name) on sidebar's screen.
func actRow(d *devServer, sidebar, session string) (int, bool) {
	for i, line := range actScreen(d, sidebar) {
		if f := strings.Fields(line); len(f) > 1 && f[0] != "─" && f[1] == session {
			return i, true
		}
	}
	return 0, false
}

// actWait polls get until it returns want, then reports the comparison.
func actWait(d *devServer, name string, get func() string, want string) {
	d.t.Helper()
	got := get()
	for i := 0; i < 60 && got != want; i++ {
		time.Sleep(250 * time.Millisecond)
		got = get()
	}
	check(d.t, name, got, want)
}

func actSelected(d *devServer) func() string {
	return func() string { return d.T("show-option", "-gqv", "@sidebar_selected_session") }
}

func actSessions(d *devServer) func() string {
	return func() string { return d.T("list-sessions", "-F", "#{session_name}") }
}

func actActivePane(d *devServer, session string) func() string {
	return func() string { return d.T("display", "-t", "="+session+":", "-p", "#{pane_id}") }
}

// actClick left-clicks column x, line y of pane: an SGR mouse press and release typed into the outer
// terminal, so the attached client reports a real MouseDown1Pane.
func actClick(d *devServer, pane string, x, y int) {
	d.t.Helper()
	var left, top int
	if _, err := fmt.Sscan(d.T("display", "-t", pane, "-p", "#{pane_left} #{pane_top}"), &left, &top); err != nil {
		d.t.Fatal(err)
	}
	col, row := left+x+1, top+y+1
	seq := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", col, row, col, row)
	args := []string{"send-keys", "-t", "out", "-H"}
	for _, b := range []byte(seq) {
		args = append(args, hex.EncodeToString([]byte{b}))
	}
	d.outer.T(args...)
}

// actPickerEsc waits for the picker popup on the client and closes it with Esc.
func actPickerEsc(d *devServer) {
	d.t.Helper()
	waitScreen(d.t, d.outer, "out", "Select project")
	d.outer.T("send-keys", "-t", "out", "Escape")
	waitFor(d.t, "picker closed", func() bool {
		return !strings.Contains(d.outer.T("capture-pane", "-p", "-t", "out"), "Select project")
	})
}

// The sidebar's keys, sent to its pane: j/k/↑/↓ wrap, ⏎ switches, q focuses the content pane, x kills,
// r renames through the client's prompt (a name with a quote), d opens the picker, ctrl+c quits.
func TestActionKeys(t *testing.T) {
	d := actServer(t, "a", "b", "c", "it's")
	actSwitch(d, "a")
	sidebar := actEnsure(d, "a")

	actSelect(d, sidebar, "a")
	for _, k := range []struct{ key, want string }{
		{"j", "b"}, {"k", "a"}, {"k", "it's"}, {"j", "a"}, {"Up", "it's"}, {"Down", "a"},
	} {
		d.T("send-keys", "-t", sidebar, k.key)
		actWait(d, k.key+": selected", actSelected(d), k.want)
	}

	actSelect(d, sidebar, "a")
	d.T("send-keys", "-t", sidebar, "Enter")
	actWait(d, "⏎ on the current session: content pane focused", func() string {
		return fmt.Sprint(actActivePane(d, "a")() != sidebar)
	}, "true")

	actSelect(d, sidebar, "b")
	d.T("send-keys", "-t", sidebar, "q")
	actWait(d, "q: content pane focused", func() string { return fmt.Sprint(actActivePane(d, "a")() != sidebar) }, "true")
	check(t, "q: client stays", actClientSession(d), "a")

	actSelect(d, sidebar, "c")
	d.T("send-keys", "-t", sidebar, "x")
	actWait(d, "x on c: sessions", actSessions(d), "a\nb\nboot\nit's")
	actWait(d, "x on c: selection back on the current session", actSelected(d), "a")

	actSelect(d, sidebar, "it's")
	d.T("send-keys", "-t", sidebar, "r")
	waitScreen(t, d.outer, "out", "Rename session:")
	d.outer.T("send-keys", "-t", "out", "C-u")
	d.outer.T("send-keys", "-t", "out", "-l", "renamed")
	d.outer.T("send-keys", "-t", "out", "Enter")
	actWait(d, "r on it's: sessions", actSessions(d), "a\nb\nboot\nrenamed")

	actSelect(d, sidebar, "b")
	d.T("send-keys", "-t", sidebar, "d")
	actPickerEsc(d)
	check(t, "d, Esc: sessions", actSessions(d)(), "a\nb\nboot\nrenamed")

	// Last: b gets a sidebar of its own, whose selection fallback would race the checks above
	actSelect(d, sidebar, "b")
	d.T("send-keys", "-t", sidebar, "Enter")
	actWait(d, "⏎ on b: client session", func() string { return actClientSession(d) }, "b")

	d.T("send-keys", "-t", sidebar, "C-c")
	waitFor(t, "ctrl+c closes the sidebar", func() bool {
		return !slices.Contains(strings.Split(d.T("list-panes", "-a", "-F", "#{pane_id}"), "\n"), sidebar)
	})
}

// Clicks: a session row switches, the current session's row focuses its content pane, × kills its row,
// + on the header opens the picker.
func TestActionMouse(t *testing.T) {
	d := actServer(t, "a", "b", "c")
	actSwitch(d, "a")
	sidebar := actEnsure(d, "a")
	var width int
	if _, err := fmt.Sscan(d.T("display", "-t", sidebar, "-p", "#{pane_width}"), &width); err != nil {
		t.Fatal(err)
	}
	row := func(s string) int {
		t.Helper()
		line, ok := actRow(d, sidebar, s)
		if !ok {
			t.Fatalf("no row for %s:\n%s", s, strings.Join(actScreen(d, sidebar), "\n"))
		}
		return line
	}

	actClick(d, sidebar, 2, row("b"))
	actWait(d, "click on b: client session", func() string { return actClientSession(d) }, "b")
	actWait(d, "click on b: selected", actSelected(d), "b")

	actSwitch(d, "a")
	d.T("select-pane", "-t", sidebar)
	actClick(d, sidebar, 2, row("a"))
	actWait(d, "click on the current session: content pane focused", func() string {
		return fmt.Sprint(actActivePane(d, "a")() != sidebar)
	}, "true")
	check(t, "click on the current session: client stays", actClientSession(d), "a")

	actClick(d, sidebar, width-3, row("c"))
	actWait(d, "× on c: sessions", actSessions(d), "a\nb\nboot")

	actClick(d, sidebar, width-3, 0)
	actPickerEsc(d)
	check(t, "+, Esc: sessions", actSessions(d)(), "a\nb\nboot")
}

// x on a session in a worktree no other session uses asks in a 70×8 popup: Esc cancels, n closes the
// session and keeps the worktree, y removes it too, also when the session is the sidebar's own, which
// dies with it while the popup carries on.
func TestActionKillWorktree(t *testing.T) {
	d := actServer(t, "a")
	repo := newRepo(t, "", "")
	wt := filepath.Join(repo, ".claude/worktrees/x")
	git(t, repo, "worktree", "add", "-q", "-b", "x", wt)
	actSwitch(d, "a")
	sidebar := actEnsure(d, "a")

	newWt := func() {
		t.Helper()
		if exists(wt) == "absent" {
			git(t, repo, "worktree", "add", "-q", wt, "x")
		}
		d.T("new-session", "-d", "-s", "wt", "-c", wt)
		waitScreen(t, d.server, sidebar, "wt")
	}
	ask := func(sidebar, key string) {
		t.Helper()
		actSelect(d, sidebar, "wt")
		d.T("send-keys", "-t", sidebar, "x")
		screen := waitScreen(t, d.outer, "out", "Close session wt?")
		if !strings.Contains(screen, "[y] close + remove worktree x") {
			t.Errorf("❌ popup lacks the worktree prompt:%s", screen)
		}
		d.outer.T("send-keys", "-t", "out", key)
	}

	newWt()
	ask(sidebar, "Escape")
	time.Sleep(time.Second)
	check(t, "Esc: sessions", actSessions(d)(), "a\nboot\nwt")

	ask(sidebar, "n")
	actWait(d, "n: sessions", actSessions(d), "a\nboot")
	check(t, "n: worktree", exists(wt), "present")

	newWt()
	ask(sidebar, "y")
	actWait(d, "y: sessions", actSessions(d), "a\nboot")
	actWait(d, "y: worktree", func() string { return exists(wt) }, "absent")

	newWt()
	actSwitch(d, "wt")
	own := actEnsure(d, "wt")
	ask(own, "y")
	actWait(d, "y on the own session: client moved to the first other session", func() string { return actClientSession(d) }, "a")
	actWait(d, "y on the own session: sessions", actSessions(d), "a\nboot")
	actWait(d, "y on the own session: worktree", func() string { return exists(wt) }, "absent")
	if branches := git(t, repo, "worktree", "list"); slices.ContainsFunc(strings.Split(branches, "\n"), func(l string) bool {
		return strings.HasPrefix(l, wt+" ")
	}) {
		t.Errorf("❌ worktree still registered:\n%s", branches)
	}
}

// A sidebar whose first refresh fails shows the error on its bottom row, then the list once it clears.
// A working directory containing tmux.Sep makes every list-panes row unparsable while its pane exists.
func TestActionErrorBeforeFirstRefresh(t *testing.T) {
	d := actServer(t)
	dir := filepath.Join(tempDir(t), "p\x1fq")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.T("new-session", "-d", "-s", "odd", "-c", dir)
	from := d.T("display", "-t", "=boot:", "-p", "#{pane_id}")
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+from), devBin, "sidebar", "ensure", "boot"); err != nil {
		t.Fatalf("dev sidebar ensure boot: %v\n%s", err, out)
	}
	pane := d.T("show-option", "-qv", "-t", "=boot:", "@sidebar_pane")
	waitScreen(t, d.server, pane, "fields, want")
	screen := actScreen(d, pane)
	check(t, "error on the bottom row", fmt.Sprint(strings.Contains(screen[len(screen)-1], "fields, want")), "true")

	d.T("kill-session", "-t", "=odd:")
	waitScreen(t, d.server, pane, "SESSIONS")
	check(t, "error cleared", fmt.Sprint(strings.Contains(strings.Join(actScreen(d, pane), "\n"), "fields")), "false")
}
