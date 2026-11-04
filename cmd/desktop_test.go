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
