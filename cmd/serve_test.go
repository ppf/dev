package cmd

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
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
	go func() { errc <- listenAndServe(ctx, ln, cfg) }()
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
