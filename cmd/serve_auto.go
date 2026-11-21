package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/serve"
)

// autoServeEnv is all the detached dev serve keeps of dev boss's environment (not CLAUDECODE or TMUX_PANE); TMUX
// keeps the boss's tmux server.
var autoServeEnv = append([]string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TMUX", "DEV_NO_RELOAD", "DEV_RELOAD_DEVEL"}, serve.LaunchEnv...)

// autoServeDefault is the address dev boss starts dev serve on without DEV_SERVE_ADDR; "": dev serve's own default
// (tailnet, else private LAN, port 8417).
const autoServeDefault = ""

// autoServeAddr is where dev boss starts dev serve: DEV_SERVE_ADDR, else autoServeDefault.
func autoServeAddr(getenv func(string) string) string {
	if a := getenv("DEV_SERVE_ADDR"); a != "" {
		return a
	}
	return autoServeDefault
}

// starter starts dev serve detached for dev boss; its fields are test seams.
type starter struct {
	getenv func(string) string
	uid    int
	dir    string // dev serve's state folder: addr.json, serve.log
	home   string
	exe    string
	served func(dir string, timeout time.Duration) (netip.AddrPort, string, error) // serve.Served
	alive  func(dir string) (bool, error)                                          // serve.AnyAlive
	bind   func(addr string) error
	spawn  func(exe string, args, env []string, log *os.File) (exited <-chan struct{}, err error)
	own    string
	wait   time.Duration
}

// autoServe is dev boss's dev serve: started detached when none answers, unless off; never fails the boss, a
// line on stderr says what went wrong. Never under test: it would start the test binary.
func autoServe(stderr io.Writer, off bool) {
	if off || os.Getenv("DEV_NO_SERVE") != "" || testing.Testing() {
		return
	}
	s, err := newStarter()
	if err == nil {
		err = s.ensure(stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev serve not started: "+strings.ReplaceAll(err.Error(), "\n", ": "))
	}
}

func newStarter() (*starter, error) {
	dir, err := authDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &starter{
		getenv: os.Getenv,
		uid:    os.Getuid(),
		dir:    dir,
		home:   home,
		exe:    exe,
		served: serve.Served,
		alive:  serve.AnyAlive,
		bind:   func(addr string) error { _, _, err := bind(addr); return err },
		spawn:  spawnServe,
		own:    selfreload.Version(),
		wait:   3 * time.Second,
	}, nil
}

// ensure starts dev serve unless one answers, a launchd agent owns it, or this boss runs on a private tmux server
// with no DEV_SERVE_ADDR; it takes at most s.wait (and a health check's last second) in all.
func (s *starter) ensure(stderr io.Writer) error {
	deadline := time.Now().Add(s.wait)
	served := func(most time.Duration) (netip.AddrPort, string, error) {
		return s.served(s.dir, min(most, max(time.Until(deadline), 50*time.Millisecond)))
	}
	addr := autoServeAddr(s.getenv)
	if s.getenv("DEV_SERVE_ADDR") == "" && !s.defaultServer() {
		return nil
	}
	if _, v, err := served(time.Second); err == nil {
		if v != "" && v != s.own {
			fmt.Fprintf(stderr, "⚠ dev serve %s runs (this dev is %s): restart it to update, unless it reloads itself\n", v, s.own)
		}
		return nil
	} else if !errors.Is(err, serve.ErrNotRunning) {
		return err
	}
	// One that runs but doesn't answer (its recorded tailnet address gone, hung) would be a second
	if alive, err := s.alive(s.dir); err != nil {
		return err
	} else if alive {
		fmt.Fprintf(stderr, "⚠ dev serve runs but doesn't answer on its address (see %s): not starting another\n", filepath.Join(s.dir, "serve.log"))
		return nil
	}
	if _, err := os.Stat(serve.PlistPath(s.home)); err == nil {
		fmt.Fprintf(stderr, "⚠ dev serve: launchd agent installed but not answering: launchctl kickstart gui/%d/%s\n", s.uid, serve.Label)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.bind(addr); err != nil {
		if addr != "" {
			return errors.New(strings.Replace(err.Error(), "--addr", "DEV_SERVE_ADDR", 1))
		}
		return fmt.Errorf("%w (dev boss: set DEV_SERVE_ADDR=ip:port, or DEV_NO_SERVE=1)", err)
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil && ap.Port() == 0 {
		return errors.New("DEV_SERVE_ADDR: port 0 picks a new port each start, so each dev boss would start another: give one")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(s.dir, "serve.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	start, err := log.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	args := []string{"serve", "--awake=false"}
	if addr != "" {
		args = append(args, "--addr", addr)
	}
	var env []string
	for _, k := range autoServeEnv {
		if v := s.getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	exited, err := s.spawn(s.exe, args, env, log)
	if err != nil {
		return err
	}
	ended := false
	for time.Now().Before(deadline) {
		// After an exit too: a second dev boss's dev serve may have taken the port first
		if ap, _, err := served(300 * time.Millisecond); err == nil {
			if !ended {
				fmt.Fprintf(stderr, "dev serve started: http://%s/ (pair this browser: dev serve pair)\n", ap)
				if _, kind, err := serve.Check(ap.String()); err == nil && kind == serve.LAN {
					fmt.Fprintln(stderr, "⚠ "+serve.LANNotice)
				}
			}
			return nil
		}
		select {
		case <-exited:
			ended, exited = true, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	if ended {
		return fmt.Errorf("%s (log: %s)", lastLine(logPath, start), logPath)
	}
	fmt.Fprintf(stderr, "⚠ dev serve: started, not answering after %s: %s (log: %s)\n", s.wait, lastLine(logPath, start), logPath)
	return nil
}

// defaultServer: dev boss talks to tmux's default server (no TMUX_TMPDIR; $TMUX, when set, its default socket).
func (s *starter) defaultServer() bool {
	if s.getenv("TMUX_TMPDIR") != "" {
		return false
	}
	t := s.getenv("TMUX")
	if t == "" {
		return true
	}
	socket, _, _ := strings.Cut(t, ",")
	want := filepath.Join("/tmp", "tmux-"+strconv.Itoa(s.uid), "default")
	return socket == want || socket == "/private"+want
}

// lastLine is the last line written to path after offset from, without its "❌ "; "exited" when none.
func lastLine(path string, from int64) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	b = b[min(from, int64(len(b))):]
	lines := strings.Split(strings.TrimSpace(string(bytes.ToValidUTF8(b, nil))), "\n")
	if l := strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "❌ "); l != "" {
		return l
	}
	return "exited"
}

// spawnServe starts exe args in its own session (no SIGHUP from the terminal or tmux), output to log; exited
// closes when it ends while this process still runs.
func spawnServe(exe string, args, env []string, log *os.File) (<-chan struct{}, error) {
	c := exec.Command(exe, args...)
	c.Env, c.Stdout, c.Stderr = env, log, log
	c.Dir = "/" // not the boss's folder: a worktree it opened in may be removed while it runs
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	exited := make(chan struct{})
	go func() { _ = c.Wait(); close(exited) }()
	return exited, nil
}
