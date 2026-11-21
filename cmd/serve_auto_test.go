package cmd

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/serve"
)

type fakeStart struct {
	runs     int // served calls that fail before one answers; -1: never answers
	version  string
	spawned  [][]string
	env      []string
	exit     bool   // the spawned serve exits at once
	logLine  string // what it writes to its log
	bindErr  error
	alive    bool // a dev serve holds the alive lock
	envValue map[string]string
}

func (f *fakeStart) starter(t *testing.T) *starter {
	t.Helper()
	return &starter{
		getenv: func(k string) string { return f.envValue[k] },
		uid:    501, dir: filepath.Join(t.TempDir(), "serve"), home: t.TempDir(), exe: "/bin/dev", own: "(devel) abc",
		served: func(string, time.Duration) (netip.AddrPort, string, error) {
			if f.runs != 0 {
				f.runs--
				return netip.AddrPort{}, "", serve.ErrNotRunning
			}
			return netip.MustParseAddrPort("127.0.0.1:18417"), f.version, nil
		},
		alive: func(string) (bool, error) { return f.alive, nil },
		local: func(netip.Addr) bool { return true },
		bind:  func(string) error { return f.bindErr },
		spawn: func(exe string, args, env []string, log, _ *os.File) (<-chan struct{}, error) {
			f.spawned = append(f.spawned, append([]string{exe}, args...))
			f.env = env
			if f.logLine != "" {
				if _, err := log.WriteString(f.logLine + "\n"); err != nil {
					t.Fatal(err)
				}
			}
			exited := make(chan struct{})
			if f.exit {
				close(exited)
			}
			return exited, nil
		},
		wait: 300 * time.Millisecond,
	}
}

func TestAutoServeStarts(t *testing.T) {
	f := &fakeStart{runs: 2, envValue: map[string]string{"TMUX": "/private/tmp/tmux-501/default,1,0", "CLAUDECODE": "1",
		"TMUX_PANE": "%1", "PATH": "/bin", "DEV_SERVE_ADDR": "127.0.0.1:18417"}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"/bin/dev", "serve", "--awake=false", "--addr", "127.0.0.1:18417"}}; !slices.EqualFunc(f.spawned, want, slices.Equal) {
		t.Fatalf("spawned %q, want %q", f.spawned, want)
	}
	if !slices.Equal(f.env, []string{"PATH=/bin", "TMUX=/private/tmp/tmux-501/default,0,-1"}) {
		t.Fatalf("env %q: want PATH and the boss's socket only", f.env)
	}
	if got := out.String(); got != "dev serve started: http://127.0.0.1:18417/ (pair this browser: dev serve pair)\n" {
		t.Fatalf("got %q", got)
	}
}

func TestAutoServeDefaultAddr(t *testing.T) {
	f := &fakeStart{runs: 1, envValue: map[string]string{}}
	if err := f.starter(t).ensure(&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"/bin/dev", "serve", "--awake=false"}}; !slices.EqualFunc(f.spawned, want, slices.Equal) {
		t.Fatalf("spawned %q, want %q", f.spawned, want)
	}
}

func TestAutoServeRunning(t *testing.T) {
	for _, tt := range []struct{ version, want string }{
		{"(devel) abc", ""},
		{"v0.71.0", "⚠ dev serve v0.71.0 runs (this dev is (devel) abc): restart it to update, unless it reloads itself\n"},
		{"", ""}, // an older dev serve's health named none
	} {
		f := &fakeStart{version: tt.version, envValue: map[string]string{}}
		var out bytes.Buffer
		if err := f.starter(t).ensure(&out); err != nil || out.String() != tt.want || len(f.spawned) > 0 {
			t.Errorf("%s: err %v, out %q, spawned %q; want %q", tt.version, err, out.String(), f.spawned, tt.want)
		}
	}
}

func TestAutoServeSkips(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"TMUX_TMPDIR":    {"TMUX_TMPDIR": "/tmp/x"},
		"TMUX first":     {"TMUX": "/tmp/x/default,1,0", "TMUX_TMPDIR": "/tmp"},
		"private socket": {"TMUX": "/tmp/tmux-501/it,1,0"},
		"other uid":      {"TMUX": "/tmp/tmux-502/default,1,0"},
	} {
		f := &fakeStart{runs: -1, envValue: env}
		err := f.starter(t).ensure(&bytes.Buffer{})
		if err == nil || !strings.HasPrefix(err.Error(), "this boss's tmux server isn't the default one") || len(f.spawned) > 0 {
			t.Errorf("%s: err %v, spawned %q", name, err, f.spawned)
		}
	}
}

func TestAutoServePrivateRunning(t *testing.T) {
	f := &fakeStart{envValue: map[string]string{"TMUX_TMPDIR": "/tmp/x"}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || out.Len() > 0 || len(f.spawned) > 0 {
		t.Fatalf("err %v, out %q, spawned %q", err, out.String(), f.spawned)
	}
}

func TestAutoServeOtherMachine(t *testing.T) {
	f := &fakeStart{version: "(devel) abc", envValue: map[string]string{}}
	s := f.starter(t)
	answered := false
	s.served = func(string, time.Duration) (netip.AddrPort, string, error) {
		if !answered { // the stale record's address answers from elsewhere, then the new one's own
			answered = true
			return netip.MustParseAddrPort("192.168.1.50:8417"), "(devel) abc", nil
		}
		return netip.MustParseAddrPort("127.0.0.1:18417"), "(devel) abc", nil
	}
	s.local = func(ip netip.Addr) bool { return ip.IsLoopback() }
	if err := s.ensure(&bytes.Buffer{}); err != nil || len(f.spawned) != 1 {
		t.Fatalf("err %v, spawned %q", err, f.spawned)
	}
}

// Switching autostart to loopback is autoServeDefault = "127.0.0.1:8417" and this test
func TestAutoServeAddr(t *testing.T) {
	if got := autoServeAddr(func(string) string { return "" }); got != "" {
		t.Errorf("default %q, want dev serve's own default", got)
	}
	if got := autoServeAddr(func(k string) string { return map[string]string{"DEV_SERVE_ADDR": "127.0.0.1:9"}[k] }); got != "127.0.0.1:9" {
		t.Errorf("override %q", got)
	}
}

func TestAutoServeLAN(t *testing.T) {
	f := &fakeStart{runs: 1, envValue: map[string]string{}}
	s := f.starter(t)
	s.served = func(string, time.Duration) (netip.AddrPort, string, error) {
		if f.runs > 0 {
			f.runs--
			return netip.AddrPort{}, "", serve.ErrNotRunning
		}
		return netip.MustParseAddrPort("192.168.1.89:8417"), "", nil
	}
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil {
		t.Fatal(err)
	}
	if want := "dev serve started: http://192.168.1.89:8417/ (pair this browser: dev serve pair)\n⚠ " + serve.LANNotice + "\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAutoServePortZero(t *testing.T) {
	f := &fakeStart{runs: -1, envValue: map[string]string{"DEV_SERVE_ADDR": "127.0.0.1:0"}}
	if err := f.starter(t).ensure(&bytes.Buffer{}); err == nil || !strings.HasPrefix(err.Error(), "DEV_SERVE_ADDR: port 0") || len(f.spawned) > 0 {
		t.Fatalf("err %v, spawned %q", err, f.spawned)
	}
}

func TestAutoServeAliveNotAnswering(t *testing.T) {
	f := &fakeStart{runs: -1, alive: true, envValue: map[string]string{}}
	s := f.starter(t)
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil || len(f.spawned) > 0 {
		t.Fatalf("err %v, spawned %q", err, f.spawned)
	}
	if want := "⚠ dev serve runs but doesn't answer on its address (see " + filepath.Join(s.dir, "serve.log") + "): not starting another\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestStartLock(t *testing.T) {
	dir := t.TempDir()
	first, err := startLock(dir, time.Now().Add(time.Second))
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	if second, err := startLock(dir, time.Now().Add(100*time.Millisecond)); err != nil || second != nil {
		t.Fatalf("taken twice: %v %v", second, err)
	}
	_ = first.Close()
	again, err := startLock(dir, time.Now())
	if err != nil || again == nil {
		t.Fatalf("after close: %v %v", again, err)
	}
	_ = again.Close()
}

func TestAutoServePrivateSocket(t *testing.T) {
	f := &fakeStart{runs: 1, envValue: map[string]string{"TMUX": "/tmp/x/it,9,3", "DEV_SERVE_ADDR": "127.0.0.1:18417"}}
	if err := f.starter(t).ensure(&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.env, []string{"TMUX=/tmp/x/it,0,-1"}) {
		t.Fatalf("env %q: want the boss's socket, no session", f.env)
	}
}

func TestAutoServeDefaultSocket(t *testing.T) {
	for _, socket := range []string{"/tmp/tmux-501/default", "/private/tmp/tmux-501/default"} {
		f := &fakeStart{runs: 1, envValue: map[string]string{"TMUX": socket + ",1,0", "TMUX_TMPDIR": "/tmp/custom", "XDG_STATE_HOME": "rel"}}
		if err := f.starter(t).ensure(&bytes.Buffer{}); err != nil || len(f.spawned) != 1 {
			t.Errorf("%s: err %v, spawned %q", socket, err, f.spawned)
		}
		if abs, _ := filepath.Abs("rel"); !slices.Contains(f.env, "XDG_STATE_HOME="+abs) || !slices.Contains(f.env, "TMUX_TMPDIR=/tmp/custom") {
			t.Errorf("%s: env %q", socket, f.env)
		}
	}
}

func TestLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.log")
	if err := os.WriteFile(path, []byte("old line\n❌ listen: in use\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		from int64
		want string
	}{{0, "listen: in use"}, {9, "listen: in use"}, {100, "exited"}} {
		if got := lastLine(path, tt.from, "exited"); got != tt.want {
			t.Errorf("from %d: %q, want %q", tt.from, got, tt.want)
		}
	}
}

func TestAutoServeLaunchAgent(t *testing.T) {
	f := &fakeStart{runs: -1, envValue: map[string]string{}}
	s := f.starter(t)
	if _, err := serve.Install(s.home, serve.Agent{Bin: "/bin/dev", Log: filepath.Join(s.home, "serve.log")}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil || len(f.spawned) > 0 {
		t.Fatalf("err %v, spawned %q", err, f.spawned)
	}
	if want := "⚠ dev serve: launchd agent installed but not answering: launchctl kickstart gui/501/dev.serve (not loaded: launchctl bootstrap gui/501 \"" + serve.PlistPath(s.home) + "\")\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAutoServeFails(t *testing.T) {
	t.Run("no address", func(t *testing.T) {
		f := &fakeStart{runs: -1, envValue: map[string]string{}, bindErr: errors.New("no tailnet or private LAN address")}
		if err := f.starter(t).ensure(&bytes.Buffer{}); err == nil || err.Error() != "no tailnet or private LAN address (dev boss: set DEV_SERVE_ADDR=ip:port, or DEV_NO_SERVE=1)" || len(f.spawned) > 0 {
			t.Fatalf("err %v, spawned %q", err, f.spawned)
		}
	})
	t.Run("exits", func(t *testing.T) {
		f := &fakeStart{runs: -1, exit: true, logLine: "Error: listen tcp 127.0.0.1:18417: bind: address already in use", envValue: map[string]string{}}
		s := f.starter(t)
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dir, "serve.log"), []byte("an earlier run's line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := s.ensure(&bytes.Buffer{})
		want := "Error: listen tcp 127.0.0.1:18417: bind: address already in use (log: " + filepath.Join(s.dir, "serve.log") + ")"
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	})
	t.Run("exits, another took the port", func(t *testing.T) {
		f := &fakeStart{runs: 2, exit: true, envValue: map[string]string{}}
		var out bytes.Buffer
		if err := f.starter(t).ensure(&out); err != nil || out.Len() > 0 {
			t.Fatalf("err %v, out %q", err, out.String())
		}
	})
	t.Run("never answers", func(t *testing.T) {
		f := &fakeStart{runs: -1, envValue: map[string]string{}}
		var out bytes.Buffer
		began := time.Now()
		if err := f.starter(t).ensure(&out); err != nil || !strings.HasPrefix(out.String(), "⚠ dev serve: started, not answering after 300ms: no output yet (log: ") {
			t.Fatalf("got %v %q", err, out.String())
		}
		if took := time.Since(began); took > 2*time.Second {
			t.Fatalf("took %s", took)
		}
	})
	t.Run("record unreadable", func(t *testing.T) {
		f := &fakeStart{envValue: map[string]string{}}
		s := f.starter(t)
		s.served = func(string, time.Duration) (netip.AddrPort, string, error) {
			return netip.AddrPort{}, "", errors.New("addr.json: permission denied")
		}
		if err := s.ensure(&bytes.Buffer{}); err == nil || err.Error() != "addr.json: permission denied" || len(f.spawned) > 0 {
			t.Fatalf("err %v, spawned %q", err, f.spawned)
		}
	})
}
