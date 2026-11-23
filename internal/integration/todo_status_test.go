//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dev todo --json and --all --json give each item its status from the live sessions: a worker asking the user is
// blocked, as is one resting after a failed turn, a working one working, a linked one gone is gone, a name that is
// no session a person (waiting), an item on no one yours. --on the boss's own name is the boss's own task (on boss), idle until it works on it.
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
	// Its coordinator runs: it answers a plain worker's question
	s.T("set-option", "-p", "-t", boss, "@agent_cmd", "sleep")
	s.T("set-option", "-p", "-t", boss, "@agent_state", "idle")
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
	worker("w-question", "idle", false, "asks: red or blue?")

	env := []string{"TMUX_PANE=" + boss, "CLAUDECODE=1"}
	dev(env, "todo", "add", "mine")
	_, errOut := dev(env, "todo", "add", "--on", "boss-a", "the boss's own")
	if !strings.Contains(errOut, "this list's own boss, so your own task, as --on boss") {
		t.Errorf("❌ --on boss-a stderr = %q", errOut)
	}
	dev(env, "todo", "add", "--on", "w-ask", "asks")
	dev(env, "todo", "add", "--on", "w-busy", "works")
	dev(env, "todo", "add", "--on", "w-failed", "failed")
	dev(env, "todo", "add", "--on", "w-question", "asks its boss")
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
		{"the boss's own", "boss", "parked", "boss-a"},
		{"asks", "w-ask", "blocked", "w-ask"},
		{"works", "w-busy", "working", "w-busy"},
		{"failed", "w-failed", "blocked", "w-failed"},
		{"asks its boss", "w-question", "asks", "w-question"},
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

	// Each is placed in its column, whose: the boss follows up a person and a gone worker's item
	var placed []struct {
		Items []struct {
			Text     string `json:"text"`
			Column   string `json:"column"`
			Assignee struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"assignee"`
			NeedsYou   bool   `json:"needs_you"`
			WaitingFor string `json:"waiting_for"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &placed); err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{"mine": "todo you  false", "the boss's own": "todo boss boss-a false", "asks": "progress worker w-ask true",
		"works": "progress worker w-busy false", "failed": "progress worker w-failed true",
		"asks its boss": "progress worker w-question false", "gone": "todo boss boss-a false",
		"a person's": "waiting boss boss-a false Ola"}
	if len(placed[0].Items) != len(columns) {
		t.Errorf("❌ placed %d items, want %d", len(placed[0].Items), len(columns))
	}
	for _, it := range placed[0].Items {
		got := fmt.Sprintf("%s %s %s %v", it.Column, it.Assignee.Kind, it.Assignee.Name, it.NeedsYou)
		if it.WaitingFor != "" {
			got += " " + it.WaitingFor
		}
		if got != columns[it.Text] {
			t.Errorf("❌ %q placed %q, want %q", it.Text, got, columns[it.Text])
		}
	}

	// An item an older dev filed on the boss's own name reads yours, and a board's tick (done --text) ticks it
	files, _ := filepath.Glob(filepath.Join(tmp, "state", "dev", "todo", "*boss-a.json"))
	if len(files) != 1 {
		t.Fatalf("❌ boss-a's list: %q", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	id := 0
	for _, it := range f["items"].([]any) {
		if it := it.(map[string]any); it["text"] == "mine" {
			it["on"], id = "boss-a", int(it["id"].(float64))
		}
	}
	if raw, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ = dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "todo", "--all", "--json")
	if err := json.Unmarshal([]byte(out), &all); err != nil || byText(all[0].Items)["mine"] != (item{"mine", "boss-a", "yours", ""}) {
		t.Errorf("❌ filed on its own boss: %v %+v", err, byText(all[0].Items)["mine"])
	}
	dev([]string{"TMUX_PANE=", "CLAUDECODE="}, "todo", "done", "--boss", "boss-a", "--text", "mine", strconv.Itoa(id))
	out, _ = dev(env, "todo", "--json")
	var ticked struct {
		Items []struct {
			Text string `json:"text"`
			Done bool   `json:"done"`
		}
	}
	if err := json.Unmarshal([]byte(out), &ticked); err != nil {
		t.Fatal(err)
	}
	for _, it := range ticked.Items {
		if it.Text == "mine" && !it.Done {
			t.Errorf("❌ filed on its own boss: not ticked")
		}
	}
}
