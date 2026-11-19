package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// Each desktop status reads as its rank's status, on an item linked to the worker by its app session id or one
// naming it.
func TestDesktopSession(t *testing.T) {
	for st, want := range map[string]string{
		desktop.StatusNeedsYou:    todo.StatusBlocked,
		desktop.StatusBlocked:     todo.StatusBlocked,
		desktop.StatusWorking:     todo.StatusWorking,
		desktop.StatusStuck:       todo.StatusWorking,
		desktop.StatusWaiting:     todo.StatusWaiting,
		desktop.StatusRateLimited: todo.StatusWaiting,
		desktop.StatusReviewReady: todo.StatusIdle,
		desktop.StatusIdle:        todo.StatusIdle,
		desktop.StatusStopped:     todo.StatusIdle,
	} {
		o := todo.Owner{Workers: []sidebar.Session{desktopSession(desktop.Worker{ID: "local_w", Name: "fix-login", Status: st})}}
		for _, it := range []todo.Shown{{On: "fix-login", Session: &todo.Session{ID: "local_w", Name: "fix-login"}}, {On: "Fix-Login"}} {
			if got, on := o.Status(it); got != want || on != "fix-login" {
				t.Errorf("%s, linked %v: status = %q on %q, want %q on fix-login", st, it.Session != nil, got, on, want)
			}
		}
	}
}

// A desktop boss's --json gives an item on someone its status from the boss's workers: linked or named, a person,
// one no longer listed.
func TestTodoDesktopStatus(t *testing.T) {
	todoEnv(t)
	home := os.Getenv("HOME")
	org := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	now := time.Now().UnixMilli()
	for name, data := range map[string]string{
		"local_busy.json":    `{"sessionId":"local_busy","createdAt":` + jsonInt(now) + `,"spawnedFrom":{"sessionId":"local_boss","name":"Busy"}}`,
		"local_blocked.json": `{"sessionId":"local_blocked","createdAt":` + jsonInt(now) + `,"spawnedFrom":{"sessionId":"local_boss","name":"Asks"},"postTurnSummary":{"status_category":"blocked"}}`,
	} {
		if err := os.WriteFile(filepath.Join(org, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	// This test's own process stands in for the busy worker's Claude
	if err := os.WriteFile(filepath.Join(sessions, "1.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"sessionId":"cli_busy","hostSessionId":"local_busy","status":"busy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(org, "local_broken.json")); err != nil {
		t.Fatal(err)
	}
	run(t, "", "todo", "add", "Fix W", "--on", "W")
	run(t, "", "todo", "add", "Run", "--on", "Busy")
	run(t, "", "todo", "add", "Pick", "--on", "Asks")
	run(t, "", "todo", "add", "Staging", "--on", "Marek")
	run(t, "", "todo", "add", "Answer Vince")
	if err := os.Remove(filepath.Join(org, "local_w.json")); err != nil {
		t.Fatal(err)
	}
	var l struct {
		Items []struct {
			Text     string `json:"text"`
			Status   string `json:"status"`
			StatusOn string `json:"status_on"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range l.Items {
		got[it.Text] = it.Status + " " + it.StatusOn
	}
	want := map[string]string{"Fix W": "gone W", "Run": "working Busy", "Pick": "blocked Asks", "Staging": "waiting Marek", "Answer Vince": "yours "}
	for text, w := range want {
		if got[text] != w {
			t.Errorf("%s = %q, want %q", text, got[text], w)
		}
	}
}
