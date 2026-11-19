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

// dev serve's /v1/board lists are what dev todo --all --json prints, on a loopback bind.
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
	resp, err := http.Get(url + "v1/board")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var board struct {
		Lists any `json:"lists"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &board) != nil {
		t.Fatalf("❌ /v1/board: %d %s", resp.StatusCode, body)
	}
	var want any
	if out := dev(user, "todo", "--all", "--json"); json.Unmarshal([]byte(out), &want) != nil {
		t.Fatalf("❌ --all --json: %s", out)
	}
	if !reflect.DeepEqual(board.Lists, want) {
		t.Errorf("❌ board lists differ from dev todo --all --json:\n%s", body)
	}
	if !strings.Contains(string(body), `"Merge #148?"`) || !strings.Contains(string(body), `"decide": 1`) {
		t.Errorf("❌ board lacks the ask:\n%s", body)
	}
}

// startServe runs dev serve on a loopback port of s, as the user, and is the URL it prints.
func startServe(t *testing.T, s *server, env ...string) string {
	t.Helper()
	serve := exec.Command(devBin, "serve", "--addr", "127.0.0.1:0")
	serve.Env = s.scriptEnv(env...)
	var stderr strings.Builder
	serve.Stderr = &stderr
	stdout, err := serve.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serve.Process.Kill(); _ = serve.Wait() })
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
