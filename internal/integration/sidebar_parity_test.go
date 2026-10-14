//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parSide is one sidebar implementation on its own actServer: bash (install-sidebar-keys.sh's root
// bindings, sidebar.sh started by toggle-sidebar.sh) or dev (`dev sidebar ensure`, no bindings, the
// sidebar reads its own keys). Keys and clicks go through the attached client, the only way the bash
// bindings see them (send-keys to a pane bypasses the key tables).
type parSide struct {
	name   string
	d      *devServer
	bash   bool
	action string // sidebar-action.sh
	toggle string // toggle-sidebar.sh
}

// parSides starts the bash and the dev side, each with sessions besides boot and the client on the
// first. Bash's picker popup runs select.sh, which lists the user's ~/.tmux/sessions/projects.conf (read
// only; the tests close it with Esc).
func parSides(t *testing.T, sessions ...string) []*parSide {
	t.Helper()
	home := tmuxHome(t)
	keys := filepath.Join(home, "sessions/install-sidebar-keys.sh")
	action := filepath.Join(home, "sessions/sidebar-action.sh")
	toggle := filepath.Join(home, "sessions/toggle-sidebar.sh")
	requireFiles(t, keys, action, toggle, filepath.Join(home, "sessions/sidebar.sh"), filepath.Join(home, "sessions/select.sh"))
	requireExported(t, map[string]string{
		"tmux/sessions/install-sidebar-keys.sh": ".tmux/sessions/install-sidebar-keys.sh",
		"tmux/sessions/sidebar-action.sh":       ".tmux/sessions/sidebar-action.sh",
		"tmux/sessions/toggle-sidebar.sh":       ".tmux/sessions/toggle-sidebar.sh",
		"tmux/sessions/sidebar.sh":              ".tmux/sessions/sidebar.sh",
	})
	var sides []*parSide
	for _, bash := range []bool{true, false} {
		s := &parSide{name: "dev", d: actServer(t, sessions...), bash: bash, action: action, toggle: toggle}
		// Long enough to read a refusal off the client's status line
		s.d.T("set-option", "-g", "display-time", "10000")
		if bash {
			s.name = "bash"
			if out, err := runScript(s.d.scriptEnv(), keys); err != nil {
				t.Fatalf("install-sidebar-keys.sh: %v\n%s", err, out)
			}
		}
		actSwitch(s.d, sessions[0])
		sides = append(sides, s)
	}
	return sides
}

// with returns s reporting to t.
func (s *parSide) with(t *testing.T) *parSide {
	c := *s
	c.d = s.d.with(t)
	return &c
}

// ensure shows session's sidebar from its active pane, as the hooks do, and returns the sidebar pane
// once it lists every session.
func (s *parSide) ensure(session string) string {
	d := s.d
	d.t.Helper()
	from := d.T("display", "-t", "="+session+":", "-p", "#{pane_id}")
	name, args := s.toggle, []string{session}
	if !s.bash {
		name, args = devBin, []string{"sidebar", "ensure", session}
	}
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+from), name, args...); err != nil {
		d.t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, out)
	}
	pane := d.T("show-option", "-qv", "-t", "="+session+":", "@sidebar_pane")
	if pane == "" {
		d.t.Fatalf("no sidebar in %s", session)
	}
	for _, name := range strings.Split(d.T("list-sessions", "-F", "#{session_name}"), "\n") {
		waitScreen(d.t, d.server, pane, name)
	}
	return pane
}

// press types keys (send-keys names) into the attached client.
func (s *parSide) press(keys ...string) {
	s.d.outer.T(append([]string{"send-keys", "-t", "out"}, keys...)...)
}

// brief is what a step must reach on each side: selection, the client's session, whether its active
// pane is the sidebar, the sessions.
func parBrief(d *devServer) string {
	client := actClientSession(d)
	focus := "content"
	if d.T("display", "-t", "="+client+":", "-p", "#{pane_id}") == d.T("show-option", "-qv", "-t", "="+client+":", "@sidebar_pane") {
		focus = "sidebar"
	}
	return fmt.Sprintf("selected=%s client=%s focus=%s sessions=%s", actSelected(d)(), client, focus,
		strings.ReplaceAll(actSessions(d)(), "\n", ","))
}

// parStep runs do on each side, waits there until parBrief is want, then compares the sides' full state.
func parStep(t *testing.T, sides []*parSide, name, want string, do func(s *parSide)) {
	t.Helper()
	for _, s := range sides {
		do(s.with(t))
		actWait(s.d.with(t), s.name+": "+name, func() string { return parBrief(s.d) }, want)
	}
	// Let both sidebars poll (bash every 120ms, refresh every second) so their selection sync settles
	time.Sleep(time.Second)
	check(t, name+" (dev = bash)", ensState(sides[1].d), ensState(sides[0].d))
}

// parMessage waits for msg on the client's status line.
func parMessage(s *parSide, msg string) {
	s.d.t.Helper()
	waitScreen(s.d.t, s.d.outer, "out", msg)
}

// parClickFocus checks the one difference a click on a sidebar's × or + makes: on dev, tmux's default
// MouseDown1Pane binding focuses the sidebar before it sees the click, where bash's root binding ran the
// action without focusing. It then focuses the content pane again, as bash left it.
func parClickFocus(s *parSide, sidebar string) {
	if s.bash {
		return
	}
	d := s.d
	d.t.Helper()
	check(d.t, "dev: the click focused the sidebar", d.T("display", "-t", sidebar, "-p", "#{pane_active}"), "1")
	d.T("last-pane", "-t", sidebar)
}

// The sidebar's keys on bash and dev: j/k/↑/↓ wrap, ⏎ on the current session focuses its content and on
// another switches, q focuses the content, x kills, r renames through the client's prompt, d opens the
// picker, x on the current session switches to the next one first, and the last session is refused.
func TestSidebarKeysParity(t *testing.T) {
	sides := parSides(t, "alpha", "beta", "gamma")
	sidebar := map[string]string{} // by side name
	parStep(t, sides, "ensure alpha", "selected=alpha client=alpha focus=sidebar sessions=alpha,beta,boot,gamma", func(s *parSide) {
		sidebar[s.name] = s.ensure("alpha")
	})
	for _, k := range []struct{ key, want string }{
		{"j", "beta"}, {"j", "boot"}, {"j", "gamma"}, {"j", "alpha"}, {"k", "gamma"}, {"Down", "alpha"}, {"Up", "gamma"}, {"Up", "boot"},
	} {
		parStep(t, sides, k.key, "selected="+k.want+" client=alpha focus=sidebar sessions=alpha,beta,boot,gamma", func(s *parSide) {
			s.press(k.key)
		})
	}

	parStep(t, sides, "⏎ on the current session", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot,gamma", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "alpha")
		s.press("Enter")
	})
	parStep(t, sides, "q", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot,gamma", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "beta")
		s.press("q")
	})
	parStep(t, sides, "x on another session", "selected=alpha client=alpha focus=sidebar sessions=alpha,beta,boot", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "gamma")
		s.press("x")
	})
	parStep(t, sides, "r", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot,renamed", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "beta")
		s.press("r")
		waitScreen(s.d.t, s.d.outer, "out", "Rename session:")
		s.press("C-u")
		s.press("-l", "renamed")
		s.press("Enter")
	})
	parStep(t, sides, "d, Esc", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot,renamed", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "alpha")
		s.press("d")
		actPickerEsc(s.d)
		s.d.T("select-pane", "-t", sidebar[s.name])
	})
	parStep(t, sides, "⏎ on another session", "selected=renamed client=renamed focus=sidebar sessions=alpha,boot,renamed", func(s *parSide) {
		actSelect(s.d, sidebar[s.name], "renamed")
		s.press("Enter")
	})
	parStep(t, sides, "x on the current session", "selected=boot client=boot focus=content sessions=boot,renamed", func(s *parSide) {
		actSwitch(s.d, "alpha")
		actSelect(s.d, sidebar[s.name], "alpha")
		s.press("x")
	})

	parStep(t, sides, "x on the last session", "selected=boot client=boot focus=sidebar sessions=boot", func(s *parSide) {
		s.d.T("kill-session", "-t", "=renamed:")
		own := s.ensure("boot")
		actSelect(s.d, own, "boot")
		s.press("x")
		parMessage(s, "Refusing to kill the last tmux session")
	})
}

// Clicks on bash and dev: a session row switches, the current session's row focuses its content, ×
// kills its row, + on the header opens the picker; prefix+0's `new` opens it too.
func TestSidebarMouseParity(t *testing.T) {
	sides := parSides(t, "alpha", "beta", "gamma")
	sidebar := map[string]string{} // by side name
	parStep(t, sides, "ensure alpha", "selected=alpha client=alpha focus=sidebar sessions=alpha,beta,boot,gamma", func(s *parSide) {
		sidebar[s.name] = s.ensure("alpha")
	})
	row := func(s *parSide, session string) int {
		line, ok := actRow(s.d, sidebar[s.name], session)
		if !ok {
			s.d.t.Fatalf("no row for %s:\n%s", session, strings.Join(actScreen(s.d, sidebar[s.name]), "\n"))
		}
		return line
	}
	const width = 40
	parStep(t, sides, "click on beta", "selected=beta client=beta focus=sidebar sessions=alpha,beta,boot,gamma", func(s *parSide) {
		actClick(s.d, sidebar[s.name], 2, row(s, "beta"))
	})
	parStep(t, sides, "click below alpha's name row", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot,gamma", func(s *parSide) {
		actSwitch(s.d, "alpha")
		s.d.T("select-pane", "-t", sidebar[s.name])
		actClick(s.d, sidebar[s.name], 6, row(s, "alpha")+1)
	})
	parStep(t, sides, "× on gamma", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot", func(s *parSide) {
		actClick(s.d, sidebar[s.name], width-3, row(s, "gamma"))
		waitFor(s.d.t, "gamma killed", func() bool { return actSessions(s.d)() == "alpha\nbeta\nboot" })
		parClickFocus(s, sidebar[s.name])
	})
	parStep(t, sides, "+, Esc", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot", func(s *parSide) {
		actClick(s.d, sidebar[s.name], width-3, 0)
		actPickerEsc(s.d)
		parClickFocus(s, sidebar[s.name])
	})
	parStep(t, sides, "new, Esc", "selected=alpha client=alpha focus=content sessions=alpha,beta,boot", func(s *parSide) {
		tty := s.d.T("list-clients", "-F", "#{client_tty}")
		pane := s.d.T("display", "-t", "=alpha:", "-p", "#{pane_id}")
		name, args := s.action, []string{"new", "alpha", pane, tty}
		if !s.bash {
			name, args = devBin, []string{"new", "--client", tty}
		}
		// Blocks while the popup is open, as run-shell from prefix+0 does
		done := make(chan error, 1)
		go func() {
			_, err := runScript(s.d.scriptEnv("TMUX_PANE="+pane), name, args...)
			done <- err
		}()
		actPickerEsc(s.d)
		if err := <-done; err != nil {
			s.d.t.Errorf("❌ %s new: %v", s.name, err)
		}
	})
}

// x on a session in a worktree no other session uses, on bash and dev: the 70×8 popup, where Esc cancels,
// n closes the session and keeps the worktree, y removes it too, also from the session's own sidebar.
func TestSidebarKillWorktreeParity(t *testing.T) {
	sides := parSides(t, "alpha")
	type world struct{ sidebar, wt, repo string }
	w := map[string]*world{} // by side name
	parStep(t, sides, "ensure alpha", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot", func(s *parSide) {
		repo := newRepo(s.d.t, "", "")
		wt := filepath.Join(repo, ".claude/worktrees/x")
		git(s.d.t, repo, "worktree", "add", "-q", "-b", "x", wt)
		w[s.name] = &world{sidebar: s.ensure("alpha"), wt: wt, repo: repo}
	})
	brief := func(s *parSide) string { return parBrief(s.d) + " worktree=" + exists(w[s.name].wt) }
	newWt := func(s *parSide) {
		if exists(w[s.name].wt) == "absent" {
			git(s.d.t, w[s.name].repo, "worktree", "add", "-q", w[s.name].wt, "x")
		}
		s.d.T("new-session", "-d", "-s", "wt", "-c", w[s.name].wt)
		waitScreen(s.d.t, s.d.server, w[s.name].sidebar, "wt")
	}
	ask := func(s *parSide, sidebar, key string) {
		actSelect(s.d, sidebar, "wt")
		s.press("x")
		screen := waitScreen(s.d.t, s.d.outer, "out", "Close session wt?")
		if !strings.Contains(screen, "[y] close + remove worktree x") {
			s.d.t.Errorf("❌ %s popup lacks the worktree prompt:%s", s.name, screen)
		}
		s.press(key)
	}
	step := func(name, want string, do func(s *parSide)) {
		t.Helper()
		for _, s := range sides {
			do(s.with(t))
			actWait(s.d.with(t), s.name+": "+name, func() string { return brief(s) }, want)
		}
		time.Sleep(time.Second)
		check(t, name+" (dev = bash)", ensState(sides[1].d), ensState(sides[0].d))
	}

	step("Esc", "selected=wt client=alpha focus=sidebar sessions=alpha,boot,wt worktree=present", func(s *parSide) {
		newWt(s)
		ask(s, w[s.name].sidebar, "Escape")
		waitFor(s.d.t, "popup closed", func() bool {
			return !strings.Contains(s.d.outer.T("capture-pane", "-p", "-t", "out"), "Close session wt?")
		})
	})
	step("n", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot worktree=present", func(s *parSide) {
		ask(s, w[s.name].sidebar, "n")
	})
	step("y", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot worktree=absent", func(s *parSide) {
		newWt(s)
		ask(s, w[s.name].sidebar, "y")
	})
	step("y on the own session", "selected=alpha client=alpha focus=sidebar sessions=alpha,boot worktree=absent", func(s *parSide) {
		newWt(s)
		actSwitch(s.d, "wt")
		ask(s, s.ensure("wt"), "y")
	})
}
