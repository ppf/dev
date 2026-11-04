package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// desktopHome is a home whose desktop app records hold boss local_boss, its worker local_w and a broken record
// naming the boss.
func desktopHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	org := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	if err := os.MkdirAll(org, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	files := map[string]string{
		"local_boss.json":   `{"sessionId":"local_boss","createdAt":` + jsonInt(now-60_000) + `}`,
		"local_w.json":      `{"sessionId":"local_w","title":"W","createdAt":` + jsonInt(now) + `,"spawnedFrom":{"sessionId":"local_boss"}}`,
		"local_broken.json": `{"spawnedFrom":{"sessionId":"local_boss"`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(org, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func jsonInt(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func TestDesktopBoardJSON(t *testing.T) {
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	stdout, stderr, err := execute(t, "", "desktop", "board", "--json")
	if err != nil {
		t.Fatal(err)
	}
	// The mod parses stdout whole: warnings must stay on stderr
	var workers []map[string]any
	if err := json.Unmarshal([]byte(stdout), &workers); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if len(workers) != 1 || workers[0]["id"] != "local_w" || workers[0]["state"] != "stopped" {
		t.Errorf("workers = %v, want local_w stopped", workers)
	}
	if !strings.Contains(stderr, "local_broken.json") {
		t.Errorf("stderr = %q, want local_broken.json reported", stderr)
	}
}

func TestDesktopBoardBossFlag(t *testing.T) {
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	stdout, _, err := execute(t, "", "desktop", "board", "--json", "--boss", "local_boss")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"local_w"`) {
		t.Errorf("stdout = %q, want --boss over the environment", stdout)
	}
}

func TestDesktopBoardNoBoss(t *testing.T) {
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "")
	if _, _, err := execute(t, "", "desktop", "board", "--json"); err == nil || !strings.Contains(err.Error(), "no boss session") {
		t.Errorf("err = %v, want no boss session", err)
	}
}

// A hook in a desktop app session writes the session's hook file instead of a pane's options; its end removes it.
func TestStateDesktop(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	path := filepath.Join(state, "dev", "desktop", "s1.json")
	if out, errOut, err := execute(t, `{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`, "state", "working"); out != "" || errOut != "" || err != nil {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), `"state":"working"`) {
		t.Errorf("hook file = %s, %v; want working", data, err)
	}
	if _, errOut, _ := execute(t, "{", "state", "working"); !strings.HasPrefix(errOut, "❌ dev state: ") {
		t.Errorf("stderr = %q, want malformed input reported", errOut)
	}
	if _, errOut, err := execute(t, `{"hook_event_name":"SessionEnd","session_id":"s1"}`, "state", "clear"); errOut != "" || err != nil {
		t.Fatalf("%q %v", errOut, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("hook file after SessionEnd: %v, want removed", err)
	}
}

// The hook file dev state writes is the one dev desktop board reads.
func TestDesktopBoardReadsHookFiles(t *testing.T) {
	desktopHome(t)
	home := os.Getenv("HOME")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	live := `{"pid":` + jsonInt(int64(os.Getpid())) + `,"sessionId":"cli-w","hostSessionId":"local_w","status":"idle"}`
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "sessions", "1.json"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := `{"hook_event_name":"StopFailure","session_id":"cli-w","error":"rate_limit","last_assistant_message":"You've hit your limit"}`
	if _, errOut, err := execute(t, failed, "state", "idle"); errOut != "" || err != nil {
		t.Fatalf("%q %v", errOut, err)
	}
	stdout, stderr, err := execute(t, "", "desktop", "board", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"kind":"rate-limited","text":"no reset time read"`) || strings.Contains(stderr, "no hook file") {
		t.Errorf("stdout = %s\nstderr = %s\nwant the rate-limited note from the hook file", stdout, stderr)
	}
}
