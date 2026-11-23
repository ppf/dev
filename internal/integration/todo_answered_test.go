//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/inbox"
)

// An ask of a boss under the manager answered elsewhere (the phone page, a board) tells the manager, so it drops its
// question; a late answer the manager relays from a question it had up says the item is answered already, who answered
// it, what and where: not sent, and a failure only when the user picked otherwise there.
func TestTodoAnsweredElsewhere(t *testing.T) {
	tmp := t.TempDir()
	state := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", state) // the manager's inbox, read here
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
	must := func(env []string, stdin string, argv ...string) string {
		t.Helper()
		out, stderr, err := dev(env, stdin, argv...)
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, stderr)
		}
		return out
	}

	// The manager works, so its notes wait in its inbox; boss-a is its boss
	s.T("new-session", "-d", "-s", "manager", "sleep 600")
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := s.T("display", "-t", "=manager:", "-p", "#{pane_id}")
	must([]string{"TMUX_PANE=" + manager}, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	must([]string{"TMUX_PANE=" + manager}, `{"hook_event_name":"UserPromptSubmit","prompt":"what's waiting?"}`, "state", "working")
	s.T("new-session", "-d", "-s", "boss-a", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	s.T("set-option", "-t", "=boss-a:", "@dev_started_by", "manager")
	bossA := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	must([]string{"TMUX_PANE=" + bossA}, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	waitFor(t, "boss-a box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", bossA), "Try") })
	b, ok, err := inbox.Here(s.tmux, manager)
	if !ok || err != nil {
		t.Fatalf("❌ no manager inbox: %v", err)
	}
	take := func() string {
		t.Helper()
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}
	boss := []string{"TMUX_PANE=" + bossA, "CLAUDECODE=1"}
	pick := func(question, answer string) {
		t.Helper()
		in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion",
			"tool_input": map[string]any{"questions": []map[string]string{{"question": question}}, "answers": map[string]string{question: answer}}})
		must([]string{"TMUX_PANE=" + manager}, string(in), "state", "working")
	}
	relay := func(n, answer, question string) (string, string, error) {
		return dev([]string{"TMUX_PANE=" + manager, "CLAUDECODE=1"}, "", "todo", "answer", "boss-a", n, answer, "--via", "manager", "--question", question)
	}
	answered := func() int {
		data, _ := os.ReadFile(filepath.Join(tmp, "boss-a.log"))
		return strings.Count(string(data), "you answered #")
	}

	must(boss, "", "todo", "add", "Keep me") // never ticked: item numbers don't restart
	must(boss, "", "todo", "ask", "Filter row?", "--option", "A: chips", "--option", "B: label + filter row (Recommended)")
	if got := take(); !strings.Contains(got, "- boss-a waits on you: #2 Filter row?") {
		t.Fatalf("❌ the manager's batch lacks the ask:\n%s", got)
	}
	time.Sleep(1100 * time.Millisecond)
	// The manager's question is up while the user answers on the phone page
	url := startServe(t, s, "TMUX_PANE=", "CLAUDECODE=")
	req, _ := http.NewRequest(http.MethodPost, url+"v1/answer", strings.NewReader(`{"boss":"boss-a","id":2,"question":"Filter row?","option":"B: label + filter row"}`))
	req.Header.Set("X-Dev-Page", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("❌ page answer: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	if got, want := take(), `- boss-a ask answered: #2 "Filter row?" on the phone page: B: label + filter row; drop its question: don't ask or relay it`; !strings.Contains(got, want) {
		t.Errorf("❌ the manager's batch after the page's answer:\n%s\nwant %q", got, want)
	}
	waitFor(t, "boss-a told once", func() bool { return answered() == 1 })

	// The pick comes once the item left the list: the same answer is no failure, and nothing goes to boss-a again
	time.Sleep(5500 * time.Millisecond)
	pick("boss-a #2: Filter row?", "B: label + filter row")
	out, stderr, err := relay("2", "B: label + filter row", "Filter row?")
	if want := `#2 not sent to boss-a: item 2 is answered already: the user answered "B: label + filter row" on the phone page` + "\n"; err != nil || out != want {
		t.Errorf("❌ late relay of the same answer: %q %v %s, want %q", out, err, stderr, want)
	}
	if pending, err := b.Box.Pending(); err != nil || pending {
		t.Errorf("❌ the manager's inbox after its late relay: pending %v, %v", pending, err)
	}
	if bossBox, ok, err := inbox.Here(s.tmux, bossA); !ok || err != nil {
		t.Fatalf("❌ no boss-a inbox: %v", err)
	} else if pending, err := bossBox.Box.Pending(); err != nil || pending {
		t.Errorf("❌ boss-a told again: pending %v, %v", pending, err)
	}

	// A board's answer, then a late relay of another pick while it still shows ticked
	must(boss, "", "todo", "ask", "Tag it?", "--option", "Tag", "--option", "Skip")
	take()
	time.Sleep(1100 * time.Millisecond)
	pick("boss-a #3: Tag it?", "Skip")
	must([]string{"TMUX_PANE=", "CLAUDECODE=1"}, "", "todo", "answer", "--from-board", "--question", "Tag it?", "boss-a", "3", "Tag")
	if got, want := take(), `- boss-a ask answered: #3 "Tag it?": Tag; drop its question: don't ask or relay it`; !strings.Contains(got, want) {
		t.Errorf("❌ the manager's batch after the board's answer:\n%s\nwant %q", got, want)
	}
	if _, stderr, err := relay("3", "Skip", "Tag it?"); err == nil || !strings.Contains(stderr, `item 3 is answered already: the user answered "Tag" in the sidebar, a board or a shell, not "Skip"`) {
		t.Errorf("❌ late relay of another answer: %v %q", err, stderr)
	}
	// The manager's own relay tells no one above
	must(boss, "", "todo", "ask", "Ship it?", "--option", "Ship", "--option", "Wait")
	take()
	time.Sleep(1100 * time.Millisecond)
	pick("boss-a #4: Ship it?", "Ship")
	if _, stderr, err := relay("4", "Ship", "Ship it?"); err != nil {
		t.Fatalf("❌ relay: %v %s", err, stderr)
	}
	if got := take(); strings.Contains(got, "ask answered") {
		t.Errorf("❌ the manager told of its own relay:\n%s", got)
	}
}
