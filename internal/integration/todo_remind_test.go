//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A tmux boss's own Stop hook hands it worker notes with its open list: in full the first time in a conversation
// (its session_id), then only the count line while nothing changed, the full list again in another conversation.
func TestBossInboxListChanges(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	run := func(pane, stdin string, env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(append([]string{"TMUX_PANE=" + pane}, env...)...)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev %s: %v, stderr %q", strings.Join(argv, " "), err, stderr.String())
		}
		return string(out)
	}
	// A draft in its box: notes come through its own Stop hook, not pasted
	s.T("new-session", "-d", "-s", "boss", "bash", script, "half a thought", filepath.Join(tmp, "boss.log"))
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", s.T("display", "-t", "=boss:", "-p", "#{session_id}"))
	w := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run(boss, `{"hook_event_name":"SessionStart"}`, nil, "state", "idle")
	run(w, `{"hook_event_name":"SessionStart"}`, nil, "state", "idle")
	waitFor(t, "draft drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "half a thought") })
	run(boss, "", []string{"CLAUDECODE=1"}, "todo", "add", "Ship v1", "--details", "why")
	run(boss, "", []string{"CLAUDECODE=1"}, "todo", "add", "Tag v1", "--details", "why")

	reason := func(msg, session string) string {
		t.Helper()
		run(w, `{"hook_event_name":"Stop","last_assistant_message":"`+msg+`"}`, nil, "state", "idle")
		out := run(boss, `{"hook_event_name":"Stop","session_id":"`+session+`"}`, nil, "state", "idle")
		var v struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("❌ boss Stop output %q: %v", out, err)
		}
		return v.Reason
	}
	if got := reason("one", "s1"); !strings.Contains(got, "Your open to do list:\n1. Ship v1\n2. Tag v1") {
		t.Errorf("❌ first batch:\n%s\nwant the full list", got)
	}
	if got := reason("two", "s1"); !strings.Contains(got, "Your open to do list: 2 open, none new or changed since the last one") || strings.Contains(got, "1. Ship v1") {
		t.Errorf("❌ second batch:\n%s\nwant the count line alone", got)
	}
	if got := reason("three", "s2"); !strings.Contains(got, "Your open to do list:\n1. Ship v1\n2. Tag v1") {
		t.Errorf("❌ another conversation:\n%s\nwant the full list", got)
	}
}
