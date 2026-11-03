//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A claude the worker runs itself (`claude -p` from its Bash tool) fires the same hooks with the worker's
// TMUX_PANE: they leave the pane's state, conversation, dialog and activity alone and tell the boss nothing.
// A new claude in the pane after the registered one's /exit, or one beside it, still registers.
func TestNestedClaude(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	s.T("new-session", "-d", "-s", "boss", "bash", script, "", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	// The pane's process is the worker's claude; the sleep it starts is the claude it runs
	childFile := filepath.Join(tmp, "child")
	s.T("new-session", "-d", "-s", "w", "sh", "-c", "sleep 600 & echo $! > "+childFile+"; wait")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	agentPID := s.T("display", "-t", worker, "-p", "#{pane_pid}")
	var nestedPID string
	waitFor(t, "child pid written", func() bool {
		data, _ := os.ReadFile(childFile)
		nestedPID = strings.TrimSpace(string(data))
		return nestedPID != ""
	})
	run := func(pane, pid, argv, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, strings.Fields(argv)...)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDE_PID="+pid)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev %s %s: %v, output %q", argv, stdin, err, out)
		}
	}
	opts := func() string {
		var vals []string
		for _, o := range []string{"@agent_state", "@agent_cmd", "@agent_session", "@agent_question", "@agent_pid", "@agent_active", "@dev_boss_note"} {
			vals = append(vals, s.T("show-option", "-pqv", "-t", worker, o))
		}
		return strings.Join(vals, "|")
	}
	boxLog := func() string { data, _ := os.ReadFile(log); return string(data) }
	run(boss, "", "state idle", `{"hook_event_name":"SessionStart"}`)
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "Try") })

	run(worker, agentPID, "state idle", `{"hook_event_name":"SessionStart","session_id":"w1"}`)
	run(worker, agentPID, "state working", `{"hook_event_name":"UserPromptSubmit","session_id":"w1"}`)
	run(worker, agentPID, "question", `{"hook_event_name":"PermissionRequest","session_id":"w1","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	// Stamped by hand: a nested hook's stamp shows even within the same second
	s.T("set-option", "-p", "-t", worker, "@agent_active", "1")
	want := opts()
	check(t, "worker registered", want, "working|"+s.T("display", "-t", worker, "-p", "#{pane_current_command}")+`|w1|{"tool":"Bash","command":"ls"}|`+agentPID+"|1|")

	nested := func(argv, stdin string) { run(worker, nestedPID, argv, `{"session_id":"n1",`+stdin[1:]) }
	nested("state idle", `{"hook_event_name":"SessionStart"}`)
	nested("state working", `{"hook_event_name":"UserPromptSubmit"}`)
	nested("state working", `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create"}}`)
	nested("question", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
	nested("state waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission"}`)
	nested("state working", `{"hook_event_name":"PostToolUse","agent_id":"a1"}`)
	nested("state idle", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	nested("state idle", `{"hook_event_name":"StopFailure","error":"server_error","error_details":"ECONNREFUSED"}`)
	nested("state clear", `{"hook_event_name":"SessionEnd"}`)
	check(t, "nested claude leaves the pane alone", opts(), want)
	time.Sleep(time.Second)
	check(t, "nested claude tells the boss nothing", boxLog(), "")

	run(worker, agentPID, "state idle", `{"hook_event_name":"Stop","session_id":"w1","last_assistant_message":"Done."}`)
	waitFor(t, "boss gets the worker's own note", func() bool { return boxLog() == "[dev boss inbox] 1 worker update(s):\n- w finished: Done.\n" })

	run(worker, agentPID, "state idle", `{"hook_event_name":"SessionStart","session_id":"w2"}`)
	check(t, "/clear or /resume: same claude, new conversation", s.T("show-option", "-pqv", "-t", worker, "@agent_session"), "w2")

	// One that isn't below the registered claude: a claude started beside it
	sibling := exec.Command("sleep", "600")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	siblingPID := strconv.Itoa(sibling.Process.Pid)
	run(worker, siblingPID, "state idle", `{"hook_event_name":"SessionStart","session_id":"s1"}`)
	check(t, "a claude beside it registers", s.T("show-option", "-pqv", "-t", worker, "@agent_session")+"|"+s.T("show-option", "-pqv", "-t", worker, "@agent_pid"), "s1|"+siblingPID)

	run(worker, siblingPID, "state clear", `{"hook_event_name":"SessionEnd","session_id":"s1"}`)
	run(worker, nestedPID, "state idle", `{"hook_event_name":"SessionStart","session_id":"n2"}`)
	check(t, "after /exit any claude registers", s.T("show-option", "-pqv", "-t", worker, "@agent_session")+"|"+s.T("show-option", "-pqv", "-t", worker, "@agent_pid"), "n2|"+nestedPID)

	run(worker, "", "state working", `{"hook_event_name":"UserPromptSubmit","session_id":"x1"}`)
	check(t, "a hook without CLAUDE_PID records as before", s.T("show-option", "-pqv", "-t", worker, "@agent_session")+"|"+s.T("show-option", "-pqv", "-t", worker, "@agent_pid"), "x1|")
}
