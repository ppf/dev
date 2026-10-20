//go:build integration

package integration

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
)

// screenStub is an agent drawing Claude's screens: "/effort …" opens the cache dialog /effort opens
// mid-conversation and takes one key, as Claude runs it, with no hook; "long…" works until a key (Esc)
// ends the turn, drawing it interrupted with no hook either; any other line is a turn. Like Claude, it draws
// a prompt before its hook runs.
func screenStub(t *testing.T, dir string) string {
	t.Helper()
	dev := shellQuote(devBin)
	return filepath.Join(writeStubs(t, dir, `hook() { printf '%s' "$2" | `+dev+` state "$1"; }
rule=$(printf '─%.0s' $(seq 40))
box() { printf '\033[2J\033[H%s\n\n%s\n❯ \n%s\n  ? for shortcuts\n' "$1" "$rule" "$rule"; }
key() { stty -icanon -echo; dd bs=1 count=1 2>/dev/null; stty icanon echo; }
hook idle '{"hook_event_name":"SessionStart"}'
box "⏺ Ready."
while IFS= read -r line; do
	case "$line" in
	/effort*)
		printf '\033[2J\033[H%s\n   Change effort level?\n   This conversation is cached for the current effort level.\n   ❯ 1. Yes, switch to high\n     2. No, go back\n' "$(printf '▔%.0s' $(seq 40))"
		box "  ⎿  Set effort level to high (key $(key))"
		continue;;
	long*)
		printf '\033[2J\033[H✻ Writing… (esc to interrupt)\n\n%s\n❯ \n%s\n' "$rule" "$rule"
		hook working '{"hook_event_name":"UserPromptSubmit"}'
		key >/dev/null
		box "  ⎿  Interrupted · What should Claude do instead?"
		continue;;
	esac
	box "got: $line"
	hook working '{"hook_event_name":"UserPromptSubmit"}'
	hook idle '{"hook_event_name":"Stop"}'
done`, "agent"), "agent")
}

// Slash commands Claude runs itself fire no hook: prompt --wait confirms /effort's dialog once it shows
// and returns when it ran. Esc ending a turn fires none either: send-keys esc records it idle, and a
// prompt after an Esc pressed by hand goes through.
func TestScreenOnlyChanges(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	d.T("new-session", "-d", "-s", "w1", screenStub(t, t.TempDir()))
	w1 := d.T("display", "-t", "=w1:", "-p", "#{pane_id}")
	env := d.scriptEnv()
	opt := func(name string) string { return d.T("show-option", "-pqv", "-t", w1, name) }
	shows := func(text string) func() bool {
		return func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", w1), text) }
	}
	waitFor(t, "stub registered", func() bool { return opt("@agent_state") == "idle" })

	out, errOut, status := devAgent(env, "", "prompt", "w1", "--wait", "/effort high")
	check(t, "/effort --wait", status+errOut+" "+out, "0 idle\n")
	check(t, "dialog confirmed with 1, none left over", strconv.FormatBool(shows("(key 1)")()), "true")
	check(t, "no prompt counted", opt("@agent_prompts"), "")

	start := func() {
		t.Helper()
		_, errOut, status := devAgent(env, "", "prompt", "w1", "long story")
		check(t, "long prompt", status+errOut, "0")
		waitFor(t, "w1 working", func() bool { return opt("@agent_state") == "working" && shows("Writing")() })
	}
	start()
	_, errOut, status = devAgent(env, "", "send-keys", "w1", "esc")
	check(t, "send-keys esc", status+errOut, "0")
	check(t, "recorded idle", opt("@agent_state"), "idle")

	start()
	d.T("send-keys", "-t", w1, "Escape")
	waitFor(t, "w1 interrupted by hand", shows("Interrupted"))
	check(t, "no hook: still working", opt("@agent_state"), "working")
	// Older than a prompt Claude may not have drawn yet
	at, _ := strconv.ParseInt(opt("@agent_prompt_at"), 10, 64)
	d.T("set-option", "-p", "-t", w1, "@agent_prompt_at", strconv.FormatInt(at-3, 10))
	out, errOut, status = devAgent(env, "", "prompt", "w1", "--wait", "--timeout", "10s", "carry on")
	check(t, "prompt --wait after Esc", status+errOut+" "+out, "0 idle\n")
	check(t, "w1 got it", strconv.FormatBool(shows("got: carry on")()), "true")
}

// agent.Interrupted writes only to a pane still in the working stretch it read: a new prompt (counted in
// @agent_prompts, @agent_since unmoved) or another state wins.
func TestInterruptedGuard(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	pane := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	set := func(state, since, prompts string) {
		s.T("set-option", "-p", "-t", pane, "@agent_state", state)
		s.T("set-option", "-p", "-t", pane, "@agent_since", since)
		s.T("set-option", "-p", "-t", pane, "@agent_prompts", prompts)
	}
	read := func() string {
		return s.T("display", "-p", "-t", pane, "#{@agent_state} #{@agent_since} #{@agent_prompts}")
	}
	for _, tt := range []struct{ name, state, since, prompts, want string }{
		{"a prompt since", "working", "50", "4", "working 50 4"},
		{"a new stretch", "working", "60", "3", "working 60 3"},
		{"waiting since", "waiting", "50", "3", "waiting 50 3"},
	} {
		set(tt.state, tt.since, tt.prompts)
		if err := agent.Interrupted(s.tmux, pane, "50", "3"); err != nil {
			t.Fatal(err)
		}
		check(t, tt.name+" left alone", read(), tt.want)
	}
	set("working", "50", "3")
	if err := agent.Interrupted(s.tmux, pane, "50", "3"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(read()); len(got) != 3 || got[0] != "idle" || got[1] == "50" {
		t.Errorf("❌ same stretch: %q, want idle with a new since", got)
	}
}
