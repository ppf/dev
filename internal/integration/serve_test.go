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
)

// dev serve's /v1/board on loopback needs no pairing but refuses another site's request; its lists are what dev todo
// --all --json prints.
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
	boardReq := func(header ...string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, url+"v1/board", nil)
		for i := 0; i < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		return req
	}
	for _, h := range [][]string{{"Origin", "https://evil.example"}, {"Sec-Fetch-Site", "cross-site"}} {
		if resp, body := do(boardReq(h...)); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "another site's request") {
			t.Fatalf("❌ /v1/board with %v: %d %s, want 403", h, resp.StatusCode, body)
		}
	}
	resp, body := do(boardReq("Sec-Fetch-Site", "same-origin"))
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
