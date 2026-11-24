//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

// dev todo asking marks the ask whose question the manager has up: --all --json carries "asking" on it until the
// manager's pick; only the manager's coordinator pane runs it, only for an open ask still reading its text.
func TestTodoAsking(t *testing.T) {
	tmp := t.TempDir()
	state := filepath.Join(tmp, "state")
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+state)
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(env []string, stdin string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return string(out), stderr.String(), err
	}
	must := func(env []string, stdin string, argv ...string) string {
		t.Helper()
		out, stderr, err := dev(env, stdin, argv...)
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, stderr)
		}
		return out
	}
	s.T("new-session", "-d", "-s", "manager", "sleep 600")
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := s.T("display", "-t", "=manager:", "-p", "#{pane_id}")
	must([]string{"TMUX_PANE=" + manager}, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	s.T("new-session", "-d", "-s", "boss-a", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	s.T("set-option", "-t", "=boss-a:", "@dev_started_by", "manager")
	bossA := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	must([]string{"TMUX_PANE=" + bossA}, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	boss := []string{"TMUX_PANE=" + bossA, "CLAUDECODE=1"}
	must(boss, "", "todo", "ask", "Filter row?", "--option", "A: chips", "--option", "B: label (Recommended)", "--details", "why")
	must(boss, "", "todo", "add", "Book a room", "--details", "why")
	inManager := []string{"TMUX_PANE=" + manager, "CLAUDECODE=1"}
	asking := func() []int {
		t.Helper()
		var lists []struct {
			Boss  string `json:"boss"`
			Items []struct {
				ID     int  `json:"id"`
				Asking bool `json:"asking"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(must([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "todo", "--all", "--json")), &lists); err != nil {
			t.Fatal(err)
		}
		var ids []int
		for _, l := range lists {
			for _, it := range l.Items {
				if it.Asking {
					ids = append(ids, it.ID)
				}
			}
		}
		return ids
	}

	for _, tt := range []struct {
		env  []string
		argv []string
		want string
	}{
		{boss, []string{"boss-a", "1", "--question", "Filter row?"}, "runs only in the manager's coordinator pane"},
		{inManager, []string{"boss-a", "1", "--question", "Other?"}, `is "Filter row?" now, not "Other?"`},
		{inManager, []string{"boss-a", "2", "--question", "Book a room"}, "boss-a #2 is no ask"},
	} {
		if _, stderr, err := dev(tt.env, "", append([]string{"todo", "asking"}, tt.argv...)...); err == nil || !strings.Contains(stderr, tt.want) {
			t.Errorf("❌ asking %v: %v %q, want %q", tt.argv, err, stderr, tt.want)
		}
	}
	if got := asking(); len(got) != 0 {
		t.Fatalf("❌ asking before: %v", got)
	}
	check(t, "asking", must(inManager, "", "todo", "asking", "boss-a", "1", "--question", "Filter row?"), "boss-a #1: asked in manager now\n")
	if got := asking(); len(got) != 1 || got[0] != 1 {
		t.Errorf("❌ asking after the mark: %v, want [1]", got)
	}
	// The manager's pick: its dialog is answered
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []map[string]string{{"question": "a #1: Filter row?"}}, "answers": map[string]string{"a #1: Filter row?": "A: chips"}}})
	must([]string{"TMUX_PANE=" + manager}, string(in), "state", "working")
	if got := asking(); len(got) != 0 {
		t.Errorf("❌ asking after the pick: %v, want none", got)
	}

	// A resumed manager has no dialog up
	must(inManager, "", "todo", "asking", "boss-a", "1", "--question", "Filter row?")
	must([]string{"TMUX_PANE=" + manager}, `{"hook_event_name":"SessionStart","source":"resume"}`, "state", "idle")
	if got := asking(); len(got) != 0 {
		t.Errorf("❌ asking after a resume: %v, want none", got)
	}

	// With the manager's inbox off it still shows; answered on a board meanwhile, it goes, and isn't asked again
	s.T("set-option", "-t", "=manager:", inbox.OptOff, "off")
	must(inManager, "", "todo", "asking", "boss-a", "1", "--question", "Filter row?")
	if got := asking(); len(got) != 1 || got[0] != 1 {
		t.Errorf("❌ asking with the inbox off: %v, want [1]", got)
	}
	must([]string{"TMUX_PANE=", "CLAUDECODE=1"}, "", "todo", "answer", "--from-board", "--question", "Filter row?", "boss-a", "1", "A: chips")
	if got := asking(); len(got) != 0 {
		t.Errorf("❌ asking after a board's answer: %v, want none", got)
	}
	if _, stderr, err := dev(inManager, "", "todo", "asking", "boss-a", "1", "--question", "Filter row?"); err == nil || !strings.Contains(stderr, "boss-a #1 is closed already") {
		t.Errorf("❌ asking an answered item: %v %q", err, stderr)
	}
}
