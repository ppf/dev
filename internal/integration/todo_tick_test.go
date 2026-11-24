//go:build integration

package integration

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

// The user's tick or reopen of an item tells the list's owner at once, wherever it ran: a shell, a board, the sidebar,
// the page. Not a quiet item's, nor the owner's own tick; a closed boss's goes to the manager; a tick still queued
// when the item is reopened is dropped, said in the reopen's note.
func TestTodoTickNote(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, stderr.String())
		}
		return string(out)
	}
	state := func(pane, status, event string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", status)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ %s: %v %s", event, err, out)
		}
	}
	// busy bosses: their notes wait in their inboxes to be read here
	open := func(name string) string {
		t.Helper()
		s.T("new-session", "-d", "-s", name, "sleep 600")
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		state(pane, "idle", "SessionStart")
		state(pane, "working", "UserPromptSubmit")
		return pane
	}
	notes := func(pane string) string {
		t.Helper()
		b, ok, err := inbox.Here(s.tmux, pane)
		if !ok || err != nil {
			t.Fatalf("❌ no inbox for %s: %v", pane, err)
		}
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}
	pane, manager, paneB := open("boss-a"), open("manager"), open("boss-b")
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	for _, text := range []string{"Alpha", "Beta", "Gamma", "Delta", "Own", "Epsilon", "Keep"} {
		dev(boss, "todo", "add", "--no-details", text)
	}
	dev(boss, "todo", "add", "--quiet", "Quiet")
	dev(boss, "todo", "add", "--no-details", "Own text")
	dev([]string{"TMUX_PANE=" + paneB, "CLAUDECODE=1"}, "todo", "add", "--no-details", "Close the ticket")
	url := startServe(t, s, user...)

	const verify = `: his word that it is done; verify where it matters; dev ticked it already, don't tick it again`
	dev(user, "todo", "done", "--boss", "boss-a", "1")
	dev(user, "todo", "done", "--where", "the board", "--text", "Beta", "--boss", "boss-a", "2")
	dev(user, "todo", "done", "--where", "the sidebar", "--text", "Gamma", "--boss", "boss-a", "3")
	post(t, url, "v1/done", `{"boss":"boss-a","id":4,"text":"Delta"}`)
	dev(user, "todo", "done", "--text", "Quiet", "--boss", "boss-a", "8") // quiet: no note
	dev(boss, "todo", "done", "5")                                        // the owner's own: no note
	dev(boss, "todo", "done", "--text", "Own text", "9")                  // nor with --text, which the owner may run too
	check(t, "boss-a's notes", notes(pane), "[dev boss inbox] 4 worker update(s):\n"+
		`- the user ticked #1 "Alpha" on his shell`+verify+"\n"+
		`- the user ticked #2 "Beta" on the board`+verify+"\n"+
		`- the user ticked #3 "Gamma" on the sidebar`+verify+"\n"+
		`- the user ticked #4 "Delta" on the web page`+verify)

	// A closed boss's tick goes to the manager, naming the boss
	s.T("kill-session", "-t", "=boss-b:")
	dev(user, "todo", "done", "--boss", "boss-b", "1")
	check(t, "manager's notes", notes(manager), "[dev boss inbox] 1 worker update(s):\n"+
		`- the user ticked boss-b #1 "Close the ticket" on his shell`+verify)

	// Reopen: its own note; a tick still queued is dropped, said in the same line
	dev(user, "todo", "reopen", "boss-a", "1", "--question", "Alpha")
	dev(user, "todo", "done", "--boss", "boss-a", "6")
	dev(user, "todo", "reopen", "boss-a", "6", "--question", "Epsilon")
	check(t, "boss-a's notes after the reopens", notes(pane), "[dev boss inbox] 2 worker update(s):\n"+
		`- the user reopened #1 "Alpha": #1 is open again`+"\n"+
		`- the user reopened #6 "Epsilon": #6 is open again; his tick of it, not yet delivered, is dropped`)

	// A reopen from the page, of a closed boss's item and of a quiet one; a refused tick or a bad --where tells no one
	post(t, url, "v1/reopen", `{"boss":"boss-a","id":2,"text":"Beta"}`)
	dev(user, "todo", "reopen", "boss-b", "1", "--question", "Close the ticket")
	dev(user, "todo", "reopen", "boss-a", "8", "--question", "Quiet")
	for _, argv := range [][]string{
		{"todo", "done", "--where", "the web page", "--text", "Keep", "--boss", "boss-a", "7"},
		{"todo", "done", "--text", "Other", "--boss", "boss-a", "7"},
	} {
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(user...)
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("❌ dev %v succeeded: %s", argv, out)
		}
	}
	check(t, "boss-a's notes after the page's reopen", notes(pane), "[dev boss inbox] 1 worker update(s):\n"+
		`- the user reopened #2 "Beta": #2 is open again`)
	check(t, "manager's notes after the closed boss's reopen", notes(manager), "[dev boss inbox] 1 worker update(s):\n"+
		`- the user reopened boss-b #1 "Close the ticket": #1 is open again`)

	// An idle owner is woken: the page's tick is pasted into its pane at once
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	logC := filepath.Join(tmp, "boss-c.log")
	s.T("new-session", "-d", "-s", "boss-c", "bash", script, "", logC)
	s.T("set-option", "-t", "=boss-c:", "@dev_herd", "1")
	paneC := s.T("display", "-t", "=boss-c:", "-p", "#{pane_id}")
	state(paneC, "idle", "SessionStart")
	waitFor(t, "boss-c's box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", paneC), "Try") })
	dev([]string{"TMUX_PANE=" + paneC, "CLAUDECODE=1"}, "todo", "add", "--no-details", "Wake me")
	post(t, url, "v1/done", `{"boss":"boss-c","id":1,"text":"Wake me"}`)
	want := `- the user ticked #1 "Wake me" on the web page` + verify
	waitFor(t, "the tick pasted into idle boss-c", func() bool {
		data, _ := os.ReadFile(logC)
		return strings.Contains(string(data), want)
	})
	data, _ := os.ReadFile(logC)
	t.Logf("✅ boss-c's pane received:\n%s", data)
}

// post is a page's POST to the test server, which must answer 200.
func post(t *testing.T, url, path, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Dev-Page", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("❌ POST %s: %d", path, resp.StatusCode)
	}
}
