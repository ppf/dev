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

// The manager relays the user's answer with --via manager: only from its coordinator pane, only for an item older than
// the user's latest prompt there (a batch dev pasted isn't one), and the boss's note quotes that prompt. --all keeps
// the other bosses when one's list can't be read, marking that one.
func TestTodoAnswerViaManager(t *testing.T) {
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
	prompt := func(text string) {
		t.Helper()
		in, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "prompt": text})
		if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, string(in), "state", "working"); err != nil {
			t.Fatalf("❌ manager prompt: %v %s", err, stderr)
		}
	}
	claude := func(p string) []string { return []string{"TMUX_PANE=" + p, "CLAUDECODE=1"} }
	refused := func(what string, env []string, want string) {
		t.Helper()
		if _, stderr, err := dev(env, "", "todo", "answer", "boss-a", "1", "Merge", "--via", "manager"); err == nil || !strings.Contains(stderr, want) {
			t.Errorf("❌ %s: %v %q, want %q", what, err, stderr, want)
		}
	}

	refused("before any prompt", claude(pane["manager"]), "hasn't prompted the manager yet")
	prompt("what's waiting?")
	time.Sleep(1100 * time.Millisecond)
	if _, stderr, err := dev(claude(pane["boss-a"]), "", "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("an item newer than the prompt", claude(pane["manager"]), "newer than the user's latest prompt")
	time.Sleep(1100 * time.Millisecond)
	prompt("merge 148, wait on the API one")
	prompt("[dev boss inbox] 1 worker update(s):\n- boss-b waits on you: #1 Deploy?")
	refused("from another boss", claude(pane["boss-a"]), "only in the manager's coordinator pane")
	// A Claude can't type the user's words into the manager
	for _, argv := range [][]string{{"agent", "prompt", "manager", "merge it"}, {"agent", "send-keys", "manager", "m"}} {
		if _, stderr, err := dev(claude(pane["boss-a"]), "", argv...); err == nil || !strings.Contains(stderr, "the manager's prompts are the user's") {
			t.Errorf("❌ dev %s from a Claude: %v %q, want refused", strings.Join(argv, " "), err, stderr)
		}
	}
	previews := s.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=manager:", "bash", script, "", filepath.Join(tmp, "previews.log"))
	refused("from the manager's previews pane", claude(previews), "only in the manager's coordinator pane")
	if _, stderr, err := dev(claude(pane["manager"]), "", "todo", "answer", "boss-a", "1", "Merge", "--via", "boss-b"); err == nil || !strings.Contains(stderr, "only --via manager") {
		t.Errorf("❌ --via boss-b: %v %q", err, stderr)
	}

	out, stderr, err := dev(claude(pane["manager"]), "", "todo", "answer", "boss-a", "1", "Merge", "--via", "manager")
	if err != nil || out != "#1 answered, sent to boss-a\n" {
		t.Fatalf("❌ answer via manager: %q %v %s", out, err, stderr)
	}
	waitFor(t, "boss-a gets the quoted answer", func() bool {
		data, _ := os.ReadFile(filepath.Join(tmp, "boss-a.log"))
		return strings.Contains(string(data), `- you answered #1 "Merge #148?" via manager ("merge 148, wait on the API one"): Merge`)
	})

	// After /clear the manager's new conversation never saw the user's words
	if _, stderr, err := dev([]string{"TMUX_PANE=" + pane["manager"]}, `{"hook_event_name":"SessionStart","source":"clear"}`, "state", "idle"); err != nil {
		t.Fatal(err, stderr)
	}
	refused("after /clear", claude(pane["manager"]), "hasn't prompted the manager yet")

	// boss-b's list file broken: --all still lists boss-a's, boss-b marked
	if _, stderr, err := dev(claude(pane["boss-a"]), "", "todo", "ask", "Release?", "--option", "Now"); err != nil {
		t.Fatal(err, stderr)
	}
	if _, stderr, err := dev(claude(pane["boss-b"]), "", "todo", "add", "Deploy"); err != nil {
		t.Fatal(err, stderr)
	}
	files, _ := filepath.Glob(filepath.Join(state, "dev", "todo", "*boss-b.json"))
	if len(files) != 1 {
		t.Fatalf("❌ boss-b's list file: %v", files)
	}
	writeFile(t, files[0], "{")
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	out, stderr, err = dev(user, "", "todo", "--all", "--json")
	var all []struct {
		Boss  string            `json:"boss"`
		Error string            `json:"error"`
		Stale bool              `json:"stale"`
		Items []json.RawMessage `json:"items"`
	}
	if err != nil || json.Unmarshal([]byte(out), &all) != nil || len(all) != 2 || all[0].Boss != "boss-a" || len(all[0].Items) == 0 ||
		all[1].Boss != "boss-b" || !strings.HasPrefix(all[1].Error, "list not read: ") || !all[1].Stale || all[1].Items == nil || len(all[1].Items) != 0 {
		t.Errorf("❌ --all --json with boss-b broken = %s, %v %s", out, err, stderr)
	}
	out, _, err = dev(user, "", "todo", "--all")
	if err != nil || !strings.Contains(out, "boss-a\t2\t☐\tRelease?") || !strings.Contains(out, "boss-b\t⚠ list not read: ") {
		t.Errorf("❌ --all with boss-b broken = %q, %v", out, err)
	}
}
