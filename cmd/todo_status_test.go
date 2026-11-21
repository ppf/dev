package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
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

// An unlinked item naming two desktop workers is on neither: a person; a linked one goes by its id.
func TestDesktopSessionSameName(t *testing.T) {
	o := todo.Owner{Workers: []sidebar.Session{
		desktopSession(desktop.Worker{ID: "local_1", Name: "fix", Status: desktop.StatusWorking}),
		desktopSession(desktop.Worker{ID: "local_2", Name: "fix", Status: desktop.StatusNeedsYou}),
	}}
	if got, on := o.Status(todo.Shown{On: "fix"}); got != todo.StatusWaiting || on != "fix" {
		t.Errorf("unlinked = %q on %q, want waiting on fix", got, on)
	}
	if got, _ := o.Status(todo.Shown{On: "fix", Session: &todo.Session{ID: "local_2", Name: "fix"}}); got != todo.StatusBlocked {
		t.Errorf("linked local_2 = %q, want blocked", got)
	}
}

// A desktop boss's --json gives an item on someone its status from the boss's workers: linked or named, a person,
// one no longer listed; none on a worker whose record, or on any while a live file, can't be read.
func TestTodoDesktopStatus(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
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
	run(t, "", "todo", "add", "Fix W", "--on", "W")
	run(t, "", "todo", "add", "Run", "--on", "Busy")
	run(t, "", "todo", "add", "Pick", "--on", "Asks")
	run(t, "", "todo", "add", "Staging", "--on", "Marek")
	run(t, "", "todo", "add", "Answer Vince")
	run(t, "", "todo", "add", "Broken", "--on", "local_broken")
	run(t, "", "todo", "add", "Parked", "--on", todo.Boss)
	if err := os.Remove(filepath.Join(org, "local_w.json")); err != nil {
		t.Fatal(err)
	}
	statuses := func() map[string]string {
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
		return got
	}
	got := statuses()
	want := map[string]string{"Fix W": "gone W", "Run": "working Busy", "Pick": "blocked Asks", "Staging": "waiting Marek", "Answer Vince": "yours ", "Broken": " ", "Parked": "idle boss"}
	for text, w := range want {
		if got[text] != w {
			t.Errorf("%s = %q, want %q", text, got[text], w)
		}
	}
	// Placed as the board model's table: the desktop boss, which has no name, as boss; an unread record's worker in progress
	var l struct {
		Items []struct {
			Text       string         `json:"text"`
			Column     string         `json:"column"`
			Assignee   *todo.Assignee `json:"assignee"`
			WaitingFor string         `json:"waiting_for"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	placed := map[string]string{}
	for _, it := range l.Items {
		placed[it.Text] = fmt.Sprintf("%s %+v %s", it.Column, *it.Assignee, it.WaitingFor)
	}
	for text, w := range map[string]string{
		"Fix W":        "todo {Kind:boss Name:boss Running:true} ",
		"Run":          "progress {Kind:worker Name:Busy Running:true} ",
		"Pick":         "progress {Kind:worker Name:Asks Running:false} ",
		"Staging":      "waiting {Kind:boss Name:boss Running:true} Marek",
		"Answer Vince": "todo {Kind:you Name: Running:false} ",
		"Broken":       "progress {Kind:worker Name:local_broken Running:false} ",
		"Parked":       "todo {Kind:boss Name:boss Running:true} ",
	} {
		if placed[text] != w {
			t.Errorf("%s placed %q, want %q", text, placed[text], w)
		}
	}
	// An item from before linking, found by its worker's name: a --replaces keeping its --on links that worker
	list := filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "desktop-local_boss.json")
	data, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for _, it := range f["items"].([]any) {
		if it := it.(map[string]any); it["text"] == "Run" {
			delete(it, "session")
			it["added"] = 1
		}
	}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, data, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "", "todo", "add", "--replaces", "2", "--context", "rerun")
	if got := run(t, "", "todo", "--json"); !strings.Contains(strings.Join(strings.Fields(got), ""), `"text":"Run","on":"Busy","session":{"id":"local_busy","name":"Busy"}`) {
		t.Errorf("--replaces of an item found by name: %s, want it linked to local_busy", got)
	}
	// A live file it can't read: a running worker would read stopped
	if err := os.WriteFile(filepath.Join(sessions, "2.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := statuses(); got["Run"] != " " || got["Answer Vince"] != "yours " {
		t.Errorf("with a live file unread: %v, want Run without a status", got)
	}
}

// A tmux worker's doing follows herd's sections: its tool call working, its planned wait, its newest WAITING marker
// otherwise; nothing asking, resting without a marker or before its transcript is written. A desktop worker's is
// its board's status text.
func TestDoing(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, rows ...any) string {
		var b strings.Builder
		for _, r := range rows {
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(append(data, '\n'))
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	reply := func(blocks ...any) map[string]any {
		return map[string]any{"type": "assistant", "message": map[string]any{"model": "claude-opus-5-5", "content": blocks}}
	}
	tool := write("tool.jsonl", reply(map[string]any{"type": "tool_use", "id": "a", "name": "Bash", "input": map[string]any{"description": "Run  the\ntests"}}))
	waits := write("wait.jsonl", reply(map[string]any{"type": "text", "text": "Pushed.\nWAITING (CI on #12)"}))
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.Local)
	planned := wait.Wait{Until: now.Add(time.Hour), Reason: "Codex limit resets"}
	var stderr strings.Builder
	for _, tt := range []struct {
		name string
		s    sidebar.Session
		want string
	}{
		{"working", sidebar.Session{State: agent.Working, Transcript: tool}, "Run the tests"},
		{"rate-limited on a marker", sidebar.Session{Resume: true, Transcript: waits}, "CI on #12"},
		{"resting on a marker", sidebar.Session{State: agent.Idle, Transcript: waits}, "CI on #12"},
		{"planned wait", sidebar.Session{Wait: planned, Transcript: tool}, planned.Text(now)},
		{"resting", sidebar.Session{State: agent.Idle, Transcript: tool}, ""},
		{"asking", sidebar.Session{State: agent.Waiting, Deciding: true, Transcript: waits}, ""},
		{"turn ended on a question", sidebar.Session{State: agent.Idle, Note: "asks: which  one?", Transcript: waits}, "which one?"},
		{"no transcript yet", sidebar.Session{State: agent.Working, Transcript: filepath.Join(dir, "none.jsonl")}, ""},
		{"no transcript option", sidebar.Session{State: agent.Working}, ""},
	} {
		if got := doing(tt.s, now, &stderr); got != tt.want {
			t.Errorf("%s: doing = %q, want %q", tt.name, got, tt.want)
		}
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr = %q, want nothing for a transcript not written yet", stderr.String())
	}
	if got := desktopSession(desktop.Worker{ID: "local_w", Status: desktop.StatusWorking, StatusText: "Edit show.go"}).Doing; got != "Edit show.go" {
		t.Errorf("desktop Doing = %q, want its status text", got)
	}
}
