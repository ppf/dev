//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
)

// A worker's permission prompt under a boss the live manager started goes to the manager, which asks the user and
// answers the worker; the boss's note says to leave it, and dev agent send-keys takes keys to it only from the manager
// until it is answered or the manager is gone. With the manager's inbox off, a boss the user opened, or a
// boss under another boss, the boss relays it as before.
func TestWorkerDecisionToManager(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // take reads the boxes in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	state := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "❌") {
			t.Fatalf("❌ dev state %s: %v %s", st, err, out)
		}
	}
	// Busy bosses: their notes wait in their inboxes
	boss := func(name, by string) string {
		t.Helper()
		s.T("new-session", "-d", "-s", name, "sleep 600")
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		if by != "" {
			s.T("set-option", "-t", "="+name+":", "@dev_started_by", by)
		}
		p := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		state(p, "idle", `{"hook_event_name":"SessionStart"}`)
		state(p, "working", `{"hook_event_name":"UserPromptSubmit"}`)
		return p
	}
	take := func(pane string) string {
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
	manager := boss("manager", "")
	app := boss("boss-app", "manager")
	s.T("new-session", "-d", "-s", "app-w", "sleep 600")
	s.T("set-option", "-t", "=app-w:", "@dev_started_by", "boss-app")
	w := s.T("display", "-t", "=app-w:", "-p", "#{pane_id}")
	decide := func() {
		t.Helper()
		state(w, "working", `{"hook_event_name":"UserPromptSubmit"}`)
		state(w, "waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
	}
	const plain = "[dev boss inbox] 1 worker update(s):\n- app-w needs a decision: Claude needs your permission to use Bash"

	decide()
	// Still unanswered: the boss's stuck note leaves it too; the manager gets a reminder once its first note is out
	later := control.Clock{Now: func() time.Time { return time.Now().Add(inbox.StuckDecision + time.Minute) }, Sleep: time.Sleep}
	watch := func() {
		t.Helper()
		if err := inbox.Watch(s.tmux, later, app); err != nil {
			t.Fatal(err)
		}
	}
	watch()
	check(t, "the manager asks, no reminder beside it", take(manager), "[dev boss inbox] 1 worker update(s):\n- app-w (boss-app's worker) needs a decision: "+
		"Claude needs your permission to use Bash; ask the user, then send-keys only their pick to app-w yourself (its boss leaves it to you)")
	check(t, "the boss leaves it, stuck too", take(app), "[dev boss inbox] 2 worker update(s):\n- app-w needs a decision: Claude needs your permission to use Bash; "+
		"the manager asks the user: leave it to the manager\n- app-w stuck: waiting on a decision for 4m, unanswered; the manager asks the user: leave it to the manager")
	for range 2 {
		watch()
	}
	check(t, "the manager's reminder, once", take(manager), "[dev boss inbox] 1 worker update(s):\n- app-w (boss-app's worker) stuck: waiting on a decision for 4m, "+
		"unanswered; ask the user, then send-keys only their pick to app-w yourself (its boss leaves it to you)")
	check(t, "nothing new for the boss", take(app), "")

	// Keys to the handed decision: only from the manager's pane until it is answered
	sendKeys := func(from string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, "agent", "send-keys", "app-w", "1")
		cmd.Env = s.scriptEnv("TMUX_PANE="+from, "CLAUDECODE=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	const leave = "app-w's decision went to the manager, which asks the user and sends their pick: leave it to the manager until it is answered"
	for _, from := range []string{app, ""} {
		if out, err := sendKeys(from); err == nil || !strings.Contains(out, leave) {
			t.Errorf("❌ send-keys from %q to a decision handed to the manager: %v %q, want refused", from, err, out)
		}
	}
	if out, err := sendKeys(manager); err != nil {
		t.Errorf("❌ send-keys from the manager: %v %q", err, out)
	}
	state(w, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	if out, err := sendKeys(app); err != nil {
		t.Errorf("❌ send-keys from the boss once answered: %v %q", err, out)
	}
	take(app)
	take(manager)

	// A worker started outside tmux, or one that is itself a boss: not handed over
	s.T("set-option", "-t", "=app-w:", "@dev_started_outside", "1")
	decide()
	check(t, "outside: left to its coordinator", take(app), plain+"; started by a coordinator outside tmux: leave it to that coordinator")
	if out, err := sendKeys(app); err != nil {
		t.Errorf("❌ send-keys from the boss to a decision not handed over (outside): %v %q", err, out)
	}
	s.T("set-option", "-u", "-t", "=app-w:", "@dev_started_outside")
	s.T("set-option", "-t", "=app-w:", "@dev_herd", "1")
	decide()
	check(t, "a boss below: its own decision goes up as before", take(app), plain)
	if out, err := sendKeys(app); err != nil {
		t.Errorf("❌ send-keys from the boss to a decision not handed over (a boss below): %v %q", err, out)
	}
	s.T("set-option", "-u", "-t", "=app-w:", "@dev_herd")
	check(t, "nothing for the manager", take(manager), "")

	s.T("set-option", "-t", "=manager:", inbox.OptOff, "off")
	decide()
	check(t, "manager's inbox off: the boss relays it", take(app), plain)
	s.T("set-option", "-u", "-t", "=manager:", inbox.OptOff)
	// A later decision dev didn't hand over: the earlier hand-over doesn't hold it
	if out, err := sendKeys(app); err != nil {
		t.Errorf("❌ send-keys from the boss to a later decision not handed over: %v %q", err, out)
	}
	s.T("set-option", "-t", "=manager:", inbox.OptOff, "off")
	s.T("set-option", "-u", "-t", "=manager:", inbox.OptOff)
	check(t, "nothing for the manager", take(manager), "")

	s.T("set-option", "-u", "-t", "=boss-app:", "@dev_started_by")
	decide()
	check(t, "a boss the user opened relays it", take(app), plain)
	check(t, "nothing for the manager", take(manager), "")

	boss("boss-hq", "")
	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "boss-hq")
	decide()
	check(t, "under another boss: the boss relays it", take(app), plain)

	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "manager")
	decide()
	take(app)
	if out, err := sendKeys(app); err == nil {
		t.Errorf("❌ send-keys from the boss to a handed decision: %q, want refused", out)
	}
	s.T("kill-session", "-t", "=manager:")
	if out, err := sendKeys(app); err != nil {
		t.Errorf("❌ send-keys from the boss once the manager is gone: %v %q", err, out)
	}
	decide()
	check(t, "manager gone: the boss relays it", take(app), plain)
}
