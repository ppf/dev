//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A boss corrects an open ask with ask --replaces: same number on --all --json with the new context, options and link.
// The manager's pick made before the correction still answers it with an option kept; a board's answer naming an
// option the correction dropped is refused; an answered ask can't be replaced.
func TestTodoAskReplaces(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
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
	pane := map[string]string{}
	for _, name := range []string{"boss-a", "manager"} {
		s.T("new-session", "-d", "-s", name, "bash", script, "", filepath.Join(tmp, name+".log"))
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane[name] = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane[name]}, `{"hook_event_name":"SessionStart"}`, "state", "idle"); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, stderr)
		}
		waitFor(t, name+" box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane[name]), "Try") })
	}
	boss := []string{"TMUX_PANE=" + pane["boss-a"], "CLAUDECODE=1"}
	must := func(env []string, argv ...string) string {
		t.Helper()
		out, stderr, err := dev(env, "", argv...)
		if err != nil {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(argv, " "), err, stderr)
		}
		return out
	}

	must(boss, "todo", "ask", "Merge #148?", "--context", "wrong line", "--option", "Merge (Recommended)", "--option", "Wait", "--option", "Drop")
	time.Sleep(1100 * time.Millisecond)
	// The manager's dialog is up, then picked, before the boss corrects the ask
	pick := `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"boss-a #1: Merge #148?"}],"answers":{"boss-a #1: Merge #148?":"Merge"}}}`
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, pick, "state", "working"); err != nil {
		t.Fatalf("❌ manager pick: %v %s", err, stderr)
	}
	time.Sleep(1100 * time.Millisecond)
	check(t, "ask --replaces", must(boss, "todo", "ask", "--replaces", "1", "--context", "CI green", "--option", "Merge | now (Recommended)", "--option", "Wait | a week", "--link", "https://github.com/o/r/pull/148"), "replaced 1 · to do 1\n")

	var all []struct {
		Items []struct {
			ID      int      `json:"id"`
			Text    string   `json:"text"`
			Link    string   `json:"link"`
			Options []string `json:"options"`
			Context []string `json:"context"`
		} `json:"items"`
	}
	out := must([]string{"TMUX_PANE=", "CLAUDECODE="}, "todo", "--all", "--json")
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 1 || len(all[0].Items) != 1 {
		t.Fatalf("❌ --all --json = %s, %v", out, err)
	}
	if it := all[0].Items[0]; it.ID != 1 || it.Text != "Merge #148?" || it.Link != "https://github.com/o/r/pull/148" ||
		strings.Join(it.Options, "|") != "Merge | now (Recommended)|Wait | a week" || strings.Join(it.Context, "|") != "CI green" {
		t.Errorf("❌ replaced ask = %+v", it)
	}

	out, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"], "CLAUDECODE=1"}, "", "todo", "answer", "boss-a", "1", "Merge", "--via", "manager", "--question", "Merge #148?")
	if err != nil || out != "#1 answered, sent to boss-a\n" {
		t.Errorf("❌ the pick from before the correction: %q %v %s", out, err, stderr)
	}
	if _, stderr, err := dev(boss, "", "todo", "ask", "--replaces", "1", "--context", "late"); err == nil || !strings.Contains(stderr, "item 1 is answered or ticked") {
		t.Errorf("❌ replace an answered ask: %v %q, want refused", err, stderr)
	}

	must(boss, "todo", "ask", "Rebase #150?", "--option", "Rebase", "--option", "Leave it")
	must(boss, "todo", "ask", "--replaces", "2", "--option", "Rebase")
	if _, stderr, err := dev(boss, "", "todo", "answer", "boss-a", "2", "Leave it", "--from-board", "--question", "Rebase #150?"); err == nil || !strings.Contains(stderr, `option "Leave it" was changed or removed since`) {
		t.Errorf("❌ a dropped option: %v %q, want refused", err, stderr)
	}
	if _, stderr, err := dev(boss, "", "todo", "answer", "boss-a", "2", "Rebase", "--from-board", "--question", "Rebase #150?"); err != nil {
		t.Errorf("❌ a kept option: %v %s", err, stderr)
	}
}
