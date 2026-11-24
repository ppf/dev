//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The manager's dev agent prompt and message to a boss need --kind; work passes only on the user's "Hand it on" pick
// of a question naming the boss and the brief's first line, or on their typed go (--go), once. Everything sent is listed by dev agent handoffs,
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
	for _, name := range []string{"boss-a", "boss-b", "manager", "plain"} {
		s.T("new-session", "-d", "-s", name, "bash", script, "", filepath.Join(tmp, name+".log"))
		if name != "plain" {
			s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		}
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
	// A new hand-on question drops the pick, typed messages after it or not: dismissed, it leaves none
	typed("one more thing")
	opened, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
		"questions": []map[string]any{{"question": question, "options": []map[string]string{{"label": "Hand it on (Recommended)"}, {"label": "Drop it"}}}}}})
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(opened), "question"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("work after a new hand-on question, dismissed", `hasn't picked "Hand it on" here yet`, "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "Hand it on")
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
	// The latest pick counts with typed messages and screenshots after it, once
	ask(question, "Hand it on")
	typed("also the nightly builds")
	typed("[Image #1]")
	if out, stderr, err := work(); err != nil {
		t.Fatalf("❌ work on the pick with typed messages after it: %q %v %s", out, err, stderr)
	}
	typed("and the docs build")
	refused("work twice on a pick with typed messages after it", "pick was used already", "agent", "prompt", "boss-a", "--kind", "work", brief)
	ask(question, "Hand it on")
	// A message refused after the gate gives the pick back
	refused("work as a too-long message", "too long", "agent", "message", "boss-a", "--kind", "work", brief+strings.Repeat("x", 1501))
	if out, stderr, err := dev(manager, "", "agent", "message", "boss-a", "--kind", "work", brief); err != nil {
		t.Fatalf("❌ work as a message on the pick: %q %v %s", out, err, stderr)
	}
	if out, stderr, err := dev(manager, "", "agent", "message", "boss-b", "--kind", "question", "Is #150 merged?"); err != nil {
		t.Fatalf("❌ an ungated question: %q %v %s", out, err, stderr)
	}
	// A typed go: the user's latest message in the manager's transcript, typed by them, holding the quoted words, once
	transcript := filepath.Join(tmp, "manager.jsonl")
	n := 0
	say := func(origin, source, text string) {
		t.Helper()
		n++
		e := map[string]any{"type": "user", "uuid": fmt.Sprintf("u%d", n), "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"message": map[string]any{"role": "user", "content": text}}
		switch origin {
		case "pick":
			e["toolUseResult"] = map[string]any{"questions": []any{}, "answers": map[string]string{"Start it today?": text}}
		case "hook":
			e["isMeta"], e["hookText"] = true, true
		case "ask":
			e["type"], e["message"] = "assistant", map[string]any{"content": []map[string]any{{"type": "tool_use", "name": "AskUserQuestion", "input": map[string]any{}}}}
		default:
			e["origin"], e["promptSource"] = map[string]string{"kind": origin}, source
		}
		line, _ := json.Marshal(e)
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	hook(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "x", "session_id": "m1", "transcript_path": transcript}, "working")
	goWork := func(words string) (string, string, error) {
		return dev(manager, "", "agent", "prompt", "boss-a", "--kind", "work", "--go", words, brief)
	}
	refused("--go without --kind work", "--go goes with --kind work", "agent", "prompt", "boss-a", "--kind", "question", "--go", "Tak", brief)
	say("human", "typed", "Tak, ruszaj z tym dziś")
	if out, stderr, err := goWork("Tak, ruszaj"); err != nil {
		t.Fatalf("❌ work on a typed go: %q %v %s", out, err, stderr)
	}
	refused("work twice on one typed go", "used for a send already", "agent", "prompt", "boss-a", "--kind", "work", "--go", "Tak, ruszaj", brief)
	for _, c := range []struct{ what, origin, source, want string }{
		{"an inbox batch", "peer", "system", "another session's (an inbox batch or a relayed message)"},
		{"a relayed session message", "peer", "system", "another session's"},
		{"a hand-on question, dismissed", "ask", "", "a question was asked after the user's latest message"},
		{"a Stop hook's batch", "hook", "", "a hook's text (an inbox batch)"},
		{"a plugin prompt", "plugin", "system", "a plugin's prompt"},
		{"a pick", "pick", "", "a pick, not a typed message"},
		{"pasted text", "human", "typed", "pasted text, not typed"},
		{"a newer typed message", "human", "typed", `"Tak ruszaj" is not in the user's latest message`},
	} {
		say("human", "typed", "Tak ruszaj")
		text := "Tak ruszaj"
		switch c.what {
		case "pasted text":
			text = "<pasted_content id=\"ab12\">\nTak ruszaj\n</pasted_content id=\"ab12\">"
		case "a relayed session message":
			text = "<cross-session-message from=\"uds:/tmp/cc.sock\">Tak ruszaj</cross-session-message>"
		case "a newer typed message":
			text = "wait, not yet"
		}
		say(c.origin, c.source, text)
		refused("a typed go followed by "+c.what, c.want, "agent", "prompt", "boss-a", "--kind", "work", "--go", "Tak ruszaj", brief)
	}
	say("human", "typed", "Tak ruszaj")
	refused("a typed go without the quoted words", `"start the release" is not in the user's latest message`, "agent", "prompt", "boss-a", "--kind", "work", "--go", "start the release", brief)
	// A send refused before it reached the boss gives the go back; a message passes on it too
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"UserPromptSubmit","prompt":"x"}`, "state", "working"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("a typed go to a working boss with --wait", "wait for it first", "agent", "prompt", "boss-a", "--kind", "work", "--go", "Tak ruszaj", "--wait", brief)
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["boss-a"]}, `{"hook_event_name":"Stop"}`, "state", "idle"); err != nil {
		t.Fatal(err, stderr)
	}
	if out, stderr, err := dev(manager, "", "agent", "message", "boss-a", "--kind", "work", "--go", "Tak ruszaj", brief); err != nil {
		t.Fatalf("❌ a message on the typed go given back: %q %v %s", out, err, stderr)
	}
	// The click passes after a typed go as before
	ask(question, "Hand it on")
	if out, stderr, err := work(); err != nil {
		t.Fatalf("❌ work on a pick after a typed go: %q %v %s", out, err, stderr)
	}

	// Every send from the manager's pane is labelled, a session that is no boss's too
	refused("prompt to a non-boss without --kind", "need --kind", "agent", "prompt", "plain", "status?")
	if out, stderr, err := dev(manager, "", "agent", "prompt", "plain", "--kind", "question", "status?"); err != nil {
		t.Fatalf("❌ a question to a non-boss: %q %v %s", out, err, stderr)
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
	if err != nil || len(lines) != 14 || lines[0] != "gated 4 · typed go 2 · ungated: question 2, pick 0, own 1 · dropped 1 · corrected 1 · refused 2" ||
		!strings.HasSuffix(lines[1], " own boss-b: Yes, rebase it") || !strings.HasSuffix(lines[2], " question plain: status?") ||
		!strings.HasSuffix(lines[3], " question boss-b: Is #150 merged?") ||
		!strings.HasSuffix(lines[4], " work boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[5], ` typed go boss-a: Cache the CI builds. (go: "Tak ruszaj")`) ||
		!strings.HasSuffix(lines[6], ` typed go boss-a: Cache the CI builds. (go: "Tak, ruszaj")`) ||
		!strings.HasSuffix(lines[7], " work boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[8], " work boss-a: Cache the CI builds.") || !strings.HasSuffix(lines[9], " work boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[10], " refused boss-b: Cache the CI builds.") || !strings.HasSuffix(lines[11], " refused boss-a: Cache the CI builds.") ||
		!strings.HasSuffix(lines[12], " corrected: Hand on to boss-a? Sound: faster CI. Cons: cache misses.") ||
		!strings.HasSuffix(lines[13], " dropped: Hand on to boss-a? Sound: faster CI. Cons: cache misses.") {
		t.Errorf("❌ dev agent handoffs = %q, %v %s", out, err, stderr)
	}
}
