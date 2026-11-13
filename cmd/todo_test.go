package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// todoEnv runs dev todo as the desktop app session local_boss, whose worker is local_w (desktopHome).
func todoEnv(t *testing.T) {
	t.Helper()
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("TMUX_PANE", "")
}

func run(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, _, err := execute(t, stdin, args...)
	if err != nil {
		t.Fatalf("dev %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// set, add, done and clear say what's left; the list prints as rows and as the JSON the boards read.
func TestTodo(t *testing.T) {
	todoEnv(t)
	for _, tt := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"todo", "set", "Answer Vince", "Connect VPN"}, "to do 2\n"},
		{"", []string{"todo", "add", "Staging access", "--on", "Marek"}, "added 3 · to do 3, 1 on others\n"},
		{"", []string{"todo", "add", "Merge PR #87", "--link", "https://github.com/o/r/pull/87"}, "added 4 · to do 4, 1 on others\n"},
		{"", []string{"todo", "done", "2", "2"}, "done 2 2 · to do 3, 1 on others\n"},
		{"", []string{"todo"}, "1\t☐\tAnswer Vince\n2\t☑\tConnect VPN\n4\t☐\tMerge PR #87\thttps://github.com/o/r/pull/87\n3\t◌\tStaging access\ton Marek\n"},
	} {
		if got := run(t, tt.stdin, tt.args...); got != tt.want {
			t.Errorf("dev %s = %q, want %q", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	var l struct {
		Items []struct {
			ID   int    `json:"id"`
			Text string `json:"text"`
			On   string `json:"on"`
			Link string `json:"link"`
			Done bool   `json:"done"`
		} `json:"items"`
		Updated int64 `json:"updated"`
		Stale   bool  `json:"stale"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 4 || !l.Items[1].Done || l.Items[3].On != "Marek" || l.Items[2].Link == "" || l.Updated == 0 || l.Stale {
		t.Errorf("json = %+v", l)
	}
	if got := run(t, "", "todo", "clear"); got != "to do empty\n" {
		t.Errorf("clear = %q", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"items": []`) {
		t.Errorf("empty list's json = %q, want items []", got)
	}
}

// set without arguments reads stdin, one item per line, skipping blank lines.
func TestTodoSetStdin(t *testing.T) {
	todoEnv(t)
	run(t, "Answer Vince\n\n  Connect VPN  \n", "todo", "set")
	if got := run(t, "", "todo"); got != "1\t☐\tAnswer Vince\n2\t☐\tConnect VPN\n" {
		t.Errorf("list = %q", got)
	}
}

// A desktop worker's own session has no list, --boss local_… names its boss's; an unknown session fails.
func TestTodoBossFlag(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	if _, _, err := execute(t, "", "todo", "add", "x"); err == nil || !strings.Contains(err.Error(), "local_w is a worker") {
		t.Errorf("err = %v, want local_w refused as a worker", err)
	}
	run(t, "", "todo", "--boss", "local_boss", "set", "x")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	if got := run(t, "", "todo"); got != "1\t☐\tx\n" {
		t.Errorf("local_boss's list = %q", got)
	}
	if _, _, err := execute(t, "", "todo", "--boss", "local_typo"); err == nil || !strings.Contains(err.Error(), "no app session record for local_typo") {
		t.Errorf("err = %v, want no record", err)
	}
}

// Outside a boss session, without --boss, it fails rather than guess whose list it is.
func TestTodoNoBoss(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "")
	if _, _, err := execute(t, "", "todo", "add", "x"); err == nil || !strings.Contains(err.Error(), "no boss session") {
		t.Errorf("err = %v, want no boss session", err)
	}
}

// Bad numbers, empty items and an empty set are refused, leaving the list as it was.
func TestTodoErrors(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a")
	for _, args := range [][]string{{"todo", "done", "2"}, {"todo", "done", "x"}, {"todo", "add", " "}, {"todo", "set"}, {"todo", "set", "a", " "}, {"todo", "add", "\n\x1b"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n" {
		t.Errorf("list after refused calls = %q, want it unchanged", got)
	}
}

// add --until shows its time in the rows and the JSON; add --replaces takes an open item's number and place.
func TestTodoUntilReplaces(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a", "b")
	if got := run(t, "", "todo", "add", "Meeting", "--until", "2099-01-02 10:00"); got != "added 3 · to do 3\n" {
		t.Errorf("add --until = %q", got)
	}
	if got := run(t, "", "todo", "add", "b2", "--replaces", "2", "--on", "Marek"); got != "replaced 2 · to do 3, 1 on others\n" {
		t.Errorf("add --replaces = %q", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n3\t☐\tMeeting\tuntil 2099-01-02 10:00\n2\t◌\tb2\ton Marek\n" {
		t.Errorf("list = %q", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"until": `+jsonInt(time.Date(2099, 1, 2, 10, 0, 0, 0, time.Local).Unix())) {
		t.Errorf("json = %s, want until", got)
	}
	for _, args := range [][]string{{"todo", "add", "x", "--until", "yesterday"}, {"todo", "add", "x", "--until", "2000-01-01"}, {"todo", "add", "x", "--replaces", "9"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo", "add", "c"); got != "added 4 · to do 4, 1 on others\n" {
		t.Errorf("refused adds changed the list: %q", got)
	}
}

// In a desktop app boss, a prompt bringing its workers' notes gets its open list as context; a worker, another
// prompt or an empty list gets nothing.
func TestStateSideSessionsTodo(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	event := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"<system-reminder>…</system-reminder>\n<side-sessions-event>W finished</side-sessions-event>"}`
	if got := run(t, event, "state", "working"); got != "" {
		t.Errorf("empty list: %q, want nothing", got)
	}
	run(t, "", "todo", "set", "Merge PR #9")
	got := run(t, event, "state", "working")
	var out struct {
		Hook struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil || out.Hook.Event != "UserPromptSubmit" || !strings.Contains(out.Hook.Context, "1. Merge PR #9") {
		t.Errorf("event prompt: %q (%v), want the list as context", got, err)
	}
	if got := run(t, `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`, "state", "working"); got != "" {
		t.Errorf("plain prompt: %q, want nothing", got)
	}
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	if got, stderr, err := execute(t, event, "state", "working"); got != "" || stderr != "" || err != nil {
		t.Errorf("worker: %q, %q, %v; want nothing", got, stderr, err)
	}
	// A session without an app record reports on stderr; stdout, which Claude reads as JSON, stays empty
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_typo")
	if got, stderr, err := execute(t, event, "state", "working"); got != "" || !strings.HasPrefix(stderr, "❌ dev state: todo: no app session record") || err != nil {
		t.Errorf("no record: %q, %q, %v; want only a stderr report", got, stderr, err)
	}
}
