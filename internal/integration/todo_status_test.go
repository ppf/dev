//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev todo --json and --all --json give each item its status from the live sessions: a worker asking the user is
// blocked, as is one resting after a failed turn, a working one working, a linked one gone is gone, a name that is
// no session a person (waiting), an item on no one yours. --on the boss's own name is the user's own item.
func TestTodoStatus(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	agentBin := sleeper(t, "fakeclaude")
	dev := func(env []string, argv ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(argv, " "), err, stderr.String())
		}
		return string(out), stderr.String()
	}
	s.T("new-session", "-d", "-s", "boss-a", "sleep", "3600")
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	bossID := s.T("display", "-t", "=boss-a:", "-p", "#{session_id}")
	// worker starts a session boss-a started, its agent in state (waiting on a dialog when deciding), with note
	worker := func(name, state string, deciding bool, note string) {
		s.T("new-session", "-d", "-s", name, agentBin)
		s.T("set-option", "-t", "="+name+":", "@dev_started_by", bossID)
		pane := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		waitFor(t, name+" runs its agent", func() bool {
			return s.T("display", "-t", pane, "-p", "#{pane_current_command}") == "fakeclaude"
		})
		s.T("set-option", "-p", "-t", pane, "@agent_cmd", "fakeclaude")
		s.T("set-option", "-p", "-t", pane, "@agent_state", state)
		if deciding {
			s.T("set-option", "-p", "-t", pane, "@agent_waiting_for", "permission_prompt")
		}
		if note != "" {
			s.T("set-option", "-p", "-t", pane, "@dev_boss_note", note)
		}
	}
	worker("w-ask", "waiting", true, "")
	worker("w-busy", "working", false, "")
	worker("w-old", "idle", false, "")
	worker("w-failed", "idle", false, "failed: API error")

	env := []string{"TMUX_PANE=" + boss, "CLAUDECODE=1"}
	dev(env, "todo", "add", "mine")
	_, errOut := dev(env, "todo", "add", "--on", "boss-a", "the boss's own")
	if !strings.Contains(errOut, "this list's own boss, so the user's own") {
		t.Errorf("❌ --on boss-a stderr = %q", errOut)
	}
	dev(env, "todo", "add", "--on", "w-ask", "asks")
	dev(env, "todo", "add", "--on", "w-busy", "works")
	dev(env, "todo", "add", "--on", "w-failed", "failed")
	dev(env, "todo", "add", "--on", "w-old", "gone")
	dev(env, "todo", "add", "--on", "Ola", "a person's")
	s.T("kill-session", "-t", "=w-old")

	type item struct {
		Text     string `json:"text"`
		On       string `json:"on"`
		Status   string `json:"status"`
		StatusOn string `json:"status_on"`
	}
	want := []item{
		{"mine", "", "yours", ""},
		{"the boss's own", "", "yours", ""},
		{"asks", "w-ask", "blocked", "w-ask"},
		{"works", "w-busy", "working", "w-busy"},
		{"failed", "w-failed", "blocked", "w-failed"},
		{"gone", "w-old", "gone", "w-old"},
		{"a person's", "Ola", "waiting", "Ola"},
	}
	byText := func(items []item) map[string]item {
		m := map[string]item{}
		for _, it := range items {
			m[it.Text] = it
		}
		return m
	}
	out, _ := dev(env, "todo", "--json")
	var one struct{ Items []item }
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatalf("❌ --json: %v\n%s", err, out)
	}
	out, _ = dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "todo", "--all", "--json")
	var all []struct {
		Boss  string `json:"boss"`
		Items []item `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 1 || all[0].Boss != "boss-a" {
		t.Fatalf("❌ --all --json: %v\n%s", err, out)
	}
	for name, items := range map[string][]item{"--json": one.Items, "--all --json": all[0].Items} {
		got := byText(items)
		for _, w := range want {
			if got[w.Text] != w {
				t.Errorf("❌ %s %q = %+v, want %+v", name, w.Text, got[w.Text], w)
			}
		}
	}
}
