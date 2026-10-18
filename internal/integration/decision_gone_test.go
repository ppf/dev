//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// declineStub is an agent whose "ask" prompt waits on a permission dialog the way Claude reports one (dev
// question, then dev state waiting), and that answers it the way Claude takes a No or Esc: its dialog
// replaced by its own input box, with no hook at all.
func declineStub(t *testing.T, dir string) string {
	t.Helper()
	dev := shellQuote(devBin)
	return filepath.Join(writeStubs(t, dir, `hook() { printf '%s' "$2" | `+dev+` state "$1"; }
rule=$(printf '─%.0s' $(seq 40))
hook idle '{"hook_event_name":"SessionStart"}'
while IFS= read -r line; do
	echo "got: $line"
	hook working '{"hook_event_name":"UserPromptSubmit"}'
	case "$line" in ask*)
		printf '%s' '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}' | `+dev+` question
		hook waiting '{"hook_event_name":"Notification","notification_type":"permission_prompt"}'
		printf ' Bash command\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n'
		stty -icanon -echo
		dd bs=1 count=1 2>/dev/null >/dev/null
		stty icanon echo
		printf '\033[2J\033[H  ⎿  Interrupted · What should Claude do instead?\n\n%s\n❯ \n%s\n  ? for shortcuts\n' "$rule" "$rule"
		continue
	esac
	hook idle '{"hook_event_name":"Stop"}'
done`, "agent"), "agent")
}

// A dialog answered No or Esc fires no hook, so its worker stays recorded as waiting on a decision. A
// prompt (dev agent prompt, the boss reply box) is still refused while the dialog is open, and goes
// through once Claude's own input box is back, settling the state to idle and clearing the question. The
// boss's inbox doesn't call it unanswered, and its previews settle it too.
func TestDecisionGone(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	d.T("new-session", "-d", "-s", "w1", declineStub(t, t.TempDir()))
	w1 := d.T("display", "-t", "=w1:", "-p", "#{pane_id}")
	env := d.scriptEnv()
	opt := func(name string) string { return d.T("show-option", "-pqv", "-t", w1, name) }
	waitFor(t, "stub registered", func() bool { return opt("@agent_state") == "idle" })
	d.T("new-session", "-d", "-s", "herd", devBin+" boss --previews")
	d.T("set-option", "-t", "=herd:", "@dev_herd", "1")
	d.T("set-option", "-t", "=w1:", "@dev_started_by", d.T("display", "-t", "=herd:", "-p", "#{session_id}"))
	previews := d.T("display", "-t", "=herd:", "-p", "#{pane_id}")
	// age moves the waiting state's start back by ago
	age := func(ago time.Duration) {
		since, _ := strconv.ParseInt(opt("@agent_since"), 10, 64)
		d.T("set-option", "-p", "-t", w1, "@agent_since", strconv.FormatInt(since-int64(ago.Seconds()), 10))
	}
	// ask opens the dialog, recorded a few seconds after it's drawn as Claude does; answer, when set, takes it
	// the way No does
	ask := func(answer bool) {
		t.Helper()
		_, errOut, status := devAgent(env, "", "prompt", "w1", "ask")
		check(t, "ask", status+errOut, "0")
		waitFor(t, "w1 waiting with its question", func() bool {
			return opt("@agent_state") == "waiting" && opt("@agent_question") != "" && strings.Contains(d.T("capture-pane", "-p", "-t", w1), "2. No")
		})
		age(3 * time.Second)
		if answer {
			d.T("send-keys", "-t", w1, "2")
			waitFor(t, "w1's input box back", func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", w1), "? for shortcuts") })
		}
	}
	// held makes the waiting state 10 minutes old: stuck, for the inbox
	held := func() { age(10 * time.Minute) }
	notes := func() string {
		files, _ := filepath.Glob(filepath.Join(d.tmp, "state", "dev", "inbox", "*.json"))
		var all string
		for _, f := range files {
			data, _ := os.ReadFile(f)
			all += string(data)
		}
		return all
	}

	ask(false)
	_, errOut, status := devAgent(env, "", "prompt", "w1", "hi")
	check(t, "open dialog refuses a prompt", status+" "+strconv.FormatBool(strings.Contains(errOut, "waiting for a decision")), "1 true")
	d.T("send-keys", "-t", w1, "2")
	waitFor(t, "w1's input box back", func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", w1), "? for shortcuts") })
	check(t, "no hook: still waiting", opt("@agent_state"), "waiting")
	_, errOut, status = devAgent(env, "", "prompt", "w1", "do X instead")
	check(t, "prompt after No", status+errOut, "0")
	waitFor(t, "w1 got the prompt", func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", w1), "got: do X instead") })
	check(t, "question cleared", opt("@agent_question"), "")
	waitFor(t, "w1 idle after its turn", func() bool { return opt("@agent_state") == "idle" })

	// The previews aren't viewed, so only the inbox watch reads w1's screen
	ask(true)
	held()
	waitFor(t, "inbox watch settles w1", func() bool { return opt("@agent_state") == "idle" })
	check(t, "no stuck note", strconv.FormatBool(strings.Contains(notes(), `"kind":"stuck"`)), "false")

	d.T("switch-client", "-t", "=herd:")
	ask(true)
	waitFor(t, "previews settle w1", func() bool {
		return opt("@agent_state") == "idle" && strings.Contains(d.T("capture-pane", "-p", "-t", previews), "w1 Claude · idle")
	})
	check(t, "question cleared", opt("@agent_question"), "")

	// A dialog still open stays a decision, and is stuck when held
	ask(false)
	held()
	waitFor(t, "stuck note for the open dialog", func() bool { return strings.Contains(notes(), "waiting on a decision for 10m, unanswered") })
	check(t, "open dialog still waiting", opt("@agent_state"), "waiting")
}

// agent.DialogGone writes only to a pane still in the waiting stretch it read: a hook that moved the pane on
// (another state, or a new @agent_since) wins.
func TestDialogGoneGuard(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	pane := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	set := func(state, since string) {
		s.T("set-option", "-p", "-t", pane, "@agent_state", state)
		s.T("set-option", "-p", "-t", pane, "@agent_since", since)
		s.T("set-option", "-p", "-t", pane, "@agent_question", "q")
	}
	read := func() string {
		return s.T("display", "-p", "-t", pane, "#{@agent_state} #{@agent_since} #{@agent_question}")
	}
	for _, tt := range []struct{ name, state, since, want string }{
		{"new waiting stretch", "waiting", "60", "waiting 60 q"},
		{"working since", "working", "50", "working 50 q"},
	} {
		set(tt.state, tt.since)
		if err := agent.DialogGone(s.tmux, pane, agent.Idle, "50"); err != nil {
			t.Fatal(err)
		}
		check(t, tt.name+" left alone", read(), tt.want)
	}
	set("waiting", "50")
	if err := agent.DialogGone(s.tmux, pane, agent.Idle, "50"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(read()); len(got) != 2 || got[0] != "idle" || got[1] == "50" {
		t.Errorf("❌ same stretch: %q, want idle with a new since and no question", got)
	}
}
