//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A boss asks a decision on its list and the user answers it from a shell: the item ticks with the answer and the
// boss's inbox gets "you answered" as one pasted line. A Claude Code session can't answer, nor answer twice; --all
// lists every boss's list with its boss.
func TestTodoAskAnswer(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(env []string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), &exec.ExitError{Stderr: []byte(stderr.String())}
		}
		return string(out), nil
	}
	log := filepath.Join(tmp, "boss-a.log")
	s.T("new-session", "-d", "-s", "boss-a", "bash", script, "", log)
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	cmd := exec.Command(devBin, "state", "idle")
	cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("❌ register boss-a: %v %s", err, out)
	}
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane), "Try") })

	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	out, err := dev(boss, "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait")
	check(t, "ask", out, "asked 1 · to do 1\n")
	if err != nil {
		t.Fatal(err)
	}

	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	if _, err := dev(boss, "todo", "answer", "boss-a", "1", "Merge"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "refused in a Claude Code session") {
		t.Errorf("❌ answer from a Claude: %v, want refused", err)
	}
	out, err = dev(user, "todo", "--all", "--json")
	var all []struct {
		Boss  string `json:"boss"`
		Items []struct {
			ID      int      `json:"id"`
			Options []string `json:"options"`
		} `json:"items"`
	}
	if err != nil || json.Unmarshal([]byte(out), &all) != nil || len(all) != 1 || all[0].Boss != "boss-a" || len(all[0].Items) != 1 || len(all[0].Items[0].Options) != 2 {
		t.Errorf("❌ --all --json = %s, %v", out, err)
	}
	out, _ = dev(user, "todo", "--all")
	check(t, "--all", out, "boss-a\t1\t☐\tMerge #148?\toptions: Merge (Recommended) · Wait\n")

	// A board's button runs inside Claude Code: allowed with its question, refused when item 1 reads otherwise
	if _, err := dev(boss, "todo", "answer", "boss-a", "1", "1", "--from-board", "--question", "Another?"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), `is "Merge #148?" now, not "Another?"`) {
		t.Errorf("❌ board answer with a changed question: %v, want refused", err)
	}
	out, err = dev(boss, "todo", "answer", "boss-a", "1", "1", "--from-board", "--question", "Merge #148?")
	check(t, "answer", out, "#1 answered, sent to boss-a\n")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "boss-a gets the answer", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "- you answered #1 \"Merge #148?\": Merge\n")
	})
	out, _ = dev(boss, "todo")
	check(t, "ticked with its answer", out, "1\t☑\tMerge #148?\tanswered: Merge\n")
	if _, err := dev(user, "todo", "answer", "boss-a", "1", "Wait"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), `item 1 is answered already: the user answered "Merge" in the sidebar, a board or a shell, not "Wait"`) {
		t.Errorf("❌ second answer: %v, want answered already", err)
	}

	// A boss with its inbox off can't get an answer: the item stays open
	if _, err := dev(boss, "todo", "ask", "Release?", "--option", "Now"); err != nil {
		t.Fatal(err)
	}
	s.T("set-option", "-t", "=boss-a:", "@dev_boss_inbox", "off")
	if _, err := dev(user, "todo", "answer", "boss-a", "2", "1"); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), "has its inbox off") {
		t.Errorf("❌ answer with the inbox off: %v, want refused", err)
	}
	if out, _ = dev(boss, "todo"); !strings.Contains(out, "2\t☐\tRelease?\toptions: Now\n") {
		t.Errorf("❌ with the inbox off the ask stays open: %q", out)
	}

	// An ask with context lines and option details: --all prints them, a board's button sends the option as stored
	// and the answer is its label
	s.T("set-option", "-u", "-t", "=boss-a:", "@dev_boss_inbox")
	if _, err := dev(boss, "todo", "ask", "Rebase #150?", "--context", "main moved 3 commits", "--context", "~/memo.md", "--option", "Rebase | ~10 min (Recommended)", "--option", "Leave it"); err != nil {
		t.Fatal(err)
	}
	out, _ = dev(user, "todo", "--all")
	if want := "boss-a\t3\t☐\tRebase #150?\toptions: Rebase | ~10 min (Recommended) · Leave it\nboss-a\t    - main moved 3 commits\nboss-a\t    - ~/memo.md\n"; !strings.HasSuffix(out, want) {
		t.Errorf("❌ --all with context = %q, want it to end %q", out, want)
	}
	out, _ = dev(user, "todo", "--all", "--json")
	var withContext []struct {
		Items []struct {
			Context []string `json:"context"`
			Choices []struct {
				Label       string `json:"label"`
				Detail      string `json:"detail"`
				Recommended bool   `json:"recommended"`
			} `json:"choices"`
		} `json:"items"`
	}
	err = json.Unmarshal([]byte(out), &withContext)
	if err != nil || len(withContext) != 1 || len(withContext[0].Items) == 0 {
		t.Fatalf("❌ --all --json = %s, %v", out, err)
	}
	if last := withContext[0].Items[len(withContext[0].Items)-1]; strings.Join(last.Context, "|") != "main moved 3 commits|~/memo.md" ||
		len(last.Choices) != 2 || last.Choices[0].Detail != "~10 min" || !last.Choices[0].Recommended {
		t.Errorf("❌ --all --json = %s, want context and choices", out)
	}
	if _, err := dev(boss, "todo", "answer", "boss-a", "3", "Rebase | ~10 min (Recommended)", "--from-board", "--question", "Rebase #150?"); err != nil {
		t.Fatal(err)
	}
	// The stub draws no input box after its first paste, so the note waits in the inbox: its row shows the answer
	if out, _ = dev(boss, "todo"); !strings.Contains(out, "3\t☑\tRebase #150?\tanswered: Rebase\n") {
		t.Errorf("❌ answered with the stored option: %q, want its label as the answer", out)
	}
}
