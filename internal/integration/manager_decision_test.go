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
// answers the worker; the boss's note says to leave it. With the manager's inbox off, a boss the user opened, or a
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
	check(t, "the manager asks", take(manager), "[dev boss inbox] 1 worker update(s):\n- app-w (boss-app's worker) needs a decision: "+
		"Claude needs your permission to use Bash; ask the user, then send-keys only their pick to app-w yourself (its boss leaves it to you)")
	check(t, "the boss leaves it", take(app), plain+"; the manager asks the user: leave it to the manager")
	// Still unanswered: the boss's stuck note leaves it too, the manager gets a reminder, once
	later := control.Clock{Now: func() time.Time { return time.Now().Add(inbox.StuckDecision + time.Minute) }, Sleep: time.Sleep}
	for range 2 {
		if err := inbox.Watch(s.tmux, later, app); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "the boss's stuck note", take(app), "[dev boss inbox] 1 worker update(s):\n- app-w stuck: waiting on a decision for 4m, unanswered; "+
		"the manager asks the user: leave it to the manager")
	check(t, "the manager's reminder", take(manager), "[dev boss inbox] 1 worker update(s):\n- app-w (boss-app's worker) stuck: waiting on a decision for 4m, "+
		"unanswered; ask the user, then send-keys only their pick to app-w yourself (its boss leaves it to you)")

	// A worker started outside tmux, or one that is itself a boss: not handed over
	s.T("set-option", "-t", "=app-w:", "@dev_started_outside", "1")
	decide()
	check(t, "outside: left to its coordinator", take(app), plain+"; started by a coordinator outside tmux: leave it to that coordinator")
	s.T("set-option", "-u", "-t", "=app-w:", "@dev_started_outside")
	s.T("set-option", "-t", "=app-w:", "@dev_herd", "1")
	decide()
	check(t, "a boss below: its own decision goes up as before", take(app), plain)
	s.T("set-option", "-u", "-t", "=app-w:", "@dev_herd")
	check(t, "nothing for the manager", take(manager), "")

	s.T("set-option", "-t", "=manager:", inbox.OptOff, "off")
	decide()
	check(t, "manager's inbox off: the boss relays it", take(app), plain)
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
	s.T("kill-session", "-t", "=manager:")
	decide()
	check(t, "manager gone: the boss relays it", take(app), plain)
}
