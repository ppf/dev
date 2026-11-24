package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// A desktop app boss gets its full open list on the first workers' note, then only what changed under a count line;
// a compaction brings the full list back.
func TestStateSideSessionsTodoChanges(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	event := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"<side-sessions-event>W finished</side-sessions-event>"}`
	context := func() string {
		t.Helper()
		var out struct {
			Hook struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		got := run(t, event, "state", "working")
		if err := json.Unmarshal([]byte(got), &out); err != nil {
			t.Fatalf("hook output %q: %v", got, err)
		}
		return out.Hook.Context
	}
	run(t, "", "todo", "set", "Merge PR #9", "Ship v1")
	if got := context(); !strings.Contains(got, "1. Merge PR #9 (no details)\n2. Ship v1") {
		t.Errorf("first = %q, want the full list", got)
	}
	if got := context(); strings.Contains(got, "1. Merge PR #9") || !strings.Contains(got, "2 open, none new or changed since the last one") {
		t.Errorf("second = %q, want the count line, no items", got)
	}
	run(t, "", "todo", "add", "--no-details", "Tag v1")
	if got := context(); strings.Contains(got, "1. Merge PR #9") || !strings.Contains(got, "3 open, 1 new or changed since the last one; the others as before (dev todo lists them):\n3. Tag v1") {
		t.Errorf("after an add = %q, want only the new item", got)
	}
	// Another conversation (a restored boss): the full list
	other := event
	event = strings.Replace(event, `"s1"`, `"s2"`, 1)
	if got := context(); !strings.Contains(got, "1. Merge PR #9 (no details)\n2. Ship v1 (no details)\n3. Tag v1") {
		t.Errorf("another conversation = %q, want the full list", got)
	}
	event = other
	// A compaction's start hook gives the full list, the next reminder then the count
	if got := run(t, `{"hook_event_name":"SessionStart","session_id":"s1","source":"compact"}`, "state", "working"); !strings.Contains(got, `1. Merge PR #9 (no details)\n2. Ship v1 (no details)\n3. Tag v1`) {
		t.Errorf("compaction = %q, want the full list", got)
	}
	if got := context(); !strings.Contains(got, "3 open, none new or changed since the last one") {
		t.Errorf("after a compaction = %q, want the count line", got)
	}
}
