//go:build integration

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

var socketSeq atomic.Int32

// killServers kills the test servers (dev-it-<pid>-<n>) whose owning pid matches and removes their sockets.
func killServers(owned func(pid int) bool) {
	sockets, _ := filepath.Glob(filepath.Join(socketDir(), "dev-it-*-*"))
	for _, sock := range sockets {
		var pid, n int
		if _, err := fmt.Sscanf(filepath.Base(sock), "dev-it-%d-%d", &pid, &n); err != nil || !owned(pid) {
			continue
		}
		tmux.NewIsolated(filepath.Base(sock), "").Run("kill-server")
		os.Remove(sock)
	}
}

// server is an isolated tmux server (-L) owned by one test; it is killed and its socket removed on cleanup.
type server struct {
	t      *testing.T
	tmux   *tmux.Client
	socket string
}

// newServer prepares a server with config conf, HOME=bashHome and extra env (also passed to scripts via
// scriptEnv). The server itself starts with the first command that needs it.
func newServer(t *testing.T, conf string, env ...string) *server {
	t.Helper()
	confPath := filepath.Join(t.TempDir(), "test.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("dev-it-%d-%d", os.Getpid(), socketSeq.Add(1))
	s := &server{t: t, tmux: tmux.NewIsolated(socket, confPath, append([]string{"HOME=" + bashHome}, env...)...), socket: socket}
	t.Cleanup(s.kill)
	return s
}

// with returns a view of s that reports failures to t, for use inside subtests.
func (s *server) with(t *testing.T) *server {
	c := *s
	c.t = t
	return &c
}

// T runs a tmux command and returns its output without the trailing newline; failure is fatal.
func (s *server) T(args ...string) string {
	s.t.Helper()
	out, err := s.tmux.Run(args...)
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.TrimSuffix(out, "\n")
}

// Try runs a tmux command whose failure is part of the test (has-session, a not-yet-ready target).
func (s *server) Try(args ...string) (string, error) {
	out, err := s.tmux.Run(args...)
	return strings.TrimSuffix(out, "\n"), err
}

// socketPath asks the running server for its socket and fails unless it is this test's own socket.
func (s *server) socketPath() string {
	s.t.Helper()
	path := s.T("display", "-p", "#{socket_path}")
	if path == "" {
		s.t.Fatal("empty socket_path: a script would fall back to the default server")
	}
	want, err := filepath.EvalSymlinks(filepath.Dir(s.expectedSocketPath()))
	if err != nil {
		s.t.Fatal(err)
	}
	if path != filepath.Join(want, s.socket) {
		s.t.Fatalf("server socket %s is not this test's socket %s", path, filepath.Join(want, s.socket))
	}
	return path
}

func (s *server) expectedSocketPath() string {
	return filepath.Join(socketDir(), s.socket)
}

func socketDir() string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()))
}

// scriptEnv is the environment for a bash script under test: its plain `tmux` calls reach this server
// through TMUX, and nothing points at the user's server.
func (s *server) scriptEnv(extra ...string) []string {
	env := append(s.tmux.Env(), "TMUX="+s.socketPath()+","+s.T("display", "-p", "#{pid}")+",0")
	return append(env, extra...)
}

// kill stops the server and waits for it, its pane processes and their descendants, so none outlives
// the test and writes into its temp dirs (nvim saves shada on SIGHUP). Survivors get SIGKILL after 10s.
// The server can be started again afterwards.
func (s *server) kill() {
	roots, _ := s.Try("list-panes", "-a", "-F", "#{pane_pid}")
	if pid, err := s.Try("display", "-p", "#{pid}"); err == nil {
		roots += " " + pid
	}
	pids := s.descendants(strings.Fields(roots))
	if _, err := s.tmux.Run("kill-server"); err != nil && !tmux.IsNoServer(err) {
		s.t.Errorf("kill-server: %v", err)
	}
	for i := 0; slices.ContainsFunc(pids, alive); i++ {
		if i == 100 {
			survivors := slices.DeleteFunc(pids, func(p int) bool { return !alive(p) })
			s.t.Errorf("processes still running 10s after kill-server, sending SIGKILL: %v", survivors)
			for _, p := range survivors {
				syscall.Kill(p, syscall.SIGKILL)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := os.Remove(s.expectedSocketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.t.Errorf("remove socket: %v", err)
	}
}

// descendants returns roots and every process below them.
func (s *server) descendants(roots []string) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		s.t.Fatalf("ps: %v", err)
	}
	children := map[int][]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var pid, ppid int
		if _, err := fmt.Sscan(line, &pid, &ppid); err == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var pids []int
	for _, r := range roots {
		pid, err := strconv.Atoi(r)
		if err != nil {
			s.t.Fatalf("pane_pid %q: %v", r, err)
		}
		pids = append(pids, pid)
	}
	for i := 0; i < len(pids); i++ {
		pids = append(pids, children[pids[i]]...)
	}
	return pids
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// runScript runs a script with env and returns its combined output and error.
func runScript(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// sleeper builds a stand-in binary called name that sleeps: rules match the pane's process name, which
// macOS takes from the executable, so a script or symlink would show as sh or sleep.
func sleeper(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n")
	build := exec.Command("go", "build", "-o", name, "main.go")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s stub: %v\n%s", name, err, out)
	}
	return filepath.Join(dir, name)
}

// check reports a mismatch and lets the test go on, like the bash tests' check().
func check(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("❌ %s: expected [%s], got [%s]", name, want, got)
		return
	}
	t.Logf("✅ %s", name)
}

// waitFor polls cond every 500ms for up to 30s, like the bash tests' wait_for.
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	for range 60 {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("❌ timeout waiting for: %s", desc)
}

// tmuxHome is bashHome's ~/.tmux, which the bash scripts run from.
func tmuxHome() string {
	return filepath.Join(bashHome, ".tmux")
}

// requireFiles fails the test when a prerequisite (a tmux plugin) is missing.
func requireFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %v", err)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
