package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.json")
	t0 := time.Date(2026, 11, 4, 17, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	steps := []struct {
		name, state, input string
		now                time.Time
		want               Hook
	}{
		{"a prompt starts working", "working", `{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`, at(0),
			Hook{State: "working", Since: at(0).Unix(), Active: at(0).Unix()}},
		{"a tool call is progress, the stretch goes on", "working", `{"hook_event_name":"PostToolUse","session_id":"s1"}`, at(1),
			Hook{State: "working", Since: at(0).Unix(), Active: at(1).Unix()}},
		{"a subagent's tool call only moves Active", "working", `{"hook_event_name":"PostToolUse","session_id":"s1","agent_id":"a1"}`, at(2),
			Hook{State: "working", Since: at(0).Unix(), Active: at(2).Unix()}},
		{"a permission prompt waits", "waiting", `{"hook_event_name":"Notification","session_id":"s1","notification_type":"permission_prompt"}`, at(3),
			Hook{State: "waiting", Since: at(3).Unix(), Active: at(3).Unix(), WaitingFor: "permission_prompt"}},
		{"each decision starts its own stretch", "waiting", `{"hook_event_name":"Notification","session_id":"s1","notification_type":"elicitation_dialog"}`, at(4),
			Hook{State: "waiting", Since: at(4).Unix(), Active: at(4).Unix(), WaitingFor: "elicitation_dialog"}},
		{"a Stop waiting on background tasks keeps working", "idle",
			`{"hook_event_name":"Stop","session_id":"s1","background_tasks":[{"type":"subagent","status":"running"}]}`, at(5),
			Hook{State: "working", Since: at(5).Unix(), Active: at(5).Unix()}},
		{"a rate limit records its reset", "idle",
			`{"hook_event_name":"StopFailure","session_id":"s1","error":"rate_limit","last_assistant_message":"You've hit your limit · resets 7:10pm (UTC)"}`, at(6),
			Hook{State: "idle", Since: at(6).Unix(), Active: at(6).Unix(), RateLimited: true, ResetAt: time.Date(2026, 11, 4, 19, 10, 0, 0, time.UTC).Unix()}},
		{"another error is no rate limit", "idle", `{"hook_event_name":"StopFailure","session_id":"s1","error":"server_error"}`, at(7),
			Hook{State: "idle", Since: at(6).Unix(), Active: at(7).Unix()}},
		{"a rate limit without a reset time", "idle", `{"hook_event_name":"StopFailure","session_id":"s1","error":"rate_limit"}`, at(8),
			Hook{State: "idle", Since: at(6).Unix(), Active: at(8).Unix(), RateLimited: true}},
		{"a new prompt ends the rate limit", "working", `{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`, at(9),
			Hook{State: "working", Since: at(9).Unix(), Active: at(9).Unix()}},
	}
	for _, s := range steps {
		if err := RecordState(dir, s.state, []byte(s.input), s.now); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		got, err := ReadHook(path)
		if err != nil || got != s.want {
			t.Errorf("%s: hook = %+v, %v; want %+v", s.name, got, err, s.want)
		}
	}

	if err := RecordState(dir, "clear", []byte(`{"hook_event_name":"SessionEnd","session_id":"s1"}`), at(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after SessionEnd: %v, want the file removed", err)
	}
	if err := RecordState(dir, "clear", []byte(`{"hook_event_name":"SessionEnd","session_id":"s1"}`), at(10)); err != nil {
		t.Errorf("a second SessionEnd: %v, want none", err)
	}
}

func TestRecordStateSkips(t *testing.T) {
	dir := t.TempDir()
	for _, input := range []string{
		`{"hook_event_name":"PostToolUse"}`,
		// Never a path outside dir
		`{"hook_event_name":"PostToolUse","session_id":"../x"}`,
	} {
		if err := RecordState(dir, "working", []byte(input), time.Now()); err != nil {
			t.Errorf("%s: %v", input, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files written: %v", entries)
	}
	if err := RecordState(dir, "working", []byte("{"), time.Now()); err == nil {
		t.Error("malformed input: want an error")
	}
}

func TestRecordQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.json")
	t0 := time.Date(2026, 11, 4, 17, 0, 0, 0, time.UTC)
	if err := RecordState(dir, "working", []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`), t0); err != nil {
		t.Fatal(err)
	}
	ask := `{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"npm   test"}}`
	if err := RecordQuestion(dir, []byte(ask), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Auto mode may approve it: still working until the dialog's Notification
	want := Hook{State: "working", Since: t0.Unix(), Active: t0.Add(time.Minute).Unix(), Question: "allow Bash: npm test"}
	if got, err := ReadHook(path); err != nil || got != want {
		t.Errorf("hook = %+v, %v; want %+v", got, err, want)
	}
	shown := `{"hook_event_name":"Notification","session_id":"s1","notification_type":"permission_prompt"}`
	if err := RecordState(dir, "waiting", []byte(shown), t0.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	want = Hook{State: "waiting", Since: t0.Add(90 * time.Second).Unix(), Active: t0.Add(90 * time.Second).Unix(), WaitingFor: "permission_prompt",
		Question: "allow Bash: npm test"}
	if got, _ := ReadHook(path); got != want {
		t.Errorf("after the Notification: %+v, want %+v", got, want)
	}

	// A subagent's dialog isn't the session's
	sub := `{"hook_event_name":"PermissionRequest","session_id":"s1","agent_id":"a1","tool_name":"Read","tool_input":{"file_path":"/x"}}`
	if err := RecordQuestion(dir, []byte(sub), t0.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadHook(path); got.Question != "allow Bash: npm test" || got.Active != t0.Add(100*time.Second).Unix() {
		t.Errorf("after a subagent's dialog: %+v, want only Active moved", got)
	}

	question := `{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}]}}`
	if err := RecordQuestion(dir, []byte(question), t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadHook(path); got.State != "waiting" || got.WaitingFor != "AskUserQuestion" || got.Question != "Which? (A / B)" {
		t.Errorf("hook = %+v, want AskUserQuestion's question", got)
	}

	// The turn's end closes any dialog in it
	if err := RecordState(dir, "idle", []byte(`{"hook_event_name":"Stop","session_id":"s1"}`), t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadHook(path); got.Question != "" || got.WaitingFor != "" || got.State != "idle" {
		t.Errorf("after Stop: %+v, want idle with no question", got)
	}

	// Another tool's PreToolUse is no dialog
	if err := RecordQuestion(dir, []byte(`{"hook_event_name":"PreToolUse","session_id":"s2","tool_name":"Bash"}`), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s2.json")); !os.IsNotExist(err) {
		t.Errorf("s2: %v, want no file", err)
	}
}

// Hooks of one session run side by side: none loses another's update.
func TestRecordStateConcurrent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := RecordState(dir, "working", []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`), now); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	for i := range 20 {
		go func() {
			if i == 0 {
				done <- RecordQuestion(dir, []byte(`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`), now)
				return
			}
			done <- RecordState(dir, "working", []byte(`{"hook_event_name":"PostToolUse","session_id":"s1","agent_id":"a1"}`), now)
		}()
	}
	for range 20 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if h, err := ReadHook(filepath.Join(dir, "s1.json")); err != nil || h.Question != "allow Bash: ls" {
		t.Errorf("hook = %+v, %v; want the question kept", h, err)
	}
}

func TestRecordStateHeals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s1"}`)
	if err := RecordState(dir, "working", in, time.Now()); err == nil {
		t.Error("broken file: want it reported")
	}
	if err := RecordState(dir, "working", in, time.Now()); err != nil {
		t.Errorf("next hook: %v, want the file replaced", err)
	}
}

func TestRecordStatePrunes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for _, id := range []string{"old", "new"} {
		if err := RecordState(dir, "idle", []byte(`{"hook_event_name":"Stop","session_id":"`+id+`"}`), now); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-staleHooks - time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.json"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := RecordState(dir, "idle", []byte(`{"hook_event_name":"SessionStart","session_id":"s1"}`), now); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"old.json": false, "old.json.lock": false, "new.json": true, "s1.json": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: %v, want present=%v", name, err, want)
		}
	}
}
