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

// The manager settles a boss's ask with dev todo answer --recommended --via manager: only from its coordinator pane, an
// unmarked ask only with --why, never a permission prompt; the item shows who decided and the boss gets the note.
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
		{"todo", "ask", "Rebuild the dev server?", "--option", "Rebuild (Recommended)", "--option", "Wait", "--details", "x"},
	} {
		if _, stderr, err := dev(claude(pane["boss-a"]), "", ask...); err != nil {
			t.Fatal(err, stderr)
		}
	}

	// Refusals: outside the manager's pane, an unmarked ask, a worker's permission prompt
	if _, stderr, err := settle(pane["boss-a"], "boss-a", "1", "Re-run", "Re-run CI on #301?"); err == nil || !strings.Contains(stderr, "only in the manager's coordinator pane") {
		t.Errorf("❌ from boss-a's pane: %v %q", err, stderr)
	}
	if _, stderr, err := settle(pane["manager"], "boss-a", "2", "Rebuild", "Rebuild the dev server?"); err == nil || !strings.Contains(stderr, "isn't marked obvious by its boss") {
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

	// The user overrules #1 with the other option: only the user, only among the others; the boss gets their answer
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	overrule := func(env []string, pick string) (string, string, error) {
		return dev(env, "", "todo", "overrule", "boss-a", "1", pick, "--question", "Re-run CI on #301?")
	}
	if _, stderr, err := overrule(claude(pane["boss-a"]), "Look first"); err == nil || !strings.Contains(stderr, "only the user overrules") {
		t.Errorf("❌ overrule from a Claude Code session: %v %q", err, stderr)
	}
	if _, stderr, err := overrule(user, "Re-run"); err == nil || !strings.Contains(stderr, `other options are "Look first"`) {
		t.Errorf("❌ overrule with the decided pick: %v %q", err, stderr)
	}
	if out, stderr, err := overrule(user, "2"); err != nil || out != "#1 overruled with \"Look first\", sent to boss-a\n" {
		t.Fatalf("❌ overrule: %q %v %s", out, err, stderr)
	}
	// boss-a's box holds the first note's paste: its Stop hook takes the overrule
	want := `- you answered #1 "Re-run CI on #301?" overruled by the user (the manager had settled it with Re-run): Look first`
	var hook struct{ Reason string }
	out, stderr, err = dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"Stop"}`, "state", "idle")
	if err != nil || json.Unmarshal([]byte(out), &hook) != nil || !strings.Contains(hook.Reason, want) {
		t.Errorf("❌ boss-a's Stop hook: %v %s %q", err, stderr, out)
	}
	if list, stderr, err = dev(user, "", "todo", "--json", "--boss", "boss-a"); err != nil || strings.Contains(list, `"by": "manager"`) || !strings.Contains(list, `"answer": "Look first"`) {
		t.Errorf("❌ after the overrule: %v %s\n%s", err, stderr, list)
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

	// #3's settled note still queued (boss-a in its prompt): the overrule withdraws it, the boss hears only the user's pick
	if out, stderr, err := dev(user, "", "todo", "overrule", "boss-a", "3", "Skip", "--question", "Retry the flaky test?"); err != nil || !strings.HasPrefix(out, "#3 overruled with \"Skip\"") {
		t.Fatalf("❌ overrule of #3: %q %v %s", out, err, stderr)
	}
	out, stderr, err = dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"Stop"}`, "state", "idle")
	if err != nil || json.Unmarshal([]byte(out), &hook) != nil || !strings.Contains(hook.Reason, `#3 "Retry the flaky test?" overruled by the user`) ||
		strings.Contains(hook.Reason, `#3 "Retry the flaky test?" settled by the manager`) {
		t.Errorf("❌ boss-a's notes after #3's overrule: %v %s %q", err, stderr, out)
	}

	// An unmarked ask: settled only with --why, never one with no recommended option or a hand-on question; the user
	// overrules it as any settled ask
	for _, ask := range [][]string{
		{"todo", "ask", "Which port?", "--option", "Port a", "--option", "Port b", "--details", "x"},
		{"todo", "ask", "Ship the parser?", "--option", "Hand it on (Recommended)", "--option", "Drop it", "--details", "x"},
		{"todo", "ask", "Hand on to boss-b: the parser?", "--option", "Yes (Recommended)", "--option", "No", "--details", "x"},
	} {
		if _, stderr, err := dev(claude(pane["boss-a"]), "", ask...); err != nil {
			t.Fatal(err, stderr)
		}
	}
	settleWhy := func(n, pick, question, why string) (string, string, error) {
		return dev(claude(pane["manager"]), "", "todo", "answer", "boss-a", n, pick, "--recommended", "--question", question, "--via", "manager", "--why", why)
	}
	for _, tt := range []struct{ n, pick, question, want string }{
		{"4", "Port a", "Which port?", "no one recommended option"},
		{"5", "Hand it on", "Ship the parser?", "is a hand-on question"},
		{"6", "Yes", "Hand on to boss-b: the parser?", "is a hand-on question"},
	} {
		if _, stderr, err := settleWhy(tt.n, tt.pick, tt.question, "cheap"); err == nil || !strings.Contains(stderr, tt.want) {
			t.Errorf("❌ settle #%s with --why: %v %q, want %q", tt.n, err, stderr, tt.want)
		}
	}
	if out, stderr, err := settleWhy("2", "Rebuild", "Rebuild the dev server?", "local, rebuilt in a minute"); err != nil || out != "#2 settled with \"Rebuild\", sent to boss-a\n" {
		t.Fatalf("❌ settle of unmarked #2 with --why: %q %v %s", out, err, stderr)
	}
	if list, stderr, err = dev(user, "", "todo", "--json", "--boss", "boss-a"); err != nil || !strings.Contains(list, `"why": "local, rebuilt in a minute"`) || !strings.Contains(list, `"by": "manager"`) {
		t.Errorf("❌ #2 not decided by the manager with its why: %v %s\n%s", err, stderr, list)
	}
	if out, stderr, err := dev(user, "", "todo", "overrule", "boss-a", "2", "Wait", "--question", "Rebuild the dev server?"); err != nil || out != "#2 overruled with \"Wait\", sent to boss-a\n" {
		t.Errorf("❌ overrule of #2: %q %v %s", out, err, stderr)
	}
}
