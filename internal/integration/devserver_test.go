//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// devServer is an isolated server set up for project.sh and dev side by side: base-index 1, panes in a
// non-login /bin/sh (a login shell reads /etc/profile and ~/.profile, which put the real binaries ahead of
// the stubs), stubbed claude/lazygit/nvim that log their command line, temp resurrect and state dirs, and
// a client attached to session boot so switch-client works as it does for a person.
type devServer struct {
	*server
	tmp     string
	path    string // PATH with the stubs first
	stubLog string
	outer   *server // its session "out" shows the attached client's terminal
}

// newDevServer starts the server with extraConf appended to the base config.
func newDevServer(t *testing.T, extraConf string) *devServer {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stubLog := filepath.Join(tmp, "stubs.log")
	stubs := writeStubs(t, filepath.Join(tmp, "stubs"),
		`printf '%s %s\n' "$TMUX_PANE" "$(basename "$0") $*" >> "$DEV_STUB_LOG"; exec sleep 600`,
		"claude", "lazygit", "nvim")
	path := stubs + ":" + os.Getenv("PATH")
	s := newServer(t, `set -g base-index 1
setw -g pane-base-index 1
set -g default-shell /bin/sh
set -g default-command /bin/sh
set -g @resurrect-dir '`+tmp+`/resurrect'
`+extraConf, "PATH="+path, "XDG_STATE_HOME="+filepath.Join(tmp, "state"), "DEV_STUB_LOG="+stubLog)
	s.T("new-session", "-d", "-s", "boot")

	out := newServer(t, "")
	out.T("new-session", "-d", "-s", "out", "-x", "200", "-y", "50")
	out.T("send-keys", "-t", "out", "TMUX= tmux -L "+s.socket+" attach -t boot", "Enter")
	waitFor(t, "client attached to boot", func() bool {
		c, _ := s.Try("list-clients", "-F", "#{client_session}")
		return c == "boot"
	})
	return &devServer{server: s, tmp: tmp, path: path, stubLog: stubLog, outer: out}
}

// with returns d reporting to t, for use inside a subtest.
func (d *devServer) with(t *testing.T) *devServer {
	c := *d
	c.server = d.server.with(t)
	return &c
}

// writeProjects writes projects.toml under a new XDG_CONFIG_HOME and returns that env entry.
func writeProjects(t *testing.T, projects ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for _, p := range projects {
		fmt.Fprintf(&b, "[[project]]\nname = %q\npath = %q\n\n", p[0], p[1])
	}
	writeFile(t, filepath.Join(dir, "dev/projects.toml"), b.String())
	return "XDG_CONFIG_HOME=" + dir
}

// waitForStubs waits until session's claude, lazygit and nvim panes run their stub (sleep).
func (d *devServer) waitForStubs(session string) {
	d.t.Helper()
	for _, pane := range []string{":1.2", ":1.3", ":2.1"} {
		waitFor(d.t, "stub in "+session+pane, func() bool {
			cmd, _ := d.Try("display", "-t", "="+session+pane, "-p", "#{pane_current_command}")
			return cmd == "sleep"
		})
	}
}

type snapPane struct {
	Window  int    `tmux:"window_index"`
	Index   int    `tmux:"pane_index"`
	ID      string `tmux:"pane_id"`
	Active  bool   `tmux:"pane_active"`
	Left    int    `tmux:"pane_left"`
	Top     int    `tmux:"pane_top"`
	Width   int    `tmux:"pane_width"`
	Height  int    `tmux:"pane_height"`
	Command string `tmux:"pane_current_command"`
	Start   string `tmux:"pane_start_command"`
	Path    string `tmux:"pane_current_path"`
}

// snapshot describes a session as the parity tests compare it: name, options, viewing clients, windows
// and every pane's geometry, command, start command, path and the stub command line it ran. Each
// replace pair is applied to paths (e.g. a per-run repo dir).
func (d *devServer) snapshot(session string, replace ...string) string {
	d.t.Helper()
	target := tmux.SessionTarget(session)
	sidebar := d.T("show-option", "-qv", "-t", target, "@sidebar_pane")
	clients := d.T("list-clients", "-F", "#{client_session}")
	stubs := map[string]string{}
	log, _ := os.ReadFile(d.stubLog)
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if id, cmd, ok := strings.Cut(line, " "); ok {
			stubs[id] = cmd
		}
	}
	r := strings.NewReplacer(replace...)

	var b strings.Builder
	fmt.Fprintf(&b, "session %s @project=%s clients=%s\n", d.T("display", "-t", target, "-p", "#{session_name}"),
		d.T("show-option", "-qv", "-t", target, "@project"), strings.ReplaceAll(clients, "\n", ","))
	b.WriteString(d.T("list-windows", "-t", target, "-F", "window #{window_index} #{window_name} active=#{window_active} panes=#{window_panes}") + "\n")
	panes, err := tmux.Query[snapPane](d.tmux, "list-panes", "-s", "-t", target)
	if err != nil {
		d.t.Fatal(err)
	}
	for _, p := range panes {
		cmd := p.Command
		// project.sh expands $HOME when it builds the sidebar command, dev leaves it to the pane's shell
		// (tmux shows it escaped)
		p.Start = strings.ReplaceAll(p.Start, `\$HOME`, os.Getenv("HOME"))
		if p.ID == sidebar {
			cmd = "<sidebar>" // the sidebar script's current command changes as it polls tmux
		}
		fmt.Fprintf(&b, "pane %d.%d active=%t %dx%d+%d+%d cmd=%s start=%q path=%s stub=%q\n",
			p.Window, p.Index, p.Active, p.Width, p.Height, p.Left, p.Top, cmd, p.Start, r.Replace(p.Path), stubs[p.ID])
	}
	if sidebar == "" {
		b.WriteString("no @sidebar_pane\n")
	}
	return b.String()
}

// step waits until the pane shows wait, then sends it keys (send-keys arguments). onClient steps use the
// attached client's terminal instead, where a popup shows and takes the keys.
type step struct {
	wait     string
	keys     []string
	onClient bool
	screen   *string // receives the screen that showed wait
}

// runInPane runs argv with env assignments in a new window of boot, so it has a terminal (the picker
// needs one), performs steps, and returns the pane's text once argv exits, plus its exit status.
func (d *devServer) runInPane(env, argv []string, steps ...step) (screen, status string) {
	d.t.Helper()
	words := []string{"env"}
	for _, w := range append(env, argv...) {
		words = append(words, shellQuote(w))
	}
	// A script, since a typed line longer than the tty's input limit (1024 bytes on macOS) gets mangled
	script := filepath.Join(d.t.TempDir(), "run.sh")
	writeFile(d.t, script, "clear\n"+strings.Join(words, " ")+"\necho \"exit=$?\"\n")
	pane := d.T("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=boot:")
	defer d.T("kill-pane", "-t", pane)
	d.T("send-keys", "-t", pane, "sh "+shellQuote(script), "Enter")
	for _, st := range steps {
		s, target := d.server, pane
		if st.onClient {
			s, target = d.outer, "out"
		}
		screen := waitScreen(d.t, s, target, st.wait)
		if st.screen != nil {
			*st.screen = screen
		}
		s.T(append([]string{"send-keys", "-t", target}, st.keys...)...)
	}
	out := waitScreen(d.t, d.server, pane, "\nexit=")
	i := strings.LastIndex(out, "\nexit=")
	status, _, _ = strings.Cut(out[i+len("\nexit="):], "\n")
	return strings.TrimSpace(out[:i]), status
}

// waitScreen waits until pane shows text and returns the screen; the leading newline it adds lets
// "\nexit=" match on the first line.
func waitScreen(t *testing.T, s *server, pane, text string) string {
	t.Helper()
	capture := func() string { return "\n" + s.T("capture-pane", "-p", "-J", "-t", pane) }
	for range 60 {
		if out := capture(); strings.Contains(out, text) {
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("❌ timeout waiting for %q on screen:%s", text, capture())
	return ""
}
