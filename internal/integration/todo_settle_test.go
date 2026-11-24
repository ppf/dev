//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The manager settles an ask its boss marked obvious with dev todo answer --recommended --via manager: only from its
// coordinator pane, never an unmarked ask, never a permission prompt; the item shows who decided and the boss gets
// the note.
func TestTodoAnswerRecommended(t *testing.T) {
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
	for _, name := range []string{"boss-a", "manager", "w"} {
		s.T("new-session", "-d", "-s", name, "bash", script, "", filepath.Join(tmp, name+".log"))
		pane[name] = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		if name == "w" {
			continue
		}
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane[name]}, `{"hook_event_name":"SessionStart"}`, "state", "idle"); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, stderr)
		}
		waitFor(t, name+" box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane[name]), "Try") })
	}
	claude := func(p string) []string { return []string{"TMUX_PANE=" + p, "CLAUDECODE=1"} }
	settle := func(from, boss, n, pick, question string) (string, string, error) {
		return dev(claude(from), "", "todo", "answer", boss, n, pick, "--recommended", "--question", question, "--via", "manager")
	}
	for _, ask := range [][]string{
		{"todo", "ask", "Re-run CI on #301?", "--option", "Re-run | known flake (Recommended)", "--option", "Look first | read the log", "--obvious", "known flake, 2 of 10 runs", "--details", "x"},
		{"todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait", "--details", "x"},
	} {
		if _, stderr, err := dev(claude(pane["boss-a"]), "", ask...); err != nil {
			t.Fatal(err, stderr)
		}
	}

	// Refusals: outside the manager's pane, an unmarked ask, a worker's permission prompt
	if _, stderr, err := settle(pane["boss-a"], "boss-a", "1", "Re-run", "Re-run CI on #301?"); err == nil || !strings.Contains(stderr, "only in the manager's coordinator pane") {
		t.Errorf("❌ from boss-a's pane: %v %q", err, stderr)
	}
	if _, stderr, err := settle(pane["manager"], "boss-a", "2", "Merge", "Merge #148?"); err == nil || !strings.Contains(stderr, "isn't marked obvious by its boss") {
		t.Errorf("❌ an unmarked ask: %v %q", err, stderr)
	}
	permission := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["w"]}, permission, "question"); err != nil {
		t.Fatal(err, stderr)
	}
	asked := s.T("show-option", "-pqv", "-t", pane["w"], "@agent_question")
	shown := s.T("capture-pane", "-p", "-t", pane["w"])
	if asked == "" {
		t.Fatal("❌ w's permission prompt not recorded")
	}
	if _, stderr, err := settle(pane["manager"], "w", "1", "Yes", "allow Bash: rm -rf build"); err == nil || !strings.Contains(stderr, "never a permission prompt or dialog") {
		t.Errorf("❌ a permission prompt: %v %q", err, stderr)
	}
	if got := s.T("show-option", "-pqv", "-t", pane["w"], "@agent_question"); got != asked || s.T("capture-pane", "-p", "-t", pane["w"]) != shown {
		t.Errorf("❌ w's permission prompt touched: %q", got)
	}

	out, stderr, err := settle(pane["manager"], "boss-a", "1", "Re-run", "Re-run CI on #301?")
	if err != nil || out != "#1 settled with \"Re-run\", sent to boss-a\n" {
		t.Fatalf("❌ settle: %q %v %s", out, err, stderr)
	}
	waitFor(t, "boss-a gets the manager's note", func() bool {
		data, _ := os.ReadFile(filepath.Join(tmp, "boss-a.log"))
		return strings.Contains(string(data), `- you answered #1 "Re-run CI on #301?" settled by the manager with your recommended option: Re-run`)
	})
	list, stderr, err := dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "todo", "--all", "--json")
	if err != nil {
		t.Fatal(err, stderr)
	}
	for _, want := range []string{`"by": "manager"`, `"pick": "Re-run | known flake (Recommended)"`, `"over": [`, `"why": "known flake, 2 of 10 runs"`} {
		if !strings.Contains(list, want) {
			t.Errorf("❌ %s missing from dev todo --all --json:\n%s", want, list)
		}
	}
	if _, stderr, err := settle(pane["manager"], "boss-a", "1", "Re-run", "Re-run CI on #301?"); err == nil || !strings.Contains(stderr, "decided for the user by the manager") {
		t.Errorf("❌ settled twice: %v %q", err, stderr)
	}

	// A boss in its own permission prompt: the prompt is no item to settle, and settling a marked ask meanwhile
	// leaves the prompt as it is
	if _, stderr, err := dev(claude(pane["boss-a"]), "", "todo", "ask", "Retry the flaky test?", "--option", "Retry (Recommended)", "--option", "Skip", "--obvious", "known flake", "--details", "x"); err != nil {
		t.Fatal(err, stderr)
	}
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-a"]}, permission, "question"); err != nil {
		t.Fatal(err, stderr)
	}
	asked = s.T("show-option", "-pqv", "-t", pane["boss-a"], "@agent_question")
	shown = s.T("capture-pane", "-p", "-t", pane["boss-a"])
	if asked == "" {
		t.Fatal("❌ boss-a's permission prompt not recorded")
	}
	if _, stderr, err := settle(pane["manager"], "boss-a", "9", "Yes", "allow Bash: rm -rf build"); err == nil || !strings.Contains(stderr, "no open item 9") {
		t.Errorf("❌ boss-a's permission prompt as an item: %v %q", err, stderr)
	}
	if out, stderr, err := settle(pane["manager"], "boss-a", "3", "Retry", "Retry the flaky test?"); err != nil || !strings.HasPrefix(out, "#3 settled with \"Retry\"") {
		t.Errorf("❌ settle while boss-a is in a permission prompt: %q %v %s", out, err, stderr)
	}
	if got := s.T("show-option", "-pqv", "-t", pane["boss-a"], "@agent_question"); got != asked || s.T("capture-pane", "-p", "-t", pane["boss-a"]) != shown {
		t.Errorf("❌ boss-a's permission prompt touched: %q", got)
	}
}
