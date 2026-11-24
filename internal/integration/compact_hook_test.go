//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// After a compaction a coordinator's own hook (dev state on SessionStart "compact") gives it back its list and notes,
// the manager first what it owes (its pick not relayed), and leaves the pane's state as it was: a compaction comes
// mid-turn.
func TestCompactHook(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(argv, " "), err, stderr.String())
		}
		return string(out)
	}
	context := func(pane string) string {
		t.Helper()
		var got struct {
			Hook struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		out := dev(pane, `{"hook_event_name":"SessionStart","source":"compact","session_id":"c1"}`, "state", "idle")
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("❌ compact hook output %q: %v", out, err)
		}
		return got.Hook.Context
	}
	pane := map[string]string{}
	for _, name := range []string{"boss-a", "manager"} {
		s.T("new-session", "-d", "-s", name, "bash", script, "", filepath.Join(tmp, name+".log"))
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane[name] = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		s.T("set-option", "-p", "-t", pane[name], "@dev_boss_pane", "coordinator")
		dev(pane[name], `{"hook_event_name":"SessionStart","source":"startup","session_id":"c1"}`, "state", "idle")
		dev(pane[name], "", "todo", "add", "--no-details", "Ship "+name)
		dev(pane[name], "", "todo", "note", "decided", "CSV only for "+name)
	}
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
		"questions": []map[string]any{{"question": "a #2: Merge it?", "options": []map[string]string{{"label": "Merge"}, {"label": "Wait"}}}},
		"answers":   map[string]string{"a #2: Merge it?": "Merge"}}})
	dev(pane["manager"], string(in), "state", "working")
	since := s.T("display", "-p", "-t", pane["manager"], "#{@agent_state} #{@agent_since}")

	got := context(pane["manager"])
	owed := "[dev] Owed from before the compaction:\n" + `- The user picked "Merge" on "a #2: Merge it?", not relayed yet: relay it with dev todo answer --via manager.` + "\n"
	if !strings.HasPrefix(got, owed) || !strings.Contains(got, "1. Ship manager") || !strings.Contains(got, "CSV only for manager") {
		t.Errorf("❌ manager's compaction context = %q, want the owed line, then its list and notes", got)
	}
	check(t, "manager's state through the compaction", s.T("display", "-p", "-t", pane["manager"], "#{@agent_state} #{@agent_since}"), since)
	got = context(pane["boss-a"])
	if strings.Contains(got, "Owed") || !strings.Contains(got, "1. Ship boss-a") || !strings.Contains(got, "CSV only for boss-a") {
		t.Errorf("❌ boss's compaction context = %q, want its list and notes alone", got)
	}
}
