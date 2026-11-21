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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/serve"
)

// autoServeEnv is all the detached dev serve keeps of dev boss's environment (not CLAUDECODE, TMUX or TMUX_PANE).
var autoServeEnv = append([]string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "DEV_NO_RELOAD", "DEV_RELOAD_DEVEL"}, serve.LaunchEnv...)

// autoServeDefault is the address dev boss starts dev serve on without DEV_SERVE_ADDR: this Mac only (the user's
// pick); "" would be dev serve's own default (tailnet, else private LAN), which a hand-run dev serve keeps.
const autoServeDefault = "127.0.0.1:8417"

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
	dir    string // dev serve's state folder: address records, serve.log
	home   string
	exe    string
	served func(dir string, timeout time.Duration) ([]serve.Running, error) // serve.Served on this Mac's addresses
	alive  func(dir string) (bool, error)                                   // serve.AnyAlive
	bind   func(addr string) error
	spawn  func(exe string, args, env []string, log, hold *os.File) (exited <-chan struct{}, err error)
	own    string
	wait   time.Duration
}

// autoServe is dev boss's dev serve: started detached when none answers, unless off; never fails the boss, a
// line on stderr says what went wrong. Never under test: it would start the test binary.
func autoServe(stderr io.Writer, off bool) {
	if off || os.Getenv("DEV_NO_SERVE") == "1" || testing.Testing() {
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
		served: func(dir string, timeout time.Duration) ([]serve.Running, error) {
			return serve.Served(dir, ownIP, timeout)
		},
		alive: serve.AnyAlive,
		bind:  func(addr string) error { _, _, err := bind(addr); return err },
		spawn: spawnServe,
		own:   selfreload.Version(),
		wait:  3 * time.Second,
	}, nil
}

// ensure starts dev serve unless one answers, a launchd agent owns it, or this boss runs on a private tmux server
// with no DEV_SERVE_ADDR; it takes at most s.wait (and a health check's last second) in all.
func (s *starter) ensure(stderr io.Writer) error {
	deadline := time.Now().Add(s.wait)
	served := func(most time.Duration) ([]serve.Running, error) {
		return s.served(s.dir, min(most, max(time.Until(deadline), 300*time.Millisecond)))
	}
	addr := autoServeAddr(s.getenv)
	if s.getenv("DEV_SERVE_ADDR") == "" && !s.defaultServer() {
		if _, err := served(time.Second); err == nil {
			return nil
		}
		return errors.New("this boss's tmux server isn't the default one: set DEV_SERVE_ADDR to start it for this one")
	}
	hold, err := startLock(s.dir, deadline)
	if err != nil {
		return err
	}
	if hold == nil {
		fmt.Fprintln(stderr, "⚠ dev serve: another dev boss is still starting it")
		return nil
	}
	defer func() { _ = hold.Close() }()
	live, err := served(time.Second)
	if err == nil {
		warned := map[string]bool{}
		for _, r := range live {
			if r.Version != "" && r.Version != s.own && !warned[r.Version] {
				warned[r.Version] = true
				fmt.Fprintf(stderr, "⚠ dev serve %s runs (this dev is %s): restart it to update, unless it reloads itself\n", r.Version, s.own)
			}
		}
		return nil
	} else if !errors.Is(err, serve.ErrNotRunning) {
		return err
	}
	// One that runs but doesn't answer (its recorded address gone, hung) would be a second
	if alive, err := s.alive(s.dir); err != nil {
		return err
	} else if alive {
		fmt.Fprintf(stderr, "⚠ dev serve runs but doesn't answer on its address (see %s): not starting another\n", filepath.Join(s.dir, "serve.log"))
		return nil
	}
	if _, err := os.Stat(serve.PlistPath(s.home)); err == nil {
		fmt.Fprintf(stderr, "⚠ dev serve: launchd agent installed but not answering: launchctl kickstart gui/%[1]d/%[2]s (not loaded: launchctl bootstrap gui/%[1]d %[3]q)\n",
			s.uid, serve.Label, serve.PlistPath(s.home))
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.bind(addr); err != nil {
		switch {
		case s.getenv("DEV_SERVE_ADDR") != "":
			return errors.New(strings.Replace(err.Error(), "--addr", "DEV_SERVE_ADDR", 1))
		case addr == "": // only with autoServeDefault "" (dev serve's own default)
			return fmt.Errorf("%w (dev boss: set DEV_SERVE_ADDR=ip:port, or DEV_NO_SERVE=1)", err)
		}
		return err
	}
	want, err := netip.ParseAddrPort(addr)                      // "": dev serve's own default, any address
	want = netip.AddrPortFrom(want.Addr().Unmap(), want.Port()) // as dev serve records it
	if err == nil && want.Port() == 0 {
		return errors.New("DEV_SERVE_ADDR: port 0 picks a new port each start, so each dev boss would start another: give one")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(s.dir, "serve.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 1<<20 { // none runs: nothing writes it now
		if err := os.Truncate(logPath, 0); err != nil {
			return err
		}
	}
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
		v := s.getenv(k)
		if v == "" {
			continue
		}
		// It runs in /: a relative folder would be another one there
		if strings.HasSuffix(k, "_HOME") || strings.HasSuffix(k, "_DIR") || strings.HasSuffix(k, "TMPDIR") {
			if v, err = filepath.Abs(v); err != nil {
				return err
			}
		}
		env = append(env, k+"="+v)
	}
	// It reads the boss's tmux server (TMUX before TMUX_TMPDIR, as tmux does): its socket, no session of its own
	if t := s.getenv("TMUX"); t != "" {
		socket, _, _ := strings.Cut(t, ",")
		if socket, err = filepath.Abs(socket); err != nil {
			return err
		}
		env = append(env, "TMUX="+socket+",0,-1")
	}
	exited, err := s.spawn(s.exe, args, env, log, hold)
	if err != nil {
		return err
	}
	if least := time.Now().Add(time.Second); deadline.Before(least) { // its turn came late: still give it a second
		deadline = least
	}
	ended := false
	for time.Now().Before(deadline) {
		// After an exit too: a second dev boss's dev serve may have taken the port first
		live, err := served(300 * time.Millisecond)
		i := slices.IndexFunc(live, func(r serve.Running) bool { return addr == "" || r.Addr == want })
		if err == nil && i >= 0 {
			if !ended {
				ap := live[i].Addr
				hint := "pair a phone: dev serve pair"
				if ap.Addr().IsLoopback() {
					hint = "this Mac only, no pairing needed"
				}
				fmt.Fprintf(stderr, "dev serve started: http://%s/ (%s)\n", ap, hint)
				if _, kind, err := serve.Check(ap.String()); err == nil && kind == serve.LAN {
					fmt.Fprintln(stderr, "⚠ "+serve.LANNotice)
				}
			}
			return nil
		}
		select {
		case <-exited:
			ended, exited = true, nil
			if grace := time.Now().Add(time.Second); grace.Before(deadline) {
				deadline = grace
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if ended {
		return fmt.Errorf("%s (log: %s)", lastLine(logPath, start, "exited"), logPath)
	}
	fmt.Fprintf(stderr, "⚠ dev serve: started, not answering after %s: %s (log: %s)\n", s.wait, lastLine(logPath, start, "no output yet"), logPath)
	return nil
}

// startLock takes dir/start, so two dev bosses don't both start one; nil once deadline passes first. The dev serve
// it starts inherits it (envStartFd) and holds it until it holds its alive lock.
func startLock(dir string, deadline time.Time) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "start"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return f, nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
		case time.Now().After(deadline):
			_ = f.Close()
			return nil, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// envStartFd names the start lock's descriptor in a dev serve a dev boss started.
const envStartFd = "DEV_SERVE_START_FD"

// defaultServer: dev boss talks to tmux's default server (TMUX_TMPDIR unset or /tmp; $TMUX, when set, its default socket).
func (s *starter) defaultServer() bool {
	if t := s.getenv("TMUX"); t != "" { // tmux takes its socket from $TMUX before TMUX_TMPDIR
		socket, _, _ := strings.Cut(t, ",")
		want := filepath.Join("/tmp", "tmux-"+strconv.Itoa(s.uid), "default")
		return socket == want || socket == "/private"+want
	}
	d := strings.TrimSuffix(s.getenv("TMUX_TMPDIR"), "/")
	return d == "" || d == "/tmp" || d == "/private/tmp"
}

// lastLine is the last line written to path after offset from, without its "❌ "; none when there is none.
func lastLine(path string, from int64, none string) string {
	f, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return err.Error()
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimSpace(string(bytes.ToValidUTF8(b, nil))), "\n")
	if l := strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "❌ "); l != "" {
		return l
	}
	return none
}

// spawnServe starts exe args in its own session (no SIGHUP from the terminal or tmux), output to log, holding hold
// as fd 3; exited closes when it ends while this process still runs.
func spawnServe(exe string, args, env []string, log, hold *os.File) (<-chan struct{}, error) {
	c := exec.Command(exe, args...)
	c.Env, c.Stdout, c.Stderr = append(env, envStartFd+"=3"), log, log
	c.ExtraFiles = []*os.File{hold} // fd 3
	c.Dir = "/"                     // not the boss's folder: a worktree it opened in may be removed while it runs
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	exited := make(chan struct{})
	go func() { _ = c.Wait(); close(exited) }()
	return exited, nil
}
