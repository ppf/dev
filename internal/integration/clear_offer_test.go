//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/bossclear"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/todo"
)

// A test boss idle past the limit gets exactly one note: dev's watch round offers the manager to clear a boss of its
// that rested bossclear.IdleLimit with every check passing, once per idle stretch, however many rounds and other notes
// come; a new stretch offers again. Too soon, too little context, a worker working or no manager over it: none.
func TestClearOffer(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Offer and the inbox reads run in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	stub := clearStub(t, filepath.Join(tmp, "bin"))
	pane := func(session string) string { return s.T("display", "-t", "="+session+":", "-p", "#{pane_id}") }
	opt := func(p, name string) string { return s.T("display", "-t", p, "-p", "#{"+name+"}") }
	state := func(p, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE="+p, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "❌") {
			t.Fatalf("❌ dev state %s: %v %s", st, err, out)
		}
	}
	// A working manager: its notes stay in its inbox to be read
	s.T("new-session", "-d", "-s", "manager", "sleep 600")
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := pane("manager")
	state(manager, "idle", `{"hook_event_name":"SessionStart"}`)
	state(manager, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	take := func() string {
		t.Helper()
		b, ok, err := inbox.Here(s.tmux, manager)
		if !ok || err != nil {
			t.Fatalf("❌ no manager inbox: %v", err)
		}
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}

	s.T("new-session", "-d", "-s", "boss-app", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=boss-app:", "@dev_herd", "1")
	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "manager")
	app := pane("boss-app")
	s.T("set-option", "-p", "-t", app, "@dev_boss_pane", "coordinator")
	waitFor(t, "boss-app registered", func() bool { return opt(app, "@agent_session") == "s0" })
	s.T("set-option", "-p", "-t", app, "@agent_context", "30")
	// round is one watch round's offer, after past the boss's idle stretch's start
	round := func(after time.Duration) {
		t.Helper()
		sec, err := strconv.ParseInt(opt(app, "@agent_since"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		since := time.Unix(sec, 0)
		clk := control.Clock{Now: func() time.Time { return since.Add(after) }, Sleep: time.Sleep}
		if err := bossclear.Offer(s.tmux, clk, app); err != nil {
			t.Fatal(err)
		}
	}
	noteAt := func(idle string) string {
		return "[dev boss inbox] 1 worker update(s):\n- boss-app may be cleared: idle " + idle + ", no worker running, context 30%: clear it? (dev agent clear boss-app, in the background)"
	}
	note := noteAt("31m")

	round(bossclear.IdleLimit - time.Minute)
	check(t, "too soon", take(), "")
	s.T("set-option", "-p", "-t", app, "@agent_context", "12")
	round(bossclear.IdleLimit + time.Minute)
	check(t, "context under 20%", take(), "")
	s.T("set-option", "-p", "-t", app, "@agent_context", "30")
	s.T("new-session", "-d", "-s", "app-w", "sleep 600")
	s.T("set-option", "-t", "=app-w:", "@dev_started_by", "boss-app")
	w := pane("app-w")
	s.T("set-option", "-p", "-t", w, "@agent_state", "working", ";", "set-option", "-p", "-t", w, "@agent_cmd", "sleep")
	round(bossclear.IdleLimit + time.Minute)
	check(t, "a worker working", take(), "")
	s.T("set-option", "-p", "-t", w, "@agent_state", "idle")
	round(bossclear.IdleLimit + time.Minute + bossclear.CheckEvery/2)
	check(t, "a stretch Check refused isn't read again within a minute", take(), "")
	s.T("set-option", "-u", "-t", "=boss-app:", "@dev_started_by")
	round(bossclear.IdleLimit + time.Minute)
	check(t, "no manager over it", take(), "")
	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "manager")
	s.T("set-option", "-p", "-t", app, "@agent_state", "waiting", ";", "set-option", "-p", "-t", app, "@agent_waiting_for", "permission_prompt")
	round(bossclear.IdleLimit + time.Minute)
	check(t, "on a permission prompt", take(), "")
	s.T("set-option", "-p", "-t", app, "@agent_state", "idle", ";", "set-option", "-pu", "-t", app, "@agent_waiting_for")
	s.T("set-option", "-p", "-t", app, "@dev_handoff", inbox.ClearValue("s0", "/h.md", time.Now().Add(time.Hour)))
	round(bossclear.IdleLimit + time.Minute)
	check(t, "a clear running", take(), "")
	s.T("set-option", "-pu", "-t", app, "@dev_handoff")
	transcript := opt(app, "@agent_transcript")
	s.T("set-option", "-pu", "-t", app, "@agent_transcript")
	round(bossclear.IdleLimit + time.Minute)
	check(t, "no transcript recorded", take(), "")
	s.T("set-option", "-p", "-t", app, "@agent_transcript", transcript)
	if got := opt(app, bossclear.OptOffered); got != "" {
		t.Fatalf("❌ a stretch marked offered without a note: %q", got)
	}

	for range 3 {
		round(bossclear.IdleLimit + 2*time.Minute)
	}
	check(t, "idle past the limit: exactly one note", take(), noteAt("32m"))
	m, _, err := inbox.ManagerOver(s.tmux, "boss-app")
	if err != nil {
		t.Fatal(err)
	}
	// Another note about boss-app resets the inbox's own dedupe: the stretch still isn't offered again
	if _, err := m.Box.Add(inbox.Note{Worker: "boss-app", Kind: inbox.WaitsOnYou, Text: "#3 Ship v2?", Key: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	round(bossclear.IdleLimit + 3*time.Minute)
	check(t, "no second note in the stretch", take(), "[dev boss inbox] 1 worker update(s):\n- boss-app waits on you: #3 Ship v2?")
	t.Log("✅ a test boss idle past the limit gets exactly one note")

	// A turn ends the stretch; the next one is offered again
	first := opt(app, "@agent_since")
	time.Sleep(1100 * time.Millisecond)
	s.T("send-keys", "-t", app, "go on", "Enter")
	waitFor(t, "boss-app's turn over", func() bool { return opt(app, "@agent_since") != first && opt(app, "@agent_state") == "idle" })
	round(bossclear.IdleLimit + time.Minute)
	round(bossclear.IdleLimit + time.Minute)
	check(t, "a new stretch: one note again", take(), note)

	// Claude's idle reminder a minute in moves the stretch's start: a real boss rests there, and is offered once
	second := opt(app, "@agent_since")
	time.Sleep(1100 * time.Millisecond)
	state(app, "waiting", `{"hook_event_name":"Notification","notification_type":"idle_prompt","session_id":"s0","transcript_path":"`+transcript+`"}`)
	waitFor(t, "boss-app at its idle reminder", func() bool { return opt(app, "@agent_since") != second && opt(app, "@agent_state") == "waiting" })
	round(bossclear.IdleLimit + time.Minute)
	round(bossclear.IdleLimit + time.Minute)
	check(t, "resting at Claude's idle reminder: one note", take(), note)
}

// A boss's offer also comes after QuietFor of rest at BigContext or more: not while a worker works or one of its asks is
// up in the manager's dialog; after an offer none for IdleLimit.
func TestClearOfferBossBig(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir)
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	stub := clearStub(t, filepath.Join(tmp, "bin"))
	pane := func(session string) string { return s.T("display", "-t", "="+session+":", "-p", "#{pane_id}") }
	opt := func(p, name string) string { return s.T("display", "-t", p, "-p", "#{"+name+"}") }
	s.T("new-session", "-d", "-s", "manager", "sleep 600")
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := pane("manager")
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		cmd := exec.Command(devBin, "state", map[string]string{"SessionStart": "idle", "UserPromptSubmit": "working"}[ev])
		cmd.Env = s.scriptEnv("TMUX_PANE="+manager, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + ev + `"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ dev state: %v %s", err, out)
		}
	}
	mb, ok, err := inbox.Here(s.tmux, manager)
	if !ok || err != nil {
		t.Fatalf("❌ no manager inbox: %v", err)
	}
	take := func() string {
		t.Helper()
		bt, err := mb.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}
	s.T("new-session", "-d", "-s", "boss-app", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=boss-app:", "@dev_herd", "1")
	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "manager")
	app := pane("boss-app")
	s.T("set-option", "-p", "-t", app, "@dev_boss_pane", "coordinator")
	waitFor(t, "boss-app registered", func() bool { return opt(app, "@agent_session") == "s0" })
	s.T("new-session", "-d", "-s", "app-w", "sleep 600")
	s.T("set-option", "-t", "=app-w:", "@dev_started_by", "boss-app")
	w := pane("app-w")
	sec, err := strconv.ParseInt(opt(app, "@agent_since"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	round := func(context string, after time.Duration) {
		t.Helper()
		s.T("set-option", "-p", "-t", app, "@agent_context", context)
		clk := control.Clock{Now: func() time.Time { return time.Unix(sec, 0).Add(after) }, Sleep: time.Sleep}
		if err := bossclear.Offer(s.tmux, clk, app); err != nil {
			t.Fatal(err)
		}
	}
	quiet := bossclear.QuietFor + time.Minute
	round("30", quiet)
	check(t, "under 40%", take(), "")
	s.T("set-option", "-p", "-t", w, "@agent_state", "working", ";", "set-option", "-p", "-t", w, "@agent_cmd", "sleep")
	round("45", quiet+2*time.Minute)
	check(t, "40% but a worker working", take(), "")
	s.T("set-option", "-p", "-t", w, "@agent_state", "idle")
	if err := mb.Box.SetAsking([]inbox.Asking{{Boss: "boss-app", ID: 3, Text: "Ship v2?", At: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	round("45", quiet+4*time.Minute)
	check(t, "40% but its ask is up in the manager", take(), "")
	if err := mb.Box.SetAsking([]inbox.Asking{{Boss: "boss-other", ID: 1, Text: "Ship v3?", At: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	round("45", quiet+6*time.Minute)
	check(t, "40% and quiet, another boss's ask up: offered", take(),
		"[dev boss inbox] 1 worker update(s):\n- boss-app may be cleared: idle 9m, no worker running, context 45%: clear it? (dev agent clear boss-app, in the background)")
	// Declined or failed: the stretch's mark gone, no new offer for IdleLimit
	s.T("set-option", "-pu", "-t", app, bossclear.OptOffered)
	round("45", quiet+8*time.Minute)
	check(t, "not offered again at once", take(), "")
	t.Log("✅ a boss's clear is offered by size after a quiet moment")
}

// The manager's offer also comes after QuietFor of rest at BigContext or more, not only after IdleLimit.
func TestClearOfferManagerBig(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir)
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	stub := clearStub(t, filepath.Join(tmp, "bin"))
	s.T("new-session", "-d", "-s", "manager", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := s.T("display", "-t", "=manager:", "-p", "#{pane_id}")
	s.T("set-option", "-p", "-t", manager, "@dev_boss_pane", "coordinator")
	waitFor(t, "manager registered", func() bool { return s.T("display", "-t", manager, "-p", "#{@agent_session}") == "s0" })
	round := func(context string, after time.Duration) []todo.Shown {
		t.Helper()
		s.T("set-option", "-p", "-t", manager, "@agent_context", context)
		sec, err := strconv.ParseInt(s.T("display", "-t", manager, "-p", "#{@agent_since}"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		clk := control.Clock{Now: func() time.Time { return time.Unix(sec, 0).Add(after) }, Sleep: time.Sleep}
		if err := bossclear.Offer(s.tmux, clk, manager); err != nil {
			t.Fatal(err)
		}
		st, err := todo.ForPane(s.tmux, manager)
		if err != nil {
			t.Fatal(err)
		}
		l, err := st.Read(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return l.Items
	}
	if items := round("45", bossclear.QuietFor-time.Minute); len(items) != 0 {
		t.Fatalf("❌ a big context, rested too short: %+v", items)
	}
	if items := round("30", bossclear.QuietFor+time.Minute); len(items) != 0 {
		t.Fatalf("❌ a context under %d%%, rested QuietFor: %+v", bossclear.BigContext, items)
	}
	items := round("45", bossclear.QuietFor+time.Minute)
	if len(items) != 1 || items[0].Text != bossclear.OfferQuestion ||
		!strings.Contains(strings.Join(items[0].Context, "\n"), "idle 3m, no worker running, context 45%") {
		t.Fatalf("❌ a big context, quiet: manager's list = %+v", items)
	}
	// "Not now" closes the ask; the next stretch's early offer waits IdleLimit after the last
	st, err := todo.ForPane(s.tmux, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Answer(items[0].ID, bossclear.OfferQuestion, "Not now", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.T("set-option", "-pu", "-t", manager, bossclear.OptOffered)
	if items := round("45", bossclear.QuietFor+time.Minute); len(items) != 1 {
		t.Fatalf("❌ asked again at once after Not now: %+v", items)
	}
	t.Log("✅ the manager's clear is offered by size after a quiet moment")
}

// For the manager itself the offer is an ask on its own list, the user's (NEEDS YOU), once while it stays open.
func TestClearOfferManager(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir)
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	stub := clearStub(t, filepath.Join(tmp, "bin"))
	s.T("new-session", "-d", "-s", "manager", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := s.T("display", "-t", "=manager:", "-p", "#{pane_id}")
	s.T("set-option", "-p", "-t", manager, "@dev_boss_pane", "coordinator")
	waitFor(t, "manager registered", func() bool { return s.T("display", "-t", manager, "-p", "#{@agent_session}") == "s0" })
	s.T("set-option", "-p", "-t", manager, "@agent_context", "45")
	clk := control.Clock{Now: func() time.Time { return time.Now().Add(bossclear.IdleLimit + time.Minute) }, Sleep: time.Sleep}
	asks := func() []todo.Shown {
		t.Helper()
		st, err := todo.ForPane(s.tmux, manager)
		if err != nil {
			t.Fatal(err)
		}
		l, err := st.Read(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return l.Items
	}
	for range 2 {
		if err := bossclear.Offer(s.tmux, clk, manager); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "the manager's stretch marked offered", s.T("display", "-t", manager, "-p", "#{"+bossclear.OptOffered+"}"),
		s.T("display", "-t", manager, "-p", "#{@agent_since}"))
	// A new stretch with the ask still open adds none
	s.T("set-option", "-pu", "-t", manager, bossclear.OptOffered)
	if err := bossclear.Offer(s.tmux, clk, manager); err != nil {
		t.Fatal(err)
	}
	items := asks()
	if len(items) != 1 || items[0].Text != bossclear.OfferQuestion || items[0].On != "" || len(items[0].Options) != 2 ||
		!strings.Contains(strings.Join(items[0].Context, "\n"), "idle 31m, no worker running, context 45%") {
		t.Fatalf("❌ manager's list = %+v", items)
	}
	// The board's read: the user's, needing them
	out, e, st := runDev(s.scriptEnv("XDG_STATE_HOME="+stateDir, "TMUX_PANE="+manager), "", "todo", "--json")
	if st != "0" || !strings.Contains(out, `"needs_you": true`) || !strings.Contains(out, `"kind": "you"`) {
		t.Fatalf("❌ dev todo --json: exit %s %s: %s", st, e, out)
	}
	t.Log("✅ the manager's own offer: one ask for the user")

	// The user runs it from a plain shell
	writeFile(t, filepath.Join(tmp, "save.md"), "## Key facts\n1. The user reviews on Friday. confirmed: said\n\n## Open asks\n#1: Clear it → the user runs the clear; Not now → nothing.\n\n"+
		"## Work in flight\nNone.\n\n## The user's standing words\nNone.\n\n## Roles\nThe user answers my prompts and dialogs; I relay the bosses' asks.\n\n"+
		"## Open risks\nNone.\n\n## Old transcript\n"+filepath.Join(tmp, "s0.jsonl")+"\n")
	writeFile(t, filepath.Join(tmp, "proof.md"), "## Key facts\n1. Friday review. confirmed: said\n\n## Open asks\n#1: Clear it → nothing for me.\n\n## Roles\nThe user answers my prompts.\n")
	shell := slices.DeleteFunc(s.scriptEnv("XDG_STATE_HOME="+stateDir), func(v string) bool { return strings.HasPrefix(v, "CLAUDECODE=") })
	out, e, st = runDev(shell, "", "agent", "clear", "manager")
	if st != "0" || !strings.Contains(out, "Cleared manager: 1 key facts restated") {
		t.Fatalf("❌ dev agent clear manager from a shell: exit %s: %s\n%s", st, e, out)
	}
	if items := asks(); len(items) != 1 || !items[0].Done || items[0].Answer != "Clear it" {
		t.Errorf("❌ the offer after the clear = %+v, want answered Clear it", items)
	}
	t.Log("✅ the user clears the manager from a shell; its offer is answered")
}
