package cmd

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// /v1/board on invented lists, read by dev todo --all --json's code, is internal/serve/testdata/board.json: the
// shape the phone page reads. go test ./cmd -run TestBoardGolden -update rewrites it.
func TestBoardGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	transcript := filepath.Join(home, "a-fix.jsonl")
	call := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"description":"Test ` + home + `/src/login.go in ` + home + `"}}]}}`
	if err := os.WriteFile(transcript, []byte(call+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-b", "1") + tmux.FormatRow("boss-a", "1"), nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		case "list-panes":
			// boss-a live: every field 0 but its name and id; its worker a-fix working, its transcript under home
			rows := ""
			for _, set := range []map[string]string{{"#{session_name}": "boss-a", "#{session_id}": "$1"},
				{"#{session_name}": "a-fix", "#{session_id}": "$2", "#{pane_id}": "%2", "#{window_index}": "1", "#{@agent_state}": "working",
					"#{@dev_started_by}": "boss-a", "#{@agent_transcript}": transcript}} {
				var row []string
				for _, f := range strings.Split(args[len(args)-1], tmux.Sep) {
					row = append(row, cmp.Or(set[f], "0"))
				}
				rows += tmux.FormatRow(row[:len(row)-1]...)
			}
			return rows, nil
		}
		return "", nil
	})
	now := time.Unix(1700000000, 0)
	s, err := todo.ForPane(c, tmux.SessionTarget("boss-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []todo.Item{
		{Text: "Merge #148 now or after the release?", Options: []string{"Merge now | CI green (Recommended)", "After the release"}, Context: []string{"Release is Friday", "~/memo.md"}},
		{Text: "Read the plan", Link: "https://example.com/plan", Until: 1700086400},
		{Text: "Send the invoice", On: "alice"},
		{Text: "Fix the login", On: "a-fix"},
	} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	// A ticked item past the 5 s it stays in the list: finished
	if _, err := s.Add(todo.Item{Text: "Ship the notes"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{5}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	b, err := todo.ForPane(c, tmux.SessionTarget("boss-b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(todo.Item{Text: "unread"}, now); err != nil {
		t.Fatal(err)
	}
	bad, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*boss-b.json"))
	if err := os.WriteFile(bad[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := serve.Handler(serve.Config{
		Version:     "v0.0.0-golden",
		Now:         func() time.Time { return now },
		Lists:       boardLists(c, io.Discard, func() time.Time { return now }),
		ManagerRuns: func() bool { return running(c, sidebar.Manager) },
		Guard:       func(h http.Handler) http.Handler { return h },
		Pair:        http.NotFoundHandler(),
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8417/v1/board", nil))
	if w.Code != 200 {
		t.Fatalf("board %d: %s", w.Code, w.Body)
	}
	// The unreadable list's error names its file under ~, as a-fix's now does its file, not the home folder
	got := w.Body.String()
	golden := filepath.Join("..", "internal", "serve", "testdata", "board.json")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("board differs from %s (-update rewrites it):\n%s", golden, got)
	}
}

// dev serve pair is refused in Claude Code and outside a terminal, else prints the URL and a code; unpair with none paired.
func TestServePair(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	orig := hasTerminal
	t.Cleanup(func() { hasTerminal = orig })
	t.Setenv("CLAUDECODE", "1")
	hasTerminal = func() bool { return true }
	if _, _, err := execute(t, "", "serve", "pair"); err == nil || !strings.Contains(err.Error(), "Claude Code session") {
		t.Errorf("in Claude Code: %v, want refused", err)
	}
	t.Setenv("CLAUDECODE", "")
	hasTerminal = func() bool { return false }
	if _, _, err := execute(t, "", "serve", "pair"); err == nil || !strings.Contains(err.Error(), "outside a terminal") {
		t.Errorf("no terminal: %v, want refused", err)
	}
	hasTerminal = func() bool { return true }
	if _, _, err := execute(t, "", "serve", "pair"); err == nil || !strings.Contains(err.Error(), "dev serve is not running") {
		t.Errorf("no server: %v, want not running", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer srv.Close()
	ln := srv.Listener
	dir, err := authDir()
	if err != nil {
		t.Fatal(err)
	}
	forget, err := serve.RecordAddr(dir, ln.Addr().(*net.TCPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, "", "serve", "pair")
	loop := regexp.MustCompile(`^dev serve listens on 127\.0\.0\.1 only: a phone can't reach it\. .*\nOn this Mac: open http://127\.0\.0\.1:\d+/ and enter the code \d{4} \d{4} \(valid 5 minutes, once\)\n$`)
	if err != nil || !loop.MatchString(out) || strings.Contains(out, "on the phone") {
		t.Errorf("pair on loopback: %q %v", out, err)
	}
	forget()
	if _, _, err := execute(t, "", "serve", "pair"); err == nil || !strings.Contains(err.Error(), "dev serve is not running") {
		t.Errorf("forgotten: %v, want not running", err)
	}
	if out, _, err := execute(t, "", "serve", "unpair"); err != nil || out != "no paired devices\n" {
		t.Errorf("unpair: %q %v", out, err)
	}
	if _, _, err := execute(t, "", "serve", "unpair", "phone"); err == nil || !strings.Contains(err.Error(), `no paired device named "phone"`) {
		t.Errorf("unpair phone: %v", err)
	}
}

// pairMessage names a URL for the phone only when the bound address is not loopback.
func TestPairMessage(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"192.168.1.89:8417", "open http://192.168.1.89:8417/ on the phone and enter the code 1234 5678 (valid 5 minutes, once)\n"},
		{"100.101.102.103:8417", "open http://100.101.102.103:8417/ on the phone and enter the code 1234 5678 (valid 5 minutes, once)\n"},
		{"127.0.0.1:8417", "dev serve listens on 127.0.0.1 only: a phone can't reach it. HINT\nOn this Mac: open http://127.0.0.1:8417/ and enter the code 1234 5678 (valid 5 minutes, once)\n"},
		{"[::1]:8417", "dev serve listens on ::1 only: a phone can't reach it. HINT\nOn this Mac: open http://[::1]:8417/ and enter the code 1234 5678 (valid 5 minutes, once)\n"},
	} {
		if got := pairMessage(netip.MustParseAddrPort(tc.addr), "1234 5678", "HINT"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// Stopping dev serve with a page's event stream open ends the stream and returns at once, not after Shutdown's timeout.
func TestListenAndServeClosesStreams(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := serve.Config{Now: time.Now, Guard: func(h http.Handler) http.Handler { return h }, Pair: http.NotFoundHandler(),
		Lists: func() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	errc := make(chan error, 1)
	go func() { _, _, err := listenAndServe(ctx, func() {}, ln, cfg, new(atomic.Int64), nil, nil); errc <- err }()
	res, err := http.Get("http://" + ln.Addr().String() + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if line, err := bufio.NewReader(res.Body).ReadString('\n'); err != nil || line != "event: board\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	start := time.Now()
	stop()
	select {
	case err := <-errc:
		if err != nil || time.Since(start) > 2*time.Second {
			t.Errorf("listenAndServe: %v after %s, want nil at once", err, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listenAndServe still running")
	}
}

// reloadRun is serveReloading on a loopback port with its watch and exec stubbed: the first watch gives a Ready
// once fire is closed; exec answers with execErr after checking what it is handed.
type reloadRun struct {
	addr    string
	watches chan struct{} // one per watch started
	execs   chan []string // the args of each exec
	errc    chan error
	stderr  *syncBuffer
	stop    func()
}

func startReloadRun(t *testing.T, fire <-chan struct{}, execErr error) *reloadRun {
	t.Helper()
	return startReloadRunWith(t, fire, execErr, nil, nil)
}

func startReloadRunLists(t *testing.T, fire <-chan struct{}, execErr error, lists func() (json.RawMessage, error)) *reloadRun {
	t.Helper()
	return startReloadRunWith(t, fire, execErr, func(c *serve.Config) { c.Lists = lists }, nil)
}

// startReloadRunWith is startReloadRun with cfg changed by tweak and args (nil: dev serve).
func startReloadRunWith(t *testing.T, fire <-chan struct{}, execErr error, tweak func(*serve.Config), args func() ([]string, error)) *reloadRun {
	t.Helper()
	if args == nil {
		args = func() ([]string, error) { return []string{"dev", "serve"}, nil }
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := serve.Config{Version: "v1.0.0", Now: time.Now, Guard: func(h http.Handler) http.Handler { return h }, Pair: http.NotFoundHandler(),
		Lists: func() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }}
	if tweak != nil {
		tweak(&cfg)
	}
	r := &reloadRun{addr: ln.Addr().String(), watches: make(chan struct{}, 10), execs: make(chan []string, 10),
		errc: make(chan error, 1), stderr: &syncBuffer{}}
	alive, err := serve.Alive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	aliveFile = alive
	watch, exec := watchReload, execReload
	t.Cleanup(func() { watchReload, execReload, aliveFile = watch, exec, nil; _ = alive.Close() })
	watchReload = func(ctx context.Context, _ selfreload.Options) <-chan selfreload.Ready {
		r.watches <- struct{}{}
		ch := make(chan selfreload.Ready, 1)
		gives := fire
		fire = nil // only the first watch gives one
		go func() {
			defer close(ch)
			select {
			case <-gives:
				ch <- selfreload.Ready{Version: "v2.0.0"}
			case <-ctx.Done():
			}
		}()
		return ch
	}
	execReload = func(rd selfreload.Ready, args []string) error {
		if rd.Version != "v2.0.0" {
			t.Errorf("exec into %q", rd.Version)
		}
		if got := os.Getenv(envServeReexec); got != strconv.Itoa(os.Getpid())+" 0" {
			t.Errorf("%s=%q at exec, want this pid", envServeReexec, got)
		}
		// The socket stays bound, so a connection waits unanswered
		if _, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get("http://" + r.addr + "/v1/health"); err == nil {
			t.Error("still serving at exec")
		}
		if fl, err := unix.FcntlInt(alive.Fd(), unix.F_GETFD, 0); err != nil || fl&unix.FD_CLOEXEC != 0 {
			t.Errorf("alive lock not kept across the exec: flags %d, %v", fl, err)
		}
		r.execs <- args
		return execErr
	}
	ctx, stop := context.WithCancel(context.Background())
	r.stop = stop
	t.Cleanup(stop)
	o := selfreload.Options{Own: "v1.0.0"}
	go func() {
		r.errc <- serveReloading(ctx, ln, cfg, o, args, r.stderr, r.stderr)
	}()
	return r
}

// health is /v1/health's version, retried for a second while the listener comes back.
func (r *reloadRun) health(t *testing.T) string {
	t.Helper()
	var err error
	for range 20 {
		var res *http.Response
		if res, err = http.Get("http://" + r.addr + "/v1/health"); err == nil {
			var h struct{ Version string }
			derr := json.NewDecoder(res.Body).Decode(&h)
			_ = res.Body.Close()
			if derr != nil {
				t.Fatalf("health: %v", derr)
			}
			return h.Version
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("health: %v", err)
	return ""
}

func (r *reloadRun) end(t *testing.T) {
	t.Helper()
	r.stop()
	select {
	case err := <-r.errc:
		if err != nil {
			t.Errorf("serveReloading: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveReloading still running")
	}
	if v := os.Getenv(envServeReexec); v != "" {
		t.Errorf("%s=%q left set", envServeReexec, v)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A new release ends the page's event stream and shuts down before the exec; a failed exec serves again on the
// same address on the old version, says so and watches no more.
func TestServeReloadExecFails(t *testing.T) {
	fire := make(chan struct{})
	r := startReloadRun(t, fire, errors.New("exec format error"))
	<-r.watches
	res, err := http.Get("http://" + r.addr + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body := bufio.NewReader(res.Body)
	if line, err := body.ReadString('\n'); err != nil || line != "event: board\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	close(fire)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("stream: %v, want it ended", err)
	}
	if args := <-r.execs; !slices.Equal(args, []string{"dev", "serve"}) {
		t.Errorf("exec args %q", args)
	}
	if v := r.health(t); v != "v1.0.0" {
		t.Errorf("health %q after the failed exec, want v1.0.0", v)
	}
	if !strings.Contains(r.stderr.String(), "❌ reload: exec format error: dev serve stays on v1.0.0") {
		t.Errorf("stderr %q, want the ❌ line", r.stderr.String())
	}
	select {
	case <-r.watches:
		t.Error("watching again after a failed exec")
	case <-time.After(100 * time.Millisecond):
	}
	r.end(t)
}

// Exec refusing (kill switch on, file changed) serves again and watches again.
func TestServeReloadNotNow(t *testing.T) {
	fire := make(chan struct{})
	r := startReloadRun(t, fire, selfreload.ErrNotNow)
	<-r.watches
	close(fire)
	<-r.execs
	if v := r.health(t); v != "v1.0.0" {
		t.Errorf("health %q, want v1.0.0", v)
	}
	select {
	case <-r.watches:
	case <-time.After(5 * time.Second):
		t.Fatal("not watching again after ErrNotNow")
	}
	if strings.Contains(r.stderr.String(), "❌") {
		t.Errorf("stderr %q: ErrNotNow is no failure", r.stderr.String())
	}
	r.end(t)
}

// A stop during a reload's shutdown (a request holding it open) wins: no exec, serveReloading returns.
func TestServeReloadStopWins(t *testing.T) {
	fire, held, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := startReloadRunLists(t, fire, errors.New("exec called"), func() (json.RawMessage, error) {
		close(held)
		<-release
		return json.RawMessage(`[]`), nil
	})
	<-r.watches
	go func() {
		if res, err := http.Get("http://" + r.addr + "/v1/board"); err == nil {
			_ = res.Body.Close()
		}
	}()
	<-held
	close(fire)
	time.Sleep(100 * time.Millisecond) // the shutdown waits on the held request
	r.stop()
	close(release)
	select {
	case err := <-r.errc:
		if err != nil {
			t.Errorf("serveReloading: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveReloading still running")
	}
	select {
	case <-r.execs:
		t.Error("exec'd after a stop")
	default:
	}
}

func TestReexecArgs(t *testing.T) {
	base := []string{"dev", "serve"}
	bound := netip.MustParseAddrPort("100.1.2.3:8417")
	for _, tc := range []struct {
		args []string
		addr string
		own  bool
		want []string
		err  bool
	}{
		{base, "", true, []string{"dev", "serve", "--addr", "100.1.2.3:8417"}, false},
		{base, "100.1.2.3:0", true, []string{"dev", "serve", "--addr", "100.1.2.3:8417"}, false},
		{base, "100.1.2.3:8417", true, base, false},
		{base, "", false, base, false},
		{base, "100.1.2.3:8417", false, nil, true},
		{[]string{"dev", "serve", "--"}, "", true, []string{"dev", "serve", "--addr", "100.1.2.3:8417", "--"}, false},
	} {
		got, err := reexecArgs(tc.args, tc.addr, bound, tc.own)
		if !slices.Equal(got, tc.want) || (err != nil) != tc.err {
			t.Errorf("%q --addr %q own %v: %q %v, want %q, error %v", tc.args, tc.addr, tc.own, got, err, tc.want, tc.err)
		}
	}
	if !slices.Equal(base, []string{"dev", "serve"}) {
		t.Errorf("args changed: %q", base)
	}
	if !ownIP(netip.MustParseAddr("127.0.0.2")) || ownIP(netip.MustParseAddr("192.0.2.1")) {
		t.Error("ownIP: any loopback must be this Mac's, 192.0.2.1 (TEST-NET-1) not")
	}
}

// A request still open after the shutdown wait holds the reload: no exec, it serves again, the request finishes,
// and it watches again.
func TestServeReloadHeldByOpenRequest(t *testing.T) {
	wait := shutdownWait
	shutdownWait = 200 * time.Millisecond
	t.Cleanup(func() { shutdownWait = wait })
	fire, held, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := startReloadRunLists(t, fire, errors.New("exec called"), func() (json.RawMessage, error) {
		once.Do(func() { close(held); <-release })
		return json.RawMessage(`[]`), nil
	})
	<-r.watches
	done := make(chan int, 1)
	go func() {
		res, err := http.Get("http://" + r.addr + "/v1/board")
		if err != nil {
			done <- 0
			return
		}
		_ = res.Body.Close()
		done <- res.StatusCode
	}()
	<-held
	close(fire)
	select {
	case <-r.watches:
	case <-time.After(5 * time.Second):
		t.Fatal("not watching again after the held reload")
	}
	if v := r.health(t); v != "v1.0.0" {
		t.Errorf("health %q, want v1.0.0", v)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("held request: %d, want 200", code)
	}
	select {
	case <-r.execs:
		t.Error("exec'd with a request open")
	default:
	}
	if !strings.Contains(r.stderr.String(), "held: requests still open") {
		t.Errorf("stderr %q, want the held line", r.stderr.String())
	}
	r.end(t)
}

func TestReexecMarker(t *testing.T) {
	for _, tc := range []struct {
		v        string
		reloaded bool
		caff     int
	}{{"42 77", true, 77}, {"42 0", true, 0}, {"41 77", false, 0}, {"", false, 0}} {
		if r, c := reexecMarker(tc.v, 42); r != tc.reloaded || c != tc.caff {
			t.Errorf("%q: %v %d, want %v %d", tc.v, r, c, tc.reloaded, tc.caff)
		}
	}
}

// heldWithoutShutdown fires the Ready and checks it is held before any shutdown: the page's stream stays open, no
// exec, a held line naming why, and it watches again.
func heldWithoutShutdown(t *testing.T, r *reloadRun, fire chan struct{}, why string) {
	t.Helper()
	res, err := http.Get("http://" + r.addr + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body := bufio.NewReader(res.Body)
	if line, err := body.ReadString('\n'); err != nil || line != "event: board\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	close(fire)
	select {
	case <-r.watches:
	case <-time.After(5 * time.Second):
		t.Fatal("not watching again after the held reload")
	}
	if !strings.Contains(r.stderr.String(), "held: "+why) {
		t.Errorf("stderr %q, want held: %s", r.stderr.String(), why)
	}
	stream := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, body); stream <- err }()
	select {
	case err := <-stream:
		t.Errorf("stream ended (%v): a held reload must not shut down", err)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case <-r.execs:
		t.Error("exec'd while held")
	default:
	}
}

// A phone action running when the Ready comes holds the reload before any shutdown.
func TestServeReloadHeldByAction(t *testing.T) {
	fire, held, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := startReloadRunWith(t, fire, errors.New("exec called"), func(c *serve.Config) {
		c.Done = func(string, int, string) error { close(held); <-release; return nil }
	}, nil)
	<-r.watches
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+r.addr+"/v1/done", strings.NewReader(`{"boss":"b","id":1,"text":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dev-Page", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = res.Body.Close()
		done <- res.StatusCode
	}()
	<-held
	heldWithoutShutdown(t, r, fire, "a phone action is still running")
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("action: %d, want 200", code)
	}
	r.end(t)
}

// An --addr whose ip is gone holds the reload before any shutdown: the new process could not bind it.
func TestServeReloadHeldByGoneAddr(t *testing.T) {
	fire := make(chan struct{})
	r := startReloadRunWith(t, fire, errors.New("exec called"), nil, func() ([]string, error) {
		return nil, errors.New("100.1.2.3 is no longer this Mac's address")
	})
	<-r.watches
	heldWithoutShutdown(t, r, fire, "100.1.2.3 is no longer this Mac's address")
	r.end(t)
}

// awakeAlive: a running child is alive; one that exited is reaped and dead; no pid is dead.
func TestAwakeAlive(t *testing.T) {
	run := exec.Command("sleep", "5")
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()
	if !awakeAlive(run.Process.Pid) {
		t.Error("running child: not alive")
	}
	ended := exec.Command("true")
	if err := ended.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for awakeAlive(ended.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("exited child still alive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if awakeAlive(0) {
		t.Error("pid 0 alive")
	}
}

// A warning repeated within a minute is written once, again after it.
func TestUnrepeated(t *testing.T) {
	var out strings.Builder
	now := time.Unix(0, 0)
	u := &unrepeated{w: &out, now: func() time.Time { return now }}
	for _, step := range []struct {
		after time.Duration
		line  string
	}{{0, "a\n"}, {time.Second, "a\n"}, {time.Second, "b\n"}, {time.Minute, "a\n"}} {
		now = now.Add(step.after)
		if n, err := fmt.Fprint(u, step.line); err != nil || n != len(step.line) {
			t.Fatalf("write %q: %d %v", step.line, n, err)
		}
	}
	if out.String() != "a\nb\na\n" {
		t.Errorf("written %q, want a, b, then a again a minute on", out.String())
	}
}
