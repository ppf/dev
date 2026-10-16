//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// ensSide is one implementation of the sidebar flows on its own server: bash (toggle-sidebar.sh,
// sidebar-action.sh goto) or dev.
type ensSide struct {
	d      *devServer
	bash   bool
	toggle string // bash scripts
	action string
	boot   string // boot's first pane, the content pane flows start from
}

// ensNewSide starts a server with sessions alpha and beta (two panes each) beside the attached boot.
func ensNewSide(t *testing.T, bash bool) *ensSide {
	t.Helper()
	s := &ensSide{bash: bash}
	if bash {
		home := tmuxHome(t)
		s.toggle, s.action = filepath.Join(home, "sessions/toggle-sidebar.sh"), filepath.Join(home, "sessions/sidebar-action.sh")
	}
	s.d = newDevServer(t, "")
	for _, name := range []string{"alpha", "beta"} {
		s.d.T("new-session", "-d", "-s", name, "-x", "200", "-y", "50")
		s.d.T("split-window", "-h", "-t", tmux.SessionTarget(name))
	}
	s.boot = s.d.T("display", "-t", "=boot:1.1", "-p", "#{pane_id}")
	return s
}

// run runs one flow with the pane env a key binding in pane would give it: "toggle", "ensure <session>"
// or "goto <n>" (for the attached client, as prefix+n passes #{client_tty}).
func (s *ensSide) run(t *testing.T, pane string, op ...string) {
	t.Helper()
	d := s.d.with(t)
	client := d.T("list-clients", "-F", "#{client_tty}")
	var name string
	var args []string
	switch {
	case s.bash && op[0] == "goto":
		session := d.T("display", "-t", pane, "-p", "#{session_name}")
		name, args = s.action, []string{"goto", session, pane, client, op[1]}
	case s.bash:
		name, args = s.toggle, op[1:]
	case op[0] == "goto":
		name, args = devBin, []string{"goto", op[1], "--client", client}
	default:
		name, args = devBin, append([]string{"sidebar"}, op...)
	}
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+pane), name, args...); err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, out)
	}
}

// sidebar is session's @sidebar_pane.
func (s *ensSide) sidebar(session string) string {
	return s.d.T("show-option", "-qv", "-t", tmux.SessionTarget(session), "@sidebar_pane")
}

// ensState is what the parity steps compare: the global sidebar options, the client's session, and per
// session whether a sidebar is tracked and every pane's geometry, focus and zoom. Pane ids and start
// commands differ by design and are left out; the sidebar is marked instead.
func ensState(d *devServer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hidden=%q selected=%q order=%q client=%s\n", d.T("show-option", "-gqv", "@sidebar_hidden"),
		d.T("show-option", "-gqv", "@sidebar_selected_session"), d.T("show-option", "-gqv", "@sidebar_session_order"),
		d.T("list-clients", "-F", "#{client_session}"))
	for _, s := range strings.Fields(d.T("list-sessions", "-F", "#{session_name}")) {
		target := tmux.SessionTarget(s)
		fmt.Fprintf(&b, "%s tracked=%t\n", s, d.T("show-option", "-qv", "-t", target, "@sidebar_pane") != "")
		b.WriteString(d.T("list-panes", "-s", "-t", target, "-F",
			"  #{window_index}.#{pane_index} active=#{pane_active} zoomed=#{window_zoomed_flag} "+
				"#{pane_width}x#{pane_height}+#{pane_left}+#{pane_top} sidebar=#{==:#{pane_id},#{@sidebar_pane}}") + "\n")
	}
	return b.String()
}

// bash's sidebar-action.sh reads the @sidebar_session_order a dev sidebar wrote: j moves to the next
// session in dev's grouped order (zeta joins alpha's group), not list-sessions' alphabetical one.
func TestSessionOrderReadByBash(t *testing.T) {
	action := filepath.Join(tmuxHome(t), "sessions/sidebar-action.sh")
	d := actServer(t, "alpha", "beta", "zeta")
	d.T("set-option", "-t", "=zeta:", "@project", "alpha")
	sidebar := actEnsure(d, "alpha")
	actWait(d, "dev's order", func() string { return d.T("show-option", "-gqv", "@sidebar_session_order") },
		"alpha\nzeta\nbeta\nboot\n") // each name newline-terminated, as sidebar.sh writes it

	d.T("set-option", "-g", "@sidebar_selected_session", "alpha")
	client := d.T("list-clients", "-F", "#{client_tty}")
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+sidebar), action, "down", "alpha", sidebar, client); err != nil {
		t.Fatalf("sidebar-action.sh down: %v\n%s", err, out)
	}
	actWait(d, "bash moved in dev's order", actSelected(d), "zeta")
}

// The sidebar flows through toggle-sidebar.sh / sidebar-action.sh goto and through dev on twin servers,
// compared after every step: create in another session, create and focus in the same one, hand focus
// back from the sidebar, focus it again, hide, ensure while hidden, show, goto, and compact's zoom.
func TestEnsureParity(t *testing.T) {
	sides := []*ensSide{ensNewSide(t, true), ensNewSide(t, false)}

	// from picks the pane a step runs from on each side
	content := func(s *ensSide) string { return s.boot }
	bootSidebar := func(s *ensSide) string { return s.sidebar("boot") }
	active := func(s *ensSide) string {
		return s.d.T("display", "-t", tmux.SessionTarget(s.d.T("list-clients", "-F", "#{client_session}")), "-p", "#{pane_id}")
	}
	zoomAlpha := func(s *ensSide) string {
		s.d.T("resize-pane", "-Z", "-t", "=alpha:1.2")
		return s.boot
	}
	steps := []struct {
		name string
		from func(*ensSide) string
		op   []string
	}{
		{"ensure another session", content, []string{"ensure", "alpha"}},
		{"ensure own session", content, []string{"ensure", "boot"}},
		{"ensure from the sidebar hands focus back", bootSidebar, []string{"ensure", "boot"}},
		{"ensure focuses the existing sidebar", content, []string{"ensure", "boot"}},
		{"toggle hides every sidebar", content, []string{"toggle"}},
		{"ensure while hidden", content, []string{"ensure", "alpha"}},
		{"toggle shows", content, []string{"toggle"}},
		{"goto 2", active, []string{"goto", "2"}},
		{"goto 3 back", active, []string{"goto", "3"}},
		{"goto out of range", active, []string{"goto", "9"}},
		{"ensure alpha again", content, []string{"ensure", "alpha"}},
		{"ensure keeps a compact zoom", zoomAlpha, []string{"ensure", "alpha"}},
		{"hiding keeps a compact zoom", content, []string{"toggle"}},
	}
	for _, st := range steps {
		for _, s := range sides {
			s.run(t, st.from(s), st.op...)
		}
		// Let both sidebars poll (120ms) so their selection sync settles
		time.Sleep(time.Second)
		check(t, st.name, ensState(sides[1].d), ensState(sides[0].d))
		if st.name == "ensure keeps a compact zoom" {
			check(t, "dev keeps the zoom on ensure", sides[1].d.T("display", "-t", "=alpha:1", "-p", "#{window_zoomed_flag}"), "1")
		}
	}
	check(t, "dev keeps the zoom on hiding", sides[1].d.T("display", "-t", "=alpha:1", "-p", "#{window_zoomed_flag}"), "1")
}

// A bash sidebar (start command running sidebar.sh) is replaced by dev's, keeping a compact zoom until
// the new split, as toggle-sidebar.sh's create path does.
func TestEnsureReplacesBashSidebar(t *testing.T) {
	s := ensNewSide(t, false)
	d := s.d
	old := d.T("split-window", "-hb", "-l", "40", "-t", "=alpha:1.1", "-PF", "#{pane_id}", "exec sleep 600 # sidebar.sh")
	d.T("set-option", "-t", "=alpha:", "@sidebar_pane", old)

	s.run(t, s.boot, "ensure", "alpha")
	time.Sleep(time.Second)
	cur := s.sidebar("alpha")
	if cur == "" || cur == old {
		t.Fatalf("❌ @sidebar_pane = %q, want a new pane replacing %s", cur, old)
	}
	check(t, "bash sidebar killed", d.T("list-panes", "-a", "-F", "#{pane_id}", "-f", "#{==:#{pane_id},"+old+"}"), "")
	start := d.T("display", "-t", cur, "-p", "#{pane_start_command}")
	check(t, "dev sidebar runs dev", devSidebarStart(start), "true")
	check(t, "dev sidebar alive, first and 40 wide", d.T("display", "-t", cur, "-p", "#{pane_dead} #{pane_index} #{pane_width}"), "0 1 40")
	check(t, "focus stays in boot", d.T("display", "-t", "=boot:", "-p", "#{pane_id}"), s.boot)
}
