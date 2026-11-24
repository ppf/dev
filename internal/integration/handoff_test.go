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

// The manager's dev agent prompt and message to a boss need --kind; work passes only on the user's "Hand it on" pick
// of a question naming the boss and the brief's first line, once. Everything sent is listed by dev agent handoffs,
// ungated first, with dropped and corrected hand-on questions counted. A boss is not affected.
func TestManagerHandOnGate(t *testing.T) {
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
	for _, name := range []string{"boss-a", "boss-b", "manager"} {
		s.T("new-session", "-d", "-s", name, "bash", script, "", filepath.Join(tmp, name+".log"))
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane[name] = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane[name]}, `{"hook_event_name":"SessionStart"}`, "state", "idle"); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, stderr)
		}
		waitFor(t, name+" box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane[name]), "Try") })
	}
	manager := []string{"TMUX_PANE=" + pane["manager"], "CLAUDECODE=1"}
	hook := func(event map[string]any, state string) {
		t.Helper()
		in, _ := json.Marshal(event)
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(in), "state", state); err != nil {
			t.Fatalf("❌ manager hook: %v %s", err, stderr)
		}
	}
	typed := func(text string) {
		hook(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": text}, "working")
	}
	ask := func(question, answer string) {
		hook(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
			"questions": []map[string]any{{"question": question, "options": []map[string]string{{"label": "Hand it on"}, {"label": "Drop it"}}}},
			"answers":   map[string]string{question: answer}}}, "working")
	}
	brief := "Cache the CI builds.\ncontract first"
	question := "Hand on to boss-a? Sound: faster CI. Cons: cache misses.\n\nCache the CI builds."
	work := func() (string, string, error) {
		return dev(manager, "", "agent", "prompt", "boss-a", "--kind", "work", brief)
	}
	refused := func(what, want string, argv ...string) {
		t.Helper()
		if _, stderr, err := dev(manager, "", argv...); err == nil || !strings.Contains(stderr, want) {
			t.Errorf("❌ %s: %v %q, want %q", what, err, stderr, want)
		}
	}

	refused("prompt without --kind", "need --kind work|question|pick|own", "agent", "prompt", "boss-a", brief)
	refused("message without --kind", "need --kind", "agent", "message", "boss-a", brief)
	refused("an unknown kind", "--kind idea: one of", "agent", "prompt", "boss-a", "--kind", "idea", brief)
	refused("work before any prompt", `hasn't picked "Hand it on" here yet`, "agent", "prompt", "boss-a", "--kind", "work", brief)
	typed("cache the CI builds please")
	refused("work on a typed message", "a typed message, not a pick", "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "Drop it")
	refused("work on Drop it", `the user picked "Drop it": not handed on`, "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "only the release builds")
	refused("work on a correction", `the user typed "only the release builds" under Other`, "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask("Hand on to boss-a? Sound.\n\nRename the repo.", "Hand it on")
	refused("work on a pick for another brief", "the user's pick was for", "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "Hand it on")
	refused("work on a pick for another boss", "not boss-b", "agent", "prompt", "boss-b", "--kind", "work", brief)

	// A prompt refused before it was typed gives the pick back
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"UserPromptSubmit","prompt":"x"}`, "state", "working"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("work to a working boss with --wait", "wait for it first", "agent", "prompt", "boss-a", "--kind", "work", "--wait", brief)
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"Stop"}`, "state", "idle"); err != nil {
		t.Fatal(err, stderr)
	}
	if out, stderr, err := work(); err != nil {
		t.Fatalf("❌ work on the pick: %q %v %s", out, err, stderr)
	}
	waitFor(t, "boss-a gets the brief", func() bool {
		data, _ := os.ReadFile(filepath.Join(tmp, "boss-a.log"))
		return strings.Contains(string(data), "Cache the CI builds.")
	})
	if _, stderr, err := work(); err == nil || !strings.Contains(stderr, "pick was used already") {
		t.Errorf("❌ work twice on one pick: %v %q", err, stderr)
	}
	// Only the latest prompt counts: words typed after the pick
	ask(question, "Hand it on")
	typed("wait, not yet")
	refused("work on a typed message after the pick", "a typed message, not a pick", "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "Hand it on")
	if out, stderr, err := dev(manager, "", "agent", "message", "boss-a", "--kind", "work", brief); err != nil {
		t.Fatalf("❌ work as a message on the pick: %q %v %s", out, err, stderr)
	}
	if out, stderr, err := dev(manager, "", "agent", "message", "boss-b", "--kind", "question", "Is #150 merged?"); err != nil {
		t.Fatalf("❌ an ungated question: %q %v %s", out, err, stderr)
	}

	// A boss sends as before, and --kind is the manager's
	boss := []string{"TMUX_PANE=" + pane["boss-a"], "CLAUDECODE=1"}
	if out, stderr, err := dev(boss, "", "agent", "message", "boss-b", "Is #150 merged?"); err != nil {
		t.Fatalf("❌ a boss's message: %q %v %s", out, err, stderr)
	}
	if _, stderr, err := dev(boss, "", "agent", "message", "boss-b", "--kind", "question", "Is #150 merged?"); err == nil || !strings.Contains(stderr, "--kind is the manager's") {
		t.Errorf("❌ --kind from a boss: %v %q", err, stderr)
	}

	// The manager's reply to a boss's message is gated the same way
	out, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-b"], "CLAUDECODE=1"}, "", "agent", "message", "manager", "Shall I rebase #150?")
	if err != nil {
		t.Fatalf("❌ boss-b's message to the manager: %q %v %s", out, err, stderr)
	}
	id := strings.Fields(out)[0]
	refused("reply without --kind", "need --kind", "agent", "reply", id, "Yes, rebase it")
	if out, stderr, err := dev(manager, "", "agent", "reply", id, "--kind", "own", "Yes, rebase it"); err != nil {
		t.Fatalf("❌ an ungated reply: %q %v %s", out, err, stderr)
	}

	out, stderr, err = dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "agent", "handoffs")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 7 || lines[0] != "gated 2 · ungated: question 1, pick 0, own 1 · dropped 1 · corrected 1" ||
		!strings.HasSuffix(lines[1], " own boss-b: Yes, rebase it") ||
		!strings.HasSuffix(lines[2], " question boss-b: Is #150 merged?") || !strings.HasSuffix(lines[3], " work boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[4], " work boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[5], " corrected: Hand on to boss-a? Sound: faster CI. Cons: cache misses.") ||
		!strings.HasSuffix(lines[6], " dropped: Hand on to boss-a? Sound: faster CI. Cons: cache misses.") {
		t.Errorf("❌ dev agent handoffs = %q, %v %s", out, err, stderr)
	}
}
