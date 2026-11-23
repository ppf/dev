//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
)

// A boss started by a boss sends it only what waits on the user and what went wrong: its list items for the user,
// its questions and decisions, its failures, and messages addressed to it; not its finished turns nor its
// workers' notes. A rate limit resumes it like a worker, its own inbox held until then.
func TestBossUnderBoss(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	dev := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev %s: %v, stderr %q", strings.Join(argv, " "), err, stderr.String())
		}
		return string(out)
	}
	state := func(pane, st, stdin string) { t.Helper(); dev(pane, stdin, "state", st) }
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	appLog := filepath.Join(tmp, "app.log")
	readLog := func() string { data, _ := os.ReadFile(appLog); return string(data) }

	// A busy top boss: its notes wait in its inbox
	s.T("new-session", "-d", "-s", "boss-hq", "sleep 600")
	s.T("set-option", "-t", "=boss-hq:", "@dev_herd", "1")
	hq := s.T("display", "-t", "=boss-hq:", "-p", "#{pane_id}")
	state(hq, "idle", `{"hook_event_name":"SessionStart"}`)
	state(hq, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	s.T("new-session", "-d", "-s", "boss-app", "bash", script, "", appLog)
	s.T("set-option", "-t", "=boss-app:", "@dev_herd", "1")
	s.T("set-option", "-t", "=boss-app:", "@dev_started_by", "boss-hq")
	app := s.T("display", "-t", "=boss-app:", "-p", "#{pane_id}")
	state(app, "idle", `{"hook_event_name":"SessionStart"}`)
	s.T("new-session", "-d", "-s", "app-w", "sleep 600")
	s.T("set-option", "-t", "=app-w:", "@dev_started_by", "boss-app")
	w := s.T("display", "-t", "=app-w:", "-p", "#{pane_id}")
	state(w, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	waitFor(t, "app's box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", app), "Try") })
	b, ok, err := inbox.Here(s.tmux, hq)
	if !ok || err != nil {
		t.Fatalf("❌ no hq inbox: %v", err)
	}
	take := func() string {
		t.Helper()
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}

	// Rate limited till a reset to come: its worker's note waits for the resume, which tells hq nothing
	state(app, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	reset := time.Now().Add(10 * time.Minute)
	state(app, "idle", `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets `+reset.Format("3:04pm")+`"}`)
	state(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Tests pass."}`)
	app2, ok, err := inbox.Here(s.tmux, app)
	if pending, perr := app2.Box.Pending(); !ok || err != nil || perr != nil || !pending {
		t.Fatalf("❌ app's inbox held: pending %v, %v %v", pending, err, perr)
	}
	time.Sleep(time.Second)
	check(t, "nothing pasted into app", readLog(), "")
	s.T("set-option", "-p", "-t", app, inbox.OptResumeAt, strconv.FormatInt(time.Now().Unix()-1, 10)) // its reset came
	if err := inbox.Watch(s.tmux, control.RealClock, hq); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "app resumed", func() bool { return strings.Contains(readLog(), inbox.ResumePrompt) })
	check(t, "only the resume prompt", readLog(), inbox.ResumePrompt+"\n")
	at := reset.Truncate(time.Minute).Add(time.Minute).Format("15:04")
	check(t, "hq hears of the failure, not the resume", take(), "[dev boss inbox] 1 worker update(s):\n- boss-app failed: rate_limit: You've hit your session limit · resets "+
		reset.Format("3:04pm")+"; dev resumes it at "+at)
	out := dev(app, `{"hook_event_name":"UserPromptSubmit","prompt":`+strconv.Quote(inbox.ResumePrompt)+`}`, "state", "working")
	if !strings.Contains(out, "app-w finished: Tests pass.") {
		t.Errorf("❌ the resume prompt's hook = %q, want app's held note", out)
	}

	// Turns that end, done or on a question (twice), and its own dialog
	dev(app, `{"hook_event_name":"Stop","last_assistant_message":"Done."}`, "state", "idle")
	state(app, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	for range 2 {
		state(app, "working", `{"hook_event_name":"UserPromptSubmit"}`)
		state(app, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Merge it?\n- yes\n- no"}`)
	}
	// A question without a "?": an instruction to reply naming its options
	state(app, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	state(app, "idle", `{"hook_event_name":"Stop","last_assistant_message":"The VPN is down.\n\nReply **Done** once it's connected, or **Skip** to leave it."}`)
	state(app, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	state(app, "waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
	// Its list: items for the user, and one on its worker
	check(t, "add", dev(app, "", "todo", "add", "Pick a name", "--details", "why"), "added 1 · to do 1\n")
	check(t, "add on a worker", dev(app, "", "todo", "add", "Fix CI", "--on", "app-w"), "added 2 · to do 2, 1 on others\n")
	check(t, "ask", dev(app, "", "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait", "--details", "why"), "asked 3 · to do 3, 1 on others\n")
	check(t, "given back", dev(app, "", "todo", "add", "--replaces", "2", "--on", "me", "--details", "why"), "replaced 2 · to do 3\n")
	check(t, "a top boss's item", dev(hq, "", "todo", "add", "Book a room", "--details", "why"), "added 1 · to do 1\n")
	dev(app, "", "agent", "message", "boss-hq", "Which API version?")
	got := take()
	for _, want := range []string{
		"- boss-app asks: Merge it? - yes - no",
		"- boss-app asks: Reply **Done** once it's connected, or **Skip** to leave it.",
		"- boss-app needs a decision: Claude needs your permission to use Bash",
		"- boss-app waits on you: #1 Pick a name",
		"- boss-app waits on you: #3 Merge #148? (options: Merge (Recommended) / Wait)",
		"- boss-app waits on you: #2 Fix CI",
		"from boss-app",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("❌ hq's batch lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "asks: Merge it?"); n != 2 {
		t.Errorf("❌ %d asks, want 2 (a prompt between):\n%s", n, got)
	}
	if n := strings.Count(got, "Fix CI"); n != 1 {
		t.Errorf("❌ %d Fix CI notes, want 1 (given back only):\n%s", n, got)
	}
	for _, not := range []string{"Done.", "Tests pass", "resumed", "Book a room"} {
		if strings.Contains(got, not) {
			t.Errorf("❌ hq's batch has %q:\n%s", not, got)
		}
	}
}
