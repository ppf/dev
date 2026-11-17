//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

// A boss messages the manager, which replies --done: each message reaches the other's inbox as one pasted line,
// sender and thread stamped by dev. A plain pane can't send, nobody replies after --done, and outside a boss
// dev agent messages lists every thread.
func TestBossMessages(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(pane string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), &exec.ExitError{Stderr: []byte(stderr.String())}
		}
		return string(out), nil
	}
	refused := func(pane, want string, argv ...string) {
		t.Helper()
		_, err := dev(pane, argv...)
		if e, ok := err.(*exec.ExitError); !ok || !strings.Contains(string(e.Stderr), want) {
			t.Errorf("❌ dev %s: %v, want refused with %q", strings.Join(argv, " "), err, want)
		}
	}
	logOf := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(tmp, name+".log"))
		return string(data)
	}
	pane := map[string]string{}
	for _, boss := range []string{"boss-a", inbox.Manager} {
		s.T("new-session", "-d", "-s", boss, "bash", script, "", filepath.Join(tmp, boss+".log"))
		s.T("set-option", "-t", "="+boss+":", "@dev_herd", "1")
		pane[boss] = s.T("display", "-t", "="+boss+":", "-p", "#{pane_id}")
		cmd := exec.Command(devBin, "state", "idle")
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane[boss])
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ register %s: %v %s", boss, err, out)
		}
	}
	s.T("new-session", "-d", "-s", "plain", "sleep 600")
	pane["plain"] = s.T("display", "-t", "=plain:", "-p", "#{pane_id}")
	waitFor(t, "stub boxes drawn", func() bool {
		return strings.Contains(s.T("capture-pane", "-p", "-t", pane["boss-a"]), "Try") &&
			strings.Contains(s.T("capture-pane", "-p", "-t", pane[inbox.Manager]), "Try")
	})

	refused(pane["plain"], "plain is not a boss session", "agent", "message", "boss-a", "hi")
	refused("", "run it in a boss's coordinator pane", "agent", "message", "boss-a", "hi")
	refused(pane["boss-a"], "plain is not a boss session", "agent", "message", "plain", "hi")
	refused(pane["boss-a"], "no session nobody", "agent", "message", "nobody", "hi")
	refused(pane["boss-a"], "unknown flag: --boss", "agent", "message", "manager", "hi", "--boss", "boss-a")

	out, err := dev(pane["boss-a"], "agent", "message", inbox.Manager, "Is 0.57.0 out?")
	check(t, "message sent", out, "m1 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "manager gets the message", func() bool {
		return strings.Contains(logOf(inbox.Manager), "- message m1 from boss-a: Is 0.57.0 out?\n")
	})

	out, err = dev(pane[inbox.Manager], "agent", "reply", "m1", "Not yet", "--done")
	check(t, "reply sent", out, "m2 sent to boss-a\n")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "boss-a gets the reply", func() bool {
		return strings.Contains(logOf("boss-a"), "- message m2 from manager (thread m1, reply to m1, done: the thread is closed): Not yet\n")
	})
	refused(pane["boss-a"], "thread m1 was ended with --done", "agent", "reply", "m2", "ok")

	out, err = dev("", "agent", "messages", "--json")
	var threads []inbox.Thread
	if err != nil || json.Unmarshal([]byte(out), &threads) != nil || len(threads) != 1 || threads[0].State != inbox.ThreadDone || len(threads[0].Messages) != 2 {
		t.Errorf("❌ messages --json = %s, %v", out, err)
	}
	out, _ = dev(pane["boss-a"], "agent", "messages")
	if !strings.HasPrefix(out, "m1 done 2/6 boss-a → manager\n") {
		t.Errorf("❌ boss-a's messages = %q", out)
	}
}
