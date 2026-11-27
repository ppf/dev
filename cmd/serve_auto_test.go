package cmd

import (
	"bytes"
	"cmp"
	"errors"
	"net"
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
	at       string // where it answers; "": 127.0.0.1:18417
	spawned  [][]string
	env      []string
	exit     bool   // the spawned serve exits at once
	logLine  string // what it writes to its log
	bindErr  error
	alive    bool             // a dev serve holds the alive lock
	taken    bool             // something listens on dev boss's address
	asked    []netip.AddrPort // the addresses taken was asked about
	envValue map[string]string
}

func (f *fakeStart) starter(t *testing.T) *starter {
	t.Helper()
	return &starter{
		getenv: func(k string) string { return f.envValue[k] },
		uid:    501, dir: filepath.Join(t.TempDir(), "serve"), home: t.TempDir(), exe: "/bin/dev", own: "(devel) abc",
		served: func(string, time.Duration) ([]serve.Running, error) {
			if f.runs != 0 {
				f.runs--
				return nil, serve.ErrNotRunning
			}
			return []serve.Running{{Addr: netip.MustParseAddrPort(cmp.Or(f.at, "127.0.0.1:18417")), Version: f.version}}, nil
		},
		alive: func(string) (bool, error) { return f.alive, nil },
		taken: func(ap netip.AddrPort) bool { f.asked = append(f.asked, ap); return f.taken },
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
	if got := out.String(); got != "dev serve started: http://127.0.0.1:18417/ (this Mac only, no pairing needed)\n" {
		t.Fatalf("got %q", got)
	}
}

func TestAutoServeDefaultAddr(t *testing.T) {
	f := &fakeStart{runs: 1, at: "127.0.0.1:8417", envValue: map[string]string{}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || !strings.HasPrefix(out.String(), "dev serve started: http://127.0.0.1:8417/") {
		t.Fatalf("err %v, out %q", err, out.String())
	}
	if want := [][]string{{"/bin/dev", "serve", "--awake=false", "--addr", "127.0.0.1:8417"}}; !slices.EqualFunc(f.spawned, want, slices.Equal) {
		t.Fatalf("spawned %q, want %q", f.spawned, want)
	}
}

func TestAutoServeRunning(t *testing.T) {
	for _, tt := range []struct{ version, want string }{
		{"(devel) abc", ""},
		{"v0.71.0", "⚠ dev serve v0.71.0 runs (this dev is (devel) abc): restart it to update, unless it reloads itself\n"},
	} {
		f := &fakeStart{version: tt.version, at: "127.0.0.1:8417", envValue: map[string]string{}}
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

func TestAutoServePrivateUnreadable(t *testing.T) {
	f := &fakeStart{envValue: map[string]string{"TMUX_TMPDIR": "/tmp/x"}}
	s := f.starter(t)
	s.served = func(string, time.Duration) ([]serve.Running, error) {
		return nil, errors.New("addr: permission denied")
	}
	if err := s.ensure(&bytes.Buffer{}); err == nil || err.Error() != "addr: permission denied" {
		t.Fatalf("got %v, want the read error", err)
	}
}

// DEV_SERVE_ADDR as an IPv4-mapped address: dev serve records it unmapped, and the start still names it
func TestAutoServeMapped(t *testing.T) {
	f := &fakeStart{runs: 1, at: "127.0.0.1:18417", envValue: map[string]string{"DEV_SERVE_ADDR": "[::ffff:127.0.0.1]:18417"}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || out.String() != "dev serve started: http://127.0.0.1:18417/ (this Mac only, no pairing needed)\n" {
		t.Fatalf("got %v %q", err, out.String())
	}
}

// #146: a hand-run dev serve beside dev boss's own: either answering is enough, and a start names its own
func TestAutoServeTwo(t *testing.T) {
	lan := serve.Running{Addr: netip.MustParseAddrPort("192.168.1.50:8417"), Version: "v0.71.0"}
	own := serve.Running{Addr: netip.MustParseAddrPort("127.0.0.1:18417"), Version: "v0.71.0"}
	f := &fakeStart{envValue: map[string]string{"DEV_SERVE_ADDR": "127.0.0.1:18417"}}
	s := f.starter(t)
	s.served = func(string, time.Duration) ([]serve.Running, error) { return []serve.Running{lan, own}, nil }
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil || len(f.spawned) > 0 || strings.Count(out.String(), "v0.71.0 runs") != 1 {
		t.Fatalf("two answering: err %v, out %q, spawned %q", err, out.String(), f.spawned)
	}

	calls := 0
	s.served = func(string, time.Duration) ([]serve.Running, error) {
		switch calls++; calls {
		case 1:
			return nil, serve.ErrNotRunning
		case 2: // a hand-run one starts meanwhile: not the one dev boss started
			return []serve.Running{lan}, nil
		}
		return []serve.Running{lan, own}, nil
	}
	out.Reset()
	if err := s.ensure(&out); err != nil || len(f.spawned) != 1 || calls < 3 ||
		out.String() != "dev serve started: http://127.0.0.1:18417/ (this Mac only, no pairing needed)\n" {
		t.Fatalf("start beside a hand-run one: err %v, out %q, calls %d", err, out.String(), calls)
	}
}

// Back to dev serve's own default (tailnet, else LAN) is autoServeDefault = "" and this test
func TestAutoServeAddr(t *testing.T) {
	if got := autoServeAddr(func(string) string { return "" }); got != "127.0.0.1:8417" {
		t.Errorf("default %q, want this Mac only", got)
	}
	if got := autoServeAddr(func(k string) string { return map[string]string{"DEV_SERVE_ADDR": "127.0.0.1:9"}[k] }); got != "127.0.0.1:9" {
		t.Errorf("override %q", got)
	}
}

func TestAutoServeLAN(t *testing.T) {
	f := &fakeStart{runs: 1, at: "192.168.1.89:8417", envValue: map[string]string{"DEV_SERVE_ADDR": "192.168.1.89:8417"}}
	s := f.starter(t)
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil {
		t.Fatal(err)
	}
	if want := "dev serve started: http://192.168.1.89:8417/ (pair a phone: dev serve pair)\n⚠ " + serve.LANNotice + "\n"; out.String() != want {
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
	for _, tt := range []struct {
		name, where string
		f           *fakeStart
	}{
		{"loopback, its port taken", "⚠ dev serve: something holds 127.0.0.1:18417 but doesn't answer as dev serve (a hung one? see ", &fakeStart{runs: -1, alive: true, taken: true, envValue: map[string]string{"DEV_SERVE_ADDR": "[::ffff:127.0.0.1]:18417"}}},
		{"tailnet", "⚠ dev serve runs but doesn't answer on its address (see ", &fakeStart{runs: -1, alive: true, envValue: map[string]string{"DEV_SERVE_ADDR": "100.96.196.51:8417"}}},
	} {
		name, f := tt.name, tt.f
		s := f.starter(t)
		var out bytes.Buffer
		if err := s.ensure(&out); err != nil || len(f.spawned) > 0 {
			t.Fatalf("%s: err %v, spawned %q", name, err, f.spawned)
		}
		if want := tt.where + filepath.Join(s.dir, "serve.log") + "): not starting another\n"; out.String() != want {
			t.Fatalf("%s: got %q, want %q", name, out.String(), want)
		}
		if f.taken && !slices.Equal(f.asked, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:18417")}) {
			t.Fatalf("%s: asked %v, want the unmapped address", name, f.asked)
		}
	}
}

// A taken loopback port with no dev serve alive isn't a stuck one: the start says what holds it
func TestAutoServeTakenNoneAlive(t *testing.T) {
	f := &fakeStart{runs: 1, at: "127.0.0.1:8417", taken: true, envValue: map[string]string{}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || len(f.spawned) != 1 || len(f.asked) > 0 {
		t.Fatalf("err %v, out %q, spawned %q, asked %v", err, out.String(), f.spawned, f.asked)
	}
}

// One starting or reloading on loopback holds the alive lock before it listens: dev boss waits for its port
func TestAutoServeSettles(t *testing.T) {
	f := &fakeStart{runs: -1, alive: true, envValue: map[string]string{}}
	s := f.starter(t)
	s.settle = time.Second
	s.taken = func(netip.AddrPort) bool { f.asked = append(f.asked, netip.AddrPort{}); return len(f.asked) >= 3 }
	var out bytes.Buffer
	if err := s.ensure(&out); err != nil || len(f.spawned) > 0 || len(f.asked) != 3 || !strings.HasPrefix(out.String(), "⚠ dev serve: something holds 127.0.0.1:8417") {
		t.Fatalf("err %v, out %q, spawned %q, asked %d", err, out.String(), f.spawned, len(f.asked))
	}
}

// Port 0 on loopback: any answering one will do, as before
func TestAutoServePortZeroRunning(t *testing.T) {
	f := &fakeStart{version: "(devel) abc", envValue: map[string]string{"DEV_SERVE_ADDR": "127.0.0.1:0"}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || out.Len() > 0 || len(f.spawned) > 0 {
		t.Fatalf("err %v, out %q, spawned %q", err, out.String(), f.spawned)
	}
}

// On a tailnet or LAN DEV_SERVE_ADDR any answering dev serve will do, as before
func TestAutoServeRunningElsewhere(t *testing.T) {
	f := &fakeStart{at: "127.0.0.1:8417", version: "(devel) abc", envValue: map[string]string{"DEV_SERVE_ADDR": "100.96.196.51:8417"}}
	var out bytes.Buffer
	if err := f.starter(t).ensure(&out); err != nil || out.Len() > 0 || len(f.spawned) > 0 {
		t.Fatalf("err %v, out %q, spawned %q", err, out.String(), f.spawned)
	}
}

// #314, #347: the launchd agent's tailnet dev serve answers, or runs with its address gone: dev boss's own starts on
// loopback beside it
func TestAutoServeBesideTailnet(t *testing.T) {
	tailnet := serve.Running{Addr: netip.MustParseAddrPort("100.96.196.51:8417"), Version: "(devel) abc"}
	own := serve.Running{Addr: netip.MustParseAddrPort("127.0.0.1:8417"), Version: "(devel) abc"}
	for name, first := range map[string][]serve.Running{"answering": {tailnet}, "address gone": nil} {
		f := &fakeStart{alive: true, envValue: map[string]string{}}
		s := f.starter(t)
		if _, err := serve.Install(s.home, serve.Agent{Bin: "/bin/dev", Addr: tailnet.Addr.String(), Log: filepath.Join(s.home, "serve.log")}); err != nil {
			t.Fatal(err)
		}
		calls := 0
		s.served = func(string, time.Duration) ([]serve.Running, error) {
			if calls++; calls == 1 {
				if first == nil {
					return nil, serve.ErrNotRunning
				}
				return first, nil
			}
			return append(slices.Clone(first), own), nil
		}
		var out bytes.Buffer
		if err := s.ensure(&out); err != nil || len(f.spawned) != 1 || out.String() != "dev serve started: http://127.0.0.1:8417/ (this Mac only, no pairing needed)\n" {
			t.Errorf("%s: err %v, out %q, spawned %q", name, err, out.String(), f.spawned)
		}
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
	for _, tt := range []struct{ name, env, agent string }{
		{"its address", "127.0.0.1:18417", "127.0.0.1:18417"},
		{"its address mapped", "127.0.0.1:18417", "[::ffff:127.0.0.1]:18417"},
		{"not loopback", "100.96.196.51:8417", "127.0.0.1:18417"},
		{"not loopback, no --addr", "100.96.196.51:8417", ""},
		{"an --addr it refuses", "127.0.0.1:18417", "0.0.0.0:18417"},
	} {
		f := &fakeStart{runs: -1, envValue: map[string]string{"DEV_SERVE_ADDR": tt.env}}
		s := f.starter(t)
		if _, err := serve.Install(s.home, serve.Agent{Bin: "/bin/dev", Addr: tt.agent, Log: filepath.Join(s.home, "serve.log")}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := s.ensure(&out); err != nil || len(f.spawned) > 0 {
			t.Fatalf("%s: err %v, spawned %q", tt.name, err, f.spawned)
		}
		if want := "⚠ dev serve: launchd agent installed but not answering: launchctl kickstart gui/501/dev.serve (not loaded: launchctl bootstrap gui/501 \"" + serve.PlistPath(s.home) + "\")\n"; out.String() != want {
			t.Fatalf("%s: got %q, want %q", tt.name, out.String(), want)
		}
	}
	// An agent on dev serve's own default (tailnet, else LAN) isn't on loopback: dev boss's starts
	f := &fakeStart{runs: 1, at: "127.0.0.1:8417", envValue: map[string]string{}}
	s := f.starter(t)
	if _, err := serve.Install(s.home, serve.Agent{Bin: "/bin/dev", Log: filepath.Join(s.home, "serve.log")}); err != nil {
		t.Fatal(err)
	}
	if err := s.ensure(&bytes.Buffer{}); err != nil || len(f.spawned) != 1 {
		t.Fatalf("no --addr: err %v, spawned %q", err, f.spawned)
	}
}

func TestAutoServeFails(t *testing.T) {
	t.Run("DEV_SERVE_ADDR refused", func(t *testing.T) {
		f := &fakeStart{runs: -1, envValue: map[string]string{"DEV_SERVE_ADDR": "0.0.0.0:1"},
			bindErr: errors.New("--addr 0.0.0.0:1 listens on every interface: refused")}
		if err := f.starter(t).ensure(&bytes.Buffer{}); err == nil || err.Error() != "DEV_SERVE_ADDR 0.0.0.0:1 listens on every interface: refused" || len(f.spawned) > 0 {
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
		f := &fakeStart{runs: 2, exit: true, at: "127.0.0.1:8417", envValue: map[string]string{}}
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
		s.served = func(string, time.Duration) ([]serve.Running, error) {
			return nil, errors.New("addr/127.0.0.1:8417.json: permission denied")
		}
		if err := s.ensure(&bytes.Buffer{}); err == nil || err.Error() != "addr/127.0.0.1:8417.json: permission denied" || len(f.spawned) > 0 {
			t.Fatalf("err %v, spawned %q", err, f.spawned)
		}
	})
}

func TestAgentAddr(t *testing.T) {
	want := netip.MustParseAddrPort("127.0.0.1:8417")
	plist := func(args ...string) string {
		var b strings.Builder
		for _, a := range args {
			b.WriteString("<string>" + a + "</string><!-- a comment -->\n")
		}
		return `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>dev.serve</string>
<key>EnvironmentVariables</key><dict><key>X</key><string>--addr=10.0.0.1:1</string></dict>
<key>ProgramArguments</key><array>` + b.String() + `</array></dict></plist>`
	}
	for name, tt := range map[string]struct {
		plist string
		ok    bool
	}{
		"written":       {string(serve.Plist(serve.Agent{Bin: "/bin/dev", Addr: "127.0.0.1:8417", Log: "/l"})), true},
		"--addr=":       {plist("/bin/dev", "serve", "--addr=[::ffff:127.0.0.1]:8417"), true},
		"spaces":        {plist("/bin/dev", "serve", " --addr ", " 127.0.0.1:8417 "), true},
		"the last":      {plist("/bin/dev", "serve", "--addr", "100.96.196.51:8417", "--addr", "127.0.0.1:8417"), true},
		"none":          {plist("/bin/dev", "serve"), false},
		"another":       {plist("/bin/dev", "serve", "--addr", "127.0.0.1:9"), false},
		"env, not args": {plist("/bin/dev", "serve"), false},
	} {
		if got, err := agentAddr([]byte(tt.plist)); err != nil || (got == want) != tt.ok {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	for name, p := range map[string]string{"refused": plist("serve", "--addr", "0.0.0.0:1"), "not XML": "bplist00\x01"} {
		if _, err := agentAddr([]byte(p)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestPortTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ap := ln.Addr().(*net.TCPAddr).AddrPort()
	if !portTaken(ap) {
		t.Error("listening: not taken")
	}
	_ = ln.Close()
	if portTaken(ap) {
		t.Error("closed: taken")
	}
}

// The log a running dev serve (the launchd agent's) writes to is kept when dev boss starts its own beside it
func TestAutoServeKeepsLiveLog(t *testing.T) {
	f := &fakeStart{runs: 1, at: "127.0.0.1:8417", alive: true, envValue: map[string]string{}}
	s := f.starter(t)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.dir, "serve.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ensure(&bytes.Buffer{}); err != nil || len(f.spawned) != 1 {
		t.Fatalf("err %v, spawned %q", err, f.spawned)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Size() <= 1<<20 {
		t.Fatalf("log truncated: %v %v", fi, err)
	}
}

// A plist it can't read or parse may be on dev boss's address: the agent's, as before
func TestAutoServeAgentUnreadable(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"not XML": 0o644, "unreadable": 0} {
		if mode == 0 && os.Getuid() == 0 {
			continue
		}
		f := &fakeStart{runs: -1, envValue: map[string]string{}}
		s := f.starter(t)
		path := serve.PlistPath(s.home)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("bplist00"), mode); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := s.ensure(&out); err != nil || len(f.spawned) > 0 || !strings.HasPrefix(out.String(), "⚠ dev serve: launchd agent installed but not answering") {
			t.Errorf("%s: err %v, out %q, spawned %q", name, err, out.String(), f.spawned)
		}
	}
}

func TestLastLineBesideAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.log")
	if err := os.WriteFile(path, []byte("❌ listen tcp 127.0.0.1:8417: bind: address already in use\n↻ dev serve: reloading into v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastLine(path, 0, "exited"); got != "listen tcp 127.0.0.1:8417: bind: address already in use" {
		t.Errorf("got %q: want the started one's error", got)
	}
}
