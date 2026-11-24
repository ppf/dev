//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One AskUserQuestion of the manager holds up to 4 asks: --via manager relays the pick of the question naming each
// item, once; a question left unanswered or "Leave it open" leaves its item open, an item answered elsewhere
// meanwhile is refused as before, the dialog's other picks still relay, and a hand-on question beside item questions
// relays neither.
func TestTodoAnswerViaManagerDialog(t *testing.T) {
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
	claude := func(name string) []string { return []string{"TMUX_PANE=" + pane[name], "CLAUDECODE=1"} }
	ask := func(boss, question string, options ...string) {
		t.Helper()
		argv := []string{"todo", "ask", question}
		for _, o := range options {
			argv = append(argv, "--option", o)
		}
		if _, stderr, err := dev(claude(boss), "", argv...); err != nil {
			t.Fatalf("❌ %s asks %q: %v %s", boss, question, err, stderr)
		}
	}
	// The user's picks in one AskUserQuestion of the manager, as its PostToolUse hook gets them ("" unanswered)
	type q struct{ question, answer string }
	dialog := func(qs ...q) {
		t.Helper()
		var questions []map[string]any
		answers := map[string]string{}
		for _, x := range qs {
			labels := []map[string]string{{"label": "Merge"}, {"label": "Wait"}}
			if strings.HasPrefix(x.question, "Hand on to ") {
				labels = []map[string]string{{"label": "Hand it on"}, {"label": "Drop it"}}
			}
			questions = append(questions, map[string]any{"question": x.question, "options": labels})
			if x.answer != "" {
				answers[x.question] = x.answer
			}
		}
		in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion",
			"tool_input": map[string]any{"questions": questions, "answers": answers}})
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(in), "state", "working"); err != nil {
			t.Fatalf("❌ manager pick: %v %s", err, stderr)
		}
	}
	relay := func(boss, n, option, question string) (string, string, error) {
		return dev(claude("manager"), "", "todo", "answer", boss, n, option, "--via", "manager", "--question", question)
	}
	relayed := func(boss, n, option, question string) {
		t.Helper()
		if out, stderr, err := relay(boss, n, option, question); err != nil || out != "#"+n+" answered, sent to "+boss+"\n" {
			t.Errorf("❌ relay %s #%s %s: %q %v %s", boss, n, option, out, err, stderr)
		}
	}
	refused := func(what, boss, n, option, question, want string) {
		t.Helper()
		if _, stderr, err := relay(boss, n, option, question); err == nil || !strings.Contains(stderr, want) {
			t.Errorf("❌ %s: %v %q, want %q", what, err, stderr, want)
		}
	}
	// What boss got: pasted into its pane, or queued for its Stop hook
	got := map[string]string{}
	notes := func(boss string) string {
		t.Helper()
		out, stderr, err := dev([]string{"TMUX_PANE=" + pane[boss]}, `{"hook_event_name":"Stop"}`, "state", "idle")
		if err != nil {
			t.Fatalf("❌ %s's Stop: %v %s", boss, err, stderr)
		}
		log, _ := os.ReadFile(filepath.Join(tmp, boss+".log"))
		got[boss] += string(log) + strings.ReplaceAll(out, `\"`, `"`)
		return got[boss]
	}

	// An item kept open on each list, so its numbers never restart between the steps below
	if _, stderr, err := dev(claude("boss-a"), "", "todo", "add", "--no-details", "Keep"); err != nil {
		t.Fatal(err, stderr)
	}
	if _, stderr, err := dev(claude("boss-b"), "", "todo", "add", "--no-details", "Keep"); err != nil {
		t.Fatal(err, stderr)
	}
	ask("boss-a", "Merge #148?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Merge #149?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Merge #150?", "Merge (Recommended)", "Wait")
	ask("boss-b", "Merge #7?", "Merge (Recommended)", "Wait")
	time.Sleep(1100 * time.Millisecond)

	// Four asks in one dialog: each relays its own pick, once
	dialog(q{"a #2: Merge #148?", "Merge"}, q{"a #3: Merge #149?", "Wait"}, q{"boss-b #2: Merge #7?", "Merge"}, q{"a #4: Merge #150?", "Merge"})
	refused("a pick for another question of the dialog", "boss-a", "3", "Merge", "Merge #149?", `the user picked "Wait", not "Merge"`)
	relayed("boss-a", "3", "Wait", "Merge #149?")
	relayed("boss-b", "2", "Merge", "Merge #7?")
	relayed("boss-a", "2", "Merge", "Merge #148?")
	relayed("boss-a", "4", "Merge", "Merge #150?")
	refused("a pick used twice", "boss-a", "2", "Merge", "Merge #148?", "the user's pick on this question was used already")
	for boss, quotes := range map[string][]string{
		"boss-a": {`you answered #2 "Merge #148?" via manager ("a #2: Merge #148? picked Merge"): Merge`,
			`you answered #3 "Merge #149?" via manager ("a #3: Merge #149? picked Wait"): Wait`,
			`you answered #4 "Merge #150?" via manager ("a #4: Merge #150? picked Merge"): Merge`},
		"boss-b": {`you answered #2 "Merge #7?" via manager ("boss-b #2: Merge #7? picked Merge"): Merge`},
	} {
		for _, quote := range quotes {
			waitFor(t, boss+" gets "+quote, func() bool { return strings.Contains(notes(boss), quote) })
		}
	}

	ask("boss-a", "Tag v1?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Tag v2?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Tag v3?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Tag v4?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Tag v5?", "Merge (Recommended)", "Wait")
	time.Sleep(1100 * time.Millisecond)
	// The user answers #5 in their shell while the manager's dialog is up
	dialog(q{"a #5: Tag v1?", ""}, q{"a #6: Tag v2?", "Merge"}, q{"a #7: Tag v3?", "Leave it open"}, q{"a #8: Tag v4?", "Merge"})
	if _, stderr, err := dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "todo", "answer", "boss-a", "6", "Wait"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("an unanswered question", "boss-a", "5", "Merge", "Tag v1?", `the user left "a #5: Tag v1?" unanswered: the item stays open`)
	refused("Leave it open", "boss-a", "7", "Leave it open", "Tag v3?", `the user picked "Leave it open": the item stays open`)
	refused("a pick for a question not in the dialog", "boss-a", "9", "Merge", "Tag v5?", `not "a #9: Tag v5?"`)
	refused("an item answered elsewhere meanwhile", "boss-a", "6", "Merge", "Tag v2?", `item 6 is answered already: the user answered "Wait" in the sidebar, a board or a shell, not "Merge"`)
	// The refusal gave that question back, and spent no other
	refused("the answered item again", "boss-a", "6", "Merge", "Tag v2?", "is answered already")
	relayed("boss-a", "8", "Merge", "Tag v4?")
	out, _, err := dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "todo", "--boss", "boss-a")
	for _, open := range []string{"5\t☐\tTag v1?", "7\t☐\tTag v3?", "9\t☐\tTag v5?"} {
		if err != nil || !strings.Contains(out, open) {
			t.Errorf("❌ boss-a's list = %q, %v; want %q open", out, err, open)
		}
	}

	// Typed under Other in a dialog of several relays the typed words; one the user answered elsewhere the same way
	// meanwhile sends nothing and isn't an error
	ask("boss-a", "Tag v6?", "Merge (Recommended)", "Wait")
	ask("boss-a", "Tag v7?", "Merge (Recommended)", "Wait")
	time.Sleep(1100 * time.Millisecond)
	dialog(q{"a #10: Tag v6?", "Merge"}, q{"a #11: Tag v7?", "only after CI"})
	if _, stderr, err := dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "", "todo", "answer", "boss-a", "10", "Merge"); err != nil {
		t.Fatal(err, stderr)
	}
	if out, stderr, err := relay("boss-a", "10", "Merge", "Tag v6?"); err != nil || !strings.HasPrefix(out, "#10 not sent to boss-a: item 10 is answered already") {
		t.Errorf("❌ an item answered elsewhere the same way: %q %v %s", out, err, stderr)
	}
	relayed("boss-a", "11", "only after CI", "Tag v7?")
	quote := `you answered #11 "Tag v7?" via manager ("a #11: Tag v7? picked only after CI"): only after CI`
	waitFor(t, "boss-a gets "+quote, func() bool { return strings.Contains(notes("boss-a"), quote) })

	// A hand-on question worded otherwise beside an item drops the user's latest typed words: a dismissed dialog
	// leaves no earlier pick to hand on with
	typed, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "prompt": "ship the docs"})
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(typed), "state", "working"); err != nil {
		t.Fatal(err, stderr)
	}
	boxes, _ := filepath.Glob(filepath.Join(tmp, "state", "dev", "inbox", "*.json"))
	opened, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion", "tool_input": map[string]any{
		"questions": []map[string]any{{"question": "Hand `boss-a` the cache idea?", "options": []map[string]string{{"label": "Hand it on"}, {"label": "Drop it"}}},
			{"question": "a #7: Tag v3?", "options": []map[string]string{{"label": "Merge"}}}}}})
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(opened), "question"); err != nil {
		t.Fatal(err, stderr)
	}
	for _, f := range boxes {
		if data, _ := os.ReadFile(f); strings.Contains(string(data), `"text":"ship the docs"`) {
			t.Errorf("❌ a hand-on question worded otherwise beside an item kept the user's typed prompt: %s", data)
		}
	}

	// A project configured under the session's name (boss-b) takes that name from boss-b's own project: its
	// questions name only that project's boss
	ask("boss-b", "Ship v2?", "Ship (Recommended)", "Wait")
	time.Sleep(1100 * time.Millisecond)
	dialog(q{"boss-b #3: Ship v2?", "Ship"}, q{"a #5: Tag v1?", ""})
	conf := filepath.Join(tmp, "config")
	writeFile(t, filepath.Join(conf, "dev", "projects.toml"), "[[project]]\nname = \"boss-b\"\npath = \""+tmp+"\"\n")
	if _, stderr, err := dev(append(claude("manager"), "XDG_CONFIG_HOME="+conf), "", "todo", "answer", "boss-b", "3", "Ship", "--via", "manager", "--question", "Ship v2?"); err == nil || !strings.Contains(stderr, `not "b #3: Ship v2?"`) {
		t.Errorf("❌ the session's name of a configured project's: %v %q, want refused", err, stderr)
	}
	relayed("boss-b", "3", "Ship", "Ship v2?")

	// A hand-on question beside item questions: neither relays nor hands on
	dialog(q{"Hand on to boss-a?\n\nCache the CI builds.", "Hand it on"}, q{"a #5: Tag v1?", "Merge"})
	refused("an item beside a hand-on question", "boss-a", "5", "Merge", "Tag v1?", "a hand-on question stays alone in its dialog")
	if _, stderr, err := dev(claude("manager"), "", "agent", "prompt", "boss-a", "--kind", "work", "Cache the CI builds."); err == nil || !strings.Contains(stderr, "a hand-on question stays alone in its dialog") {
		t.Errorf("❌ hand on from a dialog with an item beside it: %v %q", err, stderr)
	}
}
