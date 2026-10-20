package control

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// fakeClock advances only when slept on.
func fakeClock() Clock {
	now := time.Unix(0, 0)
	return Clock{Now: func() time.Time { return now }, Sleep: func(d time.Duration) { now = now.Add(d) }}
}

func paneRow(session, pane, command, state, stateCmd, project, sidebar string) string {
	return tmux.FormatRow(session, "1", pane, "/nonexistent/"+session, "title", command, state, stateCmd, "", project, sidebar, "$"+session, "", "", "")
}

func TestList(t *testing.T) {
	rows := paneRow("app", "%1", "2.1.3", "idle", "2.1.3", "app", "%0") +
		paneRow("app", "%0", "dev", "", "", "app", "%0") +
		paneRow("shell", "%5", "zsh", "", "", "", "") +
		paneRow("cx", "%7", "codex", "", "", "", "")
	c := tmux.NewFake(func(args []string) (string, error) { return rows, nil })
	got, err := List(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{Session: "app", Pane: "%1", Label: "Claude", State: "idle", Project: "app", Path: "/nonexistent/app"},
		{Session: "cx", Pane: "%7", Label: "Codex", Path: "/nonexistent/cx"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}

// A working Claude whose input box says it holds a queued prompt is listed queued.
func TestListQueued(t *testing.T) {
	queued := "✻ Nucleating… (5s)\n" + rule + "\n❯ Press up to edit queued messages\n" + rule + "\n"
	rows := paneRow("busy", "%1", "2.1.3", "working", "2.1.3", "", "") + paneRow("free", "%2", "2.1.3", "working", "2.1.3", "", "") +
		paneRow("idle", "%3", "2.1.3", "idle", "2.1.3", "", "")
	var captured []string
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			captured = append(captured, args[len(args)-1])
			if args[len(args)-1] == "%1" {
				return queued, nil
			}
			return workingScreen, nil
		}
		return rows, nil
	})
	got, err := List(c)
	if err != nil || len(got) != 3 || !got[0].Queued || got[1].Queued || got[2].Queued {
		t.Errorf("List = %+v, %v; want only busy queued", got, err)
	}
	if !slices.Equal(captured, []string{"%1", "%2"}) {
		t.Errorf("captured %q, want only the working panes", captured)
	}
	a, err := Resolve(c, "busy")
	if err != nil || a.Queued {
		t.Errorf("Resolve(busy) = %+v, %v; want no capture", a, err)
	}
	if err := a.ReadQueued(c); err != nil || !a.Queued {
		t.Errorf("ReadQueued(busy) = %v, queued %v; want queued", err, a.Queued)
	}
	// A pane closed since list-panes isn't queued, and doesn't fail the list
	gone := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			return "", &tmux.Error{Err: errors.New("exit status 1"), Stderr: "can't find pane: %1"}
		}
		return rows, nil
	})
	if got, err := List(gone); err != nil || len(got) != 3 || got[0].Queued {
		t.Errorf("List(pane gone) = %+v, %v", got, err)
	}
}

func TestListEmptyIsNotNil(t *testing.T) {
	c := tmux.NewFake(func(args []string) (string, error) { return paneRow("shell", "%5", "zsh", "", "", "", ""), nil })
	got, err := List(c)
	if err != nil || got == nil {
		t.Errorf("List = %#v, %v; want empty non-nil slice", got, err)
	}
}

func TestResolve(t *testing.T) {
	rows := paneRow("app", "%1", "2.1.3", "idle", "2.1.3", "", "") + paneRow("shell", "%5", "zsh", "", "", "", "")
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "has-session" {
			if args[2] == "=shell:" {
				return "", nil
			}
			return "", &tmux.Error{Err: errors.New("exit status 1")}
		}
		return rows, nil
	})
	for _, tt := range []struct{ target, pane, err string }{
		{"app", "%1", ""},
		{"%1", "%1", ""},
		{"ap", "", `no session "ap"`},
		{"shell", "", `session "shell" has no agent pane`},
		{"%5", "", "pane %5 is not an agent pane"},
	} {
		a, err := Resolve(c, tt.target)
		if a.Pane != tt.pane || (err == nil) != (tt.err == "") || err != nil && err.Error() != tt.err {
			t.Errorf("Resolve(%q) = %q, %v; want %q, %q", tt.target, a.Pane, err, tt.pane, tt.err)
		}
	}
}

func TestRead(t *testing.T) {
	var got []string
	c := tmux.NewFake(func(args []string) (string, error) {
		got = args
		return "one\ntwo  \n\nthree\n\n   \n\n", nil
	})
	out, err := Read(c, "%1", 2)
	if err != nil || out != "\nthree\n" {
		t.Errorf("Read = %q, %v; want %q", out, err, "\nthree\n")
	}
	if want := []string{"capture-pane", "-p", "-J", "-S", "-2", "-t", "%1"}; !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
	if _, err := Read(c, "%1", 0); err == nil {
		t.Error("Read with 0 lines: want error")
	}
}

// promptRig fakes a pane whose status follows statuses, one per display-message (the last repeats), and
// whose screen is screen, or follows screens one per capture-pane (the last repeats); it records the other
// commands and the pasted text.
type promptRig struct {
	statuses []Status
	screen   string
	screens  []string
	cmds     []string
	pasted   string
	pasteErr error
}

func (r *promptRig) client() *tmux.Client {
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "display-message":
			st := r.statuses[0]
			if len(r.statuses) > 1 {
				r.statuses = r.statuses[1:]
			}
			return tmux.FormatRow(st.Command, st.State, st.StateCmd, st.Prompts, st.WaitingFor, st.Since, st.Session, st.Active), nil
		case "capture-pane":
			if len(r.screens) == 0 {
				return r.screen, nil
			}
			screen := r.screens[0]
			if len(r.screens) > 1 {
				r.screens = r.screens[1:]
			}
			return screen, nil
		case "load-buffer":
			b, err := os.ReadFile(args[3])
			r.pasted = string(b)
			return "", err
		}
		r.cmds = append(r.cmds, strings.Join(args, " "))
		if args[0] == "paste-buffer" && r.pasteErr != nil {
			return "", r.pasteErr
		}
		return "", nil
	})
}

func status(state, prompts string) Status {
	return Status{Command: "claude", State: state, StateCmd: "claude", Prompts: prompts}
}

// waitingFor is a waiting status recorded for a Notification of type why.
func waitingFor(why, prompts string) Status {
	st := status("waiting", prompts)
	st.WaitingFor = why
	return st
}

func TestPromptWait(t *testing.T) {
	codex := Status{Command: "codex"}
	tests := []struct {
		name     string
		statuses []Status
		want     string
		err      error
		errText  string
		sent     bool
	}{
		{name: "turn ends idle", statuses: []Status{status("idle", "4"), status("idle", "4"), status("working", "5"), status("idle", "5")}, want: "idle", sent: true},
		{name: "turn shorter than a poll", statuses: []Status{status("idle", ""), status("idle", "1")}, want: "idle", sent: true},
		{name: "idle reminder to a permission question", statuses: []Status{waitingFor("idle_prompt", "2"), status("working", "3"), waitingFor("permission_prompt", "3")}, want: "waiting", sent: true},
		{name: "permission dialog refused", statuses: []Status{waitingFor("permission_prompt", "2")}, errText: "agent is waiting for a decision (permission_prompt): read it and answer with send-keys"},
		{name: "waiting for an unknown reason refused", statuses: []Status{status("waiting", "2")}, errText: "agent is waiting for a decision (unknown): read it and answer with send-keys"},
		{name: "agent exits mid-turn", statuses: []Status{status("idle", "2"), status("working", "3"), {Command: "zsh", State: "working", StateCmd: "claude", Prompts: "3"}}, err: errGone, sent: true},
		{name: "never picked up", statuses: []Status{status("idle", "2")}, err: ErrStalled, sent: true},
		{name: "idle notification is not a pickup", statuses: []Status{status("idle", "2"), waitingFor("idle_prompt", "2")}, err: ErrStalled, sent: true},
		{name: "never settles", statuses: []Status{status("idle", "2"), status("working", "3")}, err: ErrTimeout, sent: true},
		{name: "busy agent refused", statuses: []Status{status("working", "2")}, errText: "agent is working: wait for it first"},
		{name: "codex refused", statuses: []Status{codex}, err: errNoState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &promptRig{statuses: tt.statuses}
			st, err := PromptWait(r.client(), fakeClock(), "%1", "line one\nline two;", 5*time.Second, time.Minute)
			switch {
			case tt.err != nil && !errors.Is(err, tt.err), tt.errText != "" && (err == nil || err.Error() != tt.errText):
				t.Errorf("err = %v, want %v%s", err, tt.err, tt.errText)
			case tt.err == nil && tt.errText == "" && (err != nil || st.Trusted() != tt.want):
				t.Errorf("PromptWait = %q, %v; want %q", st.Trusted(), err, tt.want)
			}
			if sent := r.pasted != ""; sent != tt.sent {
				t.Fatalf("sent = %v, want %v", sent, tt.sent)
			}
			if !tt.sent {
				return
			}
			if r.pasted != "line one\nline two;" {
				t.Errorf("pasted %q", r.pasted)
			}
			buffer := "dev-agent-" + strconv.Itoa(os.Getpid())
			if want := []string{"paste-buffer -p -d -b " + buffer + " -t %1", "send-keys -t %1 Enter"}; !slices.Equal(r.cmds, want) {
				t.Errorf("cmds = %q, want %q", r.cmds, want)
			}
		})
	}
}

// TestPromptWaitClear: /clear starts no turn (no prompt counted); the new session's SessionStart confirms it.
func TestPromptWaitClear(t *testing.T) {
	in := func(session string, st Status) Status {
		st.Session = session
		return st
	}
	cleared := Status{Command: "claude", Prompts: "4"}
	tests := []struct {
		name     string
		statuses []Status
		err      error
		errText  string
		sent     bool
	}{
		{name: "new session starts idle", statuses: []Status{in("a", status("idle", "4")), in("a", status("idle", "4")), cleared, in("b", status("idle", "4"))}, sent: true},
		{name: "idle-waiting before", statuses: []Status{in("a", waitingFor("idle_prompt", "4")), in("b", status("idle", "4"))}, sent: true},
		{name: "session never changes", statuses: []Status{in("a", status("idle", "4"))}, err: ErrStalled, sent: true},
		{name: "a counted prompt isn't the reset", statuses: []Status{in("a", status("idle", "4")), in("a", status("idle", "5"))}, err: ErrStalled, sent: true},
		{name: "no session recorded: refused", statuses: []Status{status("idle", "4")}, errText: "--wait confirms /clear by the new session its hook records, and this agent's hook records none (Codex, Grok): send it without --wait"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &promptRig{statuses: tt.statuses}
			st, err := PromptWait(r.client(), fakeClock(), "%1", "/clear\n", 5*time.Second, time.Minute)
			switch {
			case tt.err != nil && !errors.Is(err, tt.err), tt.errText != "" && (err == nil || err.Error() != tt.errText):
				t.Errorf("err = %v, want %v%s", err, tt.err, tt.errText)
			case tt.err == nil && tt.errText == "" && (err != nil || st.Session != "b" || st.Trusted() != "idle"):
				t.Errorf("PromptWait = %+v, %v; want session b idle", st, err)
			}
			if sent := r.pasted != ""; sent != tt.sent {
				t.Errorf("sent = %v, want %v", sent, tt.sent)
			}
		})
	}
}

func TestPrompt(t *testing.T) {
	// A busy agent queues the prompt, unless its screen shows a dialog its hooks haven't reported yet
	r := &promptRig{statuses: []Status{status("working", "1")}, screen: workingScreen}
	if queued, err := Prompt(r.client(), fakeClock(), "%1", "next"); err != nil || !queued || r.pasted != "next" {
		t.Errorf("Prompt(busy) = %v, %v, pasted %q; want queued", queued, err, r.pasted)
	}
	r = &promptRig{statuses: []Status{status("idle", "1")}}
	if queued, err := Prompt(r.client(), fakeClock(), "%1", "next"); err != nil || queued {
		t.Errorf("Prompt(idle) = %v, %v; want sent, not queued", queued, err)
	}
	r = &promptRig{statuses: []Status{status("working", "1")}, screen: dialogScreen}
	if _, err := Prompt(r.client(), fakeClock(), "%1", "next"); err == nil || r.pasted != "" {
		t.Errorf("Prompt(busy, dialog drawn) = %v, pasted %q; want error, nothing sent", err, r.pasted)
	}
	r = &promptRig{statuses: []Status{waitingFor("elicitation_dialog", "1")}}
	if _, err := Prompt(r.client(), fakeClock(), "%1", "2"); err == nil || r.pasted != "" {
		t.Errorf("Prompt(dialog) = %v, pasted %q; want error, nothing sent", err, r.pasted)
	}
	// A failed paste deletes the buffer and never presses Enter
	r = &promptRig{statuses: []Status{status("idle", "1")}, pasteErr: errors.New("no pane")}
	_, err := Prompt(r.client(), fakeClock(), "%1", "x")
	buffer := "dev-agent-" + strconv.Itoa(os.Getpid())
	if want := []string{"paste-buffer -p -d -b " + buffer + " -t %1", "delete-buffer -b " + buffer}; err == nil || !slices.Equal(r.cmds, want) {
		t.Errorf("Prompt(paste fails) = %v, cmds %q; want error, %q", err, r.cmds, want)
	}
}

// Claude's screens with its input box back at the bottom (idle, working) and with a dialog in its place.
var (
	rule          = strings.Repeat("─", 40)
	idleScreen    = "  ⎿  Interrupted · What should Claude do instead?\n\n" + rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on · ? for shortcuts\n\n"
	workingScreen = "⏺ Next step\n\n" + rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on · esc to interrupt\n"
	dialogScreen  = rule + "\n Bash command\n\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n"
	cacheScreen   = strings.Repeat("▔", 40) + "\n   Change effort level?\n   This conversation is cached for the current\n   effort level.\n   ❯ 1. Yes, switch to high\n     2. No, go back\n"
	commandScreen = "⏺ Done.\n\n" + rule + "\n❯ /effort high\n" + rule + "\n  ⏸ manual mode on · ? for shortcuts\n"
	doneScreen    = "  ⎿  Set effort level to high\n\n" + rule + "\n❯ \n" + rule + "\n  ⏸ manual mode on · ? for shortcuts\n"
)

// TestPromptCommand: Claude fires no hook for a slash command it runs itself, and /effort and /model open
// a dialog mid-conversation, which the prompt confirms once it shows.
func TestPromptCommand(t *testing.T) {
	press := "send-keys -t %1 -l 1"
	pressed := func(r *promptRig) int { return strings.Count(strings.Join(r.cmds, "\n"), press) }
	tests := []struct {
		name    string
		screens []string
		errText string
		presses int
	}{
		{"cache dialog confirmed once", []string{commandScreen, cacheScreen, cacheScreen, doneScreen}, "", 1},
		{"no dialog", []string{doneScreen}, "", 0},
		{"a turn starts (a skill)", []string{workingScreen}, "", 0},
		{"another dialog", []string{dialogScreen}, "/effort opened a dialog: read it and answer with send-keys", 0},
		{"left in the input box", []string{commandScreen}, "/effort not seen running within 3s: read the pane", 0},
		{"left in the input box, wrapped", []string{strings.Replace(commandScreen, "❯ /effort high", "❯ /effort\n  high", 1)}, "/effort not seen running within 3s: read the pane", 0},
		{"dialog still open after 1", []string{cacheScreen}, "/effort not seen running within 3s: read the pane", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &promptRig{statuses: []Status{status("idle", "1")}, screens: tt.screens}
			_, err := Prompt(r.client(), fakeClock(), "%1", "/effort high")
			if tt.errText == "" && err != nil || tt.errText != "" && (err == nil || err.Error() != tt.errText) {
				t.Errorf("Prompt = %v, want %q", err, tt.errText)
			}
			if got := pressed(r); got != tt.presses {
				t.Errorf("pressed 1 %d times, want %d; cmds %q", got, tt.presses, r.cmds)
			}
		})
	}
	// Plain text isn't a command: no screen read, no 1
	r := &promptRig{statuses: []Status{status("idle", "1")}, screen: cacheScreen}
	if _, err := Prompt(r.client(), fakeClock(), "%1", "/effort high\nand more"); err != nil || pressed(r) != 0 {
		t.Errorf("Prompt(multi-line) = %v, cmds %q", err, r.cmds)
	}

	// PromptWait: a command counts no prompt, its screen says it ran
	idle := []Status{status("idle", "1")}
	working := slices.Repeat([]string{workingScreen}, 60) // 12s of polls
	waits := []struct {
		name    string
		screens []string
		err     error
		presses int
	}{
		{"local command after its dialog", []string{commandScreen, cacheScreen, doneScreen}, nil, 1},
		{"a long command (/compact) outlasts the stall", append(working, doneScreen), nil, 0},
		{"left in the input box", []string{commandScreen}, ErrStalled, 0},
	}
	for _, tt := range waits {
		t.Run("wait: "+tt.name, func(t *testing.T) {
			r := &promptRig{statuses: idle, screens: tt.screens}
			st, err := PromptWait(r.client(), fakeClock(), "%1", "/effort high", 5*time.Second, time.Minute)
			if !errors.Is(err, tt.err) || tt.err == nil && st.Trusted() != "idle" {
				t.Errorf("PromptWait = %q, %v; want idle, %v", st.Trusted(), err, tt.err)
			}
			if got := pressed(r); got != tt.presses {
				t.Errorf("pressed 1 %d times, want %d", got, tt.presses)
			}
		})
	}
	// A command that starts a turn counts a prompt like any other
	r = &promptRig{statuses: []Status{status("idle", "1"), status("working", "2"), status("idle", "2")}, screen: workingScreen}
	if st, err := PromptWait(r.client(), fakeClock(), "%1", "/review", 5*time.Second, time.Minute); err != nil || st.Trusted() != "idle" {
		t.Errorf("PromptWait(skill) = %q, %v; want idle", st.Trusted(), err)
	}
}

// TestInterrupted: Esc ending a turn fires no hook, so a working state is checked against the screen.
func TestInterrupted(t *testing.T) {
	working := status("working", "3")
	working.Since = "50"
	settle := "if-shell -F -t %1 #{&&:#{==:#{@agent_state},working},#{&&:#{==:#{@agent_since},50},#{==:#{@agent_prompts},3}}} set-option -p -t %1 @agent_state idle"
	settled := func(r *promptRig) bool {
		return slices.ContainsFunc(r.cmds, func(c string) bool { return strings.HasPrefix(c, settle) })
	}
	r := &promptRig{statuses: []Status{working, status("idle", "3")}, screen: idleScreen}
	if st, err := PromptWait(r.client(), fakeClock(), "%1", "next", 5*time.Second, time.Minute); !errors.Is(err, ErrStalled) || st.Trusted() != "idle" || !settled(r) || r.pasted != "next" {
		t.Errorf("PromptWait(interrupted) = %q, %v, settled %v, pasted %q; want sent", st.Trusted(), err, settled(r), r.pasted)
	}
	r = &promptRig{statuses: []Status{working}, screen: workingScreen}
	if _, err := PromptWait(r.client(), fakeClock(), "%1", "next", 5*time.Second, time.Minute); err == nil || settled(r) {
		t.Errorf("PromptWait(working) = %v, settled %v; want refused", err, settled(r))
	}
	// A prompt just sent may not be drawn yet over the last turn's "Interrupted"
	fresh := working
	fresh.Active = strconv.FormatInt(time.Now().Unix(), 10)
	r = &promptRig{statuses: []Status{fresh}, screen: idleScreen}
	if st, err := ReadChecked(r.client(), "%1"); err != nil || st.Trusted() != "working" || settled(r) {
		t.Errorf("ReadChecked(just prompted) = %q, %v, settled %v; want working", st.Trusted(), err, settled(r))
	}
	// wait notices within screenEvery: its first read, five polls, then the screen check
	r = &promptRig{statuses: append(slices.Repeat([]Status{working}, 7), status("idle", "3")), screens: []string{workingScreen, idleScreen}}
	clk := fakeClock()
	start := clk.Now()
	st, err := Wait(r.client(), clk, "%1", time.Minute, func(s Status) bool { return s.Trusted() == "idle" })
	if err != nil || st.Trusted() != "idle" || !settled(r) || clk.Now().Sub(start) > 2*screenEvery {
		t.Errorf("Wait(interrupted later) = %q, %v after %s, settled %v", st.Trusted(), err, clk.Now().Sub(start), settled(r))
	}
}

// TestPromptDecisionGone: Claude fires no hook when its dialog is answered No or Esc, so the waiting state
// is checked against the screen before a prompt is refused.
func TestPromptDecisionGone(t *testing.T) {
	stale := waitingFor("permission_prompt", "2")
	stale.Since = "50"
	fresh := stale
	fresh.Since = strconv.FormatInt(time.Now().Unix(), 10)
	codex := stale
	codex.Command, codex.StateCmd = "codex", "codex"
	moved := stale
	moved.Since = "60"
	settle := "if-shell -F -t %1 #{&&:#{==:#{@agent_state},waiting},#{==:#{@agent_since},50}} set-option -p -t %1 @agent_state "
	tests := []struct {
		name     string
		statuses []Status
		screen   string
		settled  string // the state recorded, "" for none
		sent     bool
	}{
		{"answered No: settles idle and sends", []Status{stale, status("idle", "2")}, idleScreen, "idle", true},
		{"answered, Claude goes on: settles working and queues", []Status{stale, status("working", "2")}, workingScreen, "working", true},
		{"dialog still open: refused", []Status{stale}, dialogScreen, "", false},
		{"screen it doesn't know: refused", []Status{stale}, "", "", false},
		{"a new dialog since: refused", []Status{stale, moved}, idleScreen, "idle", false},
		{"decision just recorded, its dialog maybe not drawn yet: refused", []Status{fresh}, idleScreen, "", false},
		{"not Claude: refused", []Status{codex}, idleScreen, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &promptRig{statuses: tt.statuses, screen: tt.screen}
			_, err := Prompt(r.client(), fakeClock(), "%1", "do X instead")
			if sent := r.pasted != ""; sent != tt.sent || sent != (err == nil) {
				t.Errorf("Prompt = %v, pasted %q; want sent %v", err, r.pasted, tt.sent)
			}
			var settled []string
			for _, cmd := range r.cmds {
				if rest, ok := strings.CutPrefix(cmd, settle); ok {
					settled = append(settled, rest)
				}
			}
			if tt.settled == "" && len(settled) > 0 || tt.settled != "" && (len(settled) != 1 || !strings.HasPrefix(settled[0], tt.settled+" ; ")) {
				t.Errorf("settled %q, want %q; cmds %q", settled, tt.settled, r.cmds)
			}
		})
	}
	// PromptWait needs an agent done with its turn: back to idle it sends, back to working it refuses
	r := &promptRig{statuses: []Status{stale, status("idle", "2"), status("idle", "3")}, screen: idleScreen}
	if st, err := PromptWait(r.client(), fakeClock(), "%1", "do X instead", 5*time.Second, time.Minute); err != nil || st.Trusted() != "idle" || r.pasted == "" {
		t.Errorf("PromptWait(answered No) = %q, %v, pasted %q; want idle, sent", st.Trusted(), err, r.pasted)
	}
	r = &promptRig{statuses: []Status{stale, status("working", "2")}, screen: workingScreen}
	if _, err := PromptWait(r.client(), fakeClock(), "%1", "x", 5*time.Second, time.Minute); err == nil || err.Error() != "agent is working: wait for it first" || r.pasted != "" {
		t.Errorf("PromptWait(Claude went on) = %v, pasted %q; want refused as working", err, r.pasted)
	}
}

func TestPromptEmpty(t *testing.T) {
	r := &promptRig{statuses: []Status{status("idle", "1")}}
	if _, err := Prompt(r.client(), fakeClock(), "%1", " \n"); err == nil || r.pasted != "" {
		t.Errorf("Prompt(blank) = %v, pasted %q; want error, nothing sent", err, r.pasted)
	}
}

func TestWait(t *testing.T) {
	idle := func(s Status) bool { return s.Trusted() == "idle" }
	r := &promptRig{statuses: []Status{status("working", "1"), status("working", "1"), status("idle", "1")}}
	if st, err := Wait(r.client(), fakeClock(), "%1", time.Minute, idle); err != nil || st.Trusted() != "idle" {
		t.Errorf("Wait = %q, %v; want idle", st.Trusted(), err)
	}
	r = &promptRig{statuses: []Status{status("working", "1")}}
	if _, err := Wait(r.client(), fakeClock(), "%1", time.Second, idle); !errors.Is(err, ErrTimeout) {
		t.Errorf("Wait on a busy agent = %v, want ErrTimeout", err)
	}
	// A crash leftover: the state's writer no longer runs in the pane
	gone := Status{Command: "zsh", State: "working", StateCmd: "claude"}
	r = &promptRig{statuses: []Status{gone}}
	if _, err := Wait(r.client(), fakeClock(), "%1", time.Minute, idle); !errors.Is(err, errNoState) {
		t.Errorf("Wait on a stale state = %v, want errNoState", err)
	}
	// /clear unsets the state briefly; an agent that left its pane fails after lostGrace, not the timeout
	cleared := status("", "1")
	r = &promptRig{statuses: []Status{status("working", "1"), cleared, cleared, status("idle", "1")}}
	if st, err := Wait(r.client(), fakeClock(), "%1", time.Minute, idle); err != nil || st.Trusted() != "idle" {
		t.Errorf("Wait across /clear = %q, %v; want idle", st.Trusted(), err)
	}
	r = &promptRig{statuses: []Status{status("working", "1"), gone}}
	clk := fakeClock()
	start := clk.Now()
	if _, err := Wait(r.client(), clk, "%1", time.Minute, idle); !errors.Is(err, errGone) || clk.Now().Sub(start) > 2*lostGrace {
		t.Errorf("Wait on an exited agent = %v after %s, want errGone within %s", err, clk.Now().Sub(start), 2*lostGrace)
	}
}

func TestSendKeys(t *testing.T) {
	r := &promptRig{}
	if err := SendKeys(r.client(), "%1", []string{"Esc", "shift+tab", "1", ";", "é"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"send-keys -t %1 Escape", "send-keys -t %1 BTab", "send-keys -t %1 -l 1", `send-keys -t %1 -l \;`, "send-keys -t %1 -l é"}
	if !slices.Equal(r.cmds, want) {
		t.Errorf("cmds = %q, want %q", r.cmds, want)
	}
	r = &promptRig{}
	if err := SendKeys(r.client(), "%1", []string{"enter", "ctrl+z"}); err == nil || len(r.cmds) != 0 {
		t.Errorf("SendKeys(unknown) = %v, sent %q; want error, nothing sent", err, r.cmds)
	}
}

func TestAwaitAgent(t *testing.T) {
	registered := false
	var captured []string
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "has-session":
			return "", nil
		case "capture-pane":
			captured = args
			return "Do you trust this folder?\n\n", nil
		}
		if registered {
			return paneRow("app", "%1", "2.1.3", "idle", "2.1.3", "app", ""), nil
		}
		return paneRow("app", "%1", "zsh", "", "", "app", ""), nil
	})
	clk := fakeClock()
	_, err := AwaitAgent(c, clk, "app", 2*time.Second)
	if !errors.Is(err, ErrTimeout) || !strings.HasSuffix(err.Error(), `session "app" has no agent pane; its screen:`+"\nDo you trust this folder?") {
		t.Errorf("AwaitAgent timeout = %v", err)
	}
	if want := []string{"capture-pane", "-p", "-t", "=app:1"}; !slices.Equal(captured, want) {
		t.Errorf("captured %q, want %q", captured, want)
	}
	registered = true
	if a, err := AwaitAgent(c, clk, "app", time.Second); err != nil || a.Pane != "%1" {
		t.Errorf("AwaitAgent = %+v, %v", a, err)
	}
	// Anything but "no agent yet" fails at once
	broken := tmux.NewFake(func(args []string) (string, error) { return "", errors.New("server exited") })
	start := clk.Now()
	if _, err := AwaitAgent(broken, clk, "app", time.Minute); err == nil || errors.Is(err, ErrTimeout) || clk.Now() != start {
		t.Errorf("AwaitAgent on a broken server = %v after %s", err, clk.Now().Sub(start))
	}
}

func TestListStartedByAndHerd(t *testing.T) {
	line := func(session, id, pane, startedBy, herd string) string {
		return tmux.FormatRow(session, "1", pane, "/p/"+session, "t", "claude", "idle", "claude", "", "", "", id, startedBy, herd, "")
	}
	out := line("herd", "$1", "%1", "", "1") + line("fix-login", "$2", "%2", "$1", "") + line("mine-too", "$3", "%3", "$1", "") +
		line("user", "$4", "%4", "", "") + line("orphan", "$5", "%5", "$9", "") +
		line("by-name", "$6", "%6", "herd", "") + line("not-a-boss", "$7", "%7", "user", "")
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return out, nil
		case "display-message":
			return "herd\n", nil
		}
		return "", nil
	})
	agents, err := list(c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range agents {
		got[a.Session] = fmt.Sprintf("%s/%v", a.StartedBy, a.Boss)
	}
	want := map[string]string{"herd": "/true", "fix-login": "herd/false", "mine-too": "herd/false", "user": "/false", "orphan": "/false",
		"by-name": "herd/false", "not-a-boss": "/false"}
	if !maps.Equal(got, want) {
		t.Errorf("started_by/herd = %v, want %v", got, want)
	}
	mine, err := StartedBy(c, agents, "%1")
	if err != nil || len(mine) != 3 || mine[0].Session != "fix-login" || mine[1].Session != "mine-too" || mine[2].Session != "by-name" {
		t.Errorf("StartedBy = %+v, %v; want fix-login, mine-too and by-name", mine, err)
	}
}

func TestListCostOff(t *testing.T) {
	row := tmux.FormatRow("app", "1", "%1", "/nonexistent/app", "t", "claude", "idle", "claude", "1.5", "", "", "$app", "", "", "off")
	got, err := List(tmux.NewFake(func([]string) (string, error) { return row, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].HideCost || got[0].CostUSD == nil || *got[0].CostUSD != 1.5 {
		t.Errorf("List = %+v, want HideCost with cost_usd 1.5 kept", got)
	}
}

func TestSendText(t *testing.T) {
	for text, want := range map[string]string{
		"-R":     "send-keys -t %1 -l -- -R",
		"a;b":    "send-keys -t %1 -l -- a;b",
		"done;":  `send-keys -t %1 -l -- done\;`,
		"medium": "send-keys -t %1 -l -- medium",
	} {
		var got string
		c := tmux.NewFake(func(args []string) (string, error) { got = strings.Join(args, " "); return "", nil })
		if err := SendText(c, "%1", text); err != nil || got != want {
			t.Errorf("SendText(%q) ran %q, %v; want %q", text, got, err, want)
		}
	}
}
