package control

import (
	"errors"
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
	return tmux.FormatRow(session, "1", pane, "/nonexistent/"+session, "title", command, state, stateCmd, "", project, sidebar)
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

// promptRig fakes a pane whose status follows statuses, one per display-message (the last repeats),
// and records the other commands and the pasted text.
type promptRig struct {
	statuses []Status
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
			return tmux.FormatRow(st.Command, st.State, st.StateCmd, st.Prompts, st.WaitingFor), nil
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

func TestPrompt(t *testing.T) {
	// A busy agent queues the prompt
	r := &promptRig{statuses: []Status{status("working", "1")}}
	if err := Prompt(r.client(), fakeClock(), "%1", "next"); err != nil || r.pasted != "next" {
		t.Errorf("Prompt(busy) = %v, pasted %q", err, r.pasted)
	}
	r = &promptRig{statuses: []Status{waitingFor("elicitation_dialog", "1")}}
	if err := Prompt(r.client(), fakeClock(), "%1", "2"); err == nil || r.pasted != "" {
		t.Errorf("Prompt(dialog) = %v, pasted %q; want error, nothing sent", err, r.pasted)
	}
	// A failed paste deletes the buffer and never presses Enter
	r = &promptRig{statuses: []Status{status("idle", "1")}, pasteErr: errors.New("no pane")}
	err := Prompt(r.client(), fakeClock(), "%1", "x")
	buffer := "dev-agent-" + strconv.Itoa(os.Getpid())
	if want := []string{"paste-buffer -p -d -b " + buffer + " -t %1", "delete-buffer -b " + buffer}; err == nil || !slices.Equal(r.cmds, want) {
		t.Errorf("Prompt(paste fails) = %v, cmds %q; want error, %q", err, r.cmds, want)
	}
}

func TestPromptEmpty(t *testing.T) {
	r := &promptRig{statuses: []Status{status("idle", "1")}}
	if err := Prompt(r.client(), fakeClock(), "%1", " \n"); err == nil || r.pasted != "" {
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
