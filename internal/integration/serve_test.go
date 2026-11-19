//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/serve"
)

// dev serve's /v1/board needs a paired session, also on loopback; after pairing with a code from serve.NewCode (what
// dev serve pair writes), its lists are what dev todo --all --json prints.
func TestServeBoard(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "boss-a", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %v: %v", argv, err)
		}
		return string(out)
	}
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	dev(boss, "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait")
	dev(boss, "todo", "add", "Read the plan")
	user := []string{"TMUX_PANE=", "CLAUDECODE="}

	url := startServe(t, s, user...)
	do := func(req *http.Request) (*http.Response, string) {
		t.Helper()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(body)
	}
	boardReq := func(cookies ...*http.Cookie) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, url+"v1/board", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		return req
	}
	if resp, body := do(boardReq()); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "isn't paired") {
		t.Fatalf("❌ /v1/board unpaired: %d %s", resp.StatusCode, body)
	}
	resp, body := do(boardReq(pairServe(t, url, filepath.Join(tmp, "state"))))
	var board struct {
		Lists any `json:"lists"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &board) != nil {
		t.Fatalf("❌ /v1/board: %d %s", resp.StatusCode, body)
	}
	var want any
	if out := dev(user, "todo", "--all", "--json"); json.Unmarshal([]byte(out), &want) != nil {
		t.Fatalf("❌ --all --json: %s", out)
	}
	if !reflect.DeepEqual(board.Lists, want) {
		t.Errorf("❌ board lists differ from dev todo --all --json:\n%s", body)
	}
	if !strings.Contains(body, `"Merge #148?"`) || !strings.Contains(body, `"decide": 1`) {
		t.Errorf("❌ board lacks the ask:\n%s", body)
	}
}

// startServe runs dev serve on a loopback port of s, as the user, and is the URL it prints.
func startServe(t *testing.T, s *server, env ...string) string {
	t.Helper()
	srv := exec.Command(devBin, "serve", "--addr", "127.0.0.1:0", "--awake=false")
	srv.Env = s.scriptEnv(env...)
	var stderr strings.Builder
	srv.Stderr = &stderr
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("❌ dev serve's first line: %q, %v; stderr: %s", line, err, stderr.String())
	}
	url := strings.Fields(line)[3]
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("❌ dev serve printed %q", line)
	}
	return url
}

// pairServe pairs a phone with the dev serve at url through a code as dev serve pair writes it under stateHome, and
// is the session cookie.
func pairServe(t *testing.T, url, stateHome string) *http.Cookie {
	t.Helper()
	code, err := serve.NewCode(filepath.Join(stateHome, "dev", "serve"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"v1/pair", "application/json", strings.NewReader(`{"code":"`+code+`","device":"phone"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 {
		t.Fatalf("❌ /v1/pair: %d", resp.StatusCode)
	}
	return resp.Cookies()[0]
}
