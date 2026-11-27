package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// dev serve pair --popup is refused outside the manager's coordinator pane, in Claude Code too.
func TestServePairPopupRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("TMUX_PANE", "")
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "runs only in the manager's coordinator pane") || out != "" {
		t.Errorf("--popup outside the manager: %q %v, want refused", out, err)
	}
}

// The prompt is given back unless the popup showed a code: tmux refusing it, no ack (tmux answers success on a client
// showing a popup already), or an ack of no code; a popup that never says keeps it. One popup per prompt once a code showed.
func TestServePairPopupAck(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	box, err := inbox.Open("/tmp/tmux-1/default", "$1")
	if err != nil {
		t.Fatal(err)
	}
	if err := box.Arrived("pair my phone", time.Now()); err != nil {
		t.Fatal(err)
	}
	origTmux, origSelf, origAck, origShow := pairTmux, pairSelf, pairAckWait, pairShowWait
	t.Cleanup(func() { pairTmux, pairSelf, pairAckWait, pairShowWait = origTmux, origSelf, origAck, origShow })
	pairAckWait, pairShowWait = 200*time.Millisecond, 200*time.Millisecond
	pairSelf = func(*tmux.Client, string) (inbox.Boss, error) { return inbox.Boss{Name: inbox.Manager, Box: box}, nil }
	var popup func(ack string) error // what the popup does with its ack file
	var argv []string
	pairTmux = func() *tmux.Client {
		return tmux.NewFake(func(args []string) (string, error) {
			if args[0] == "list-clients" {
				return tmux.FormatRow("/dev/ttys001", "manager", "100"), nil
			}
			argv = args
			for _, a := range args {
				if ack, ok := strings.CutPrefix(a, pairGuard+"="); ok {
					return "", popup(ack)
				}
			}
			return "", errors.New("no ack file passed")
		})
	}
	acks := func(what string) func(string) error {
		return func(ack string) error {
			f, err := openAck(ack)
			if err != nil {
				return err
			}
			if what == ackCoded {
				return writeAck(f, true, nil)
			}
			return writeAck(f, false, errors.New(what))
		}
	}
	var late string
	for _, c := range []struct {
		name  string
		popup func(string) error
		want  string
	}{
		{"tmux refusing it", func(string) error {
			return &tmux.Error{Stderr: "can't find client: /dev/ttys001", Err: errors.New("exit status 1")}
		}, "can't find client"},
		{"no ack", func(ack string) error { late = ack; return nil }, "the popup didn't show"},
		{"an ack of no code", acks("dev serve is not running: start it first"), "the popup showed no code: dev serve is not running"},
	} {
		popup = c.popup
		if out, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), c.want) || out != "" {
			t.Errorf("%s: %q %v, want %q", c.name, out, err, c.want)
		}
	}
	// A popup up only after --popup stopped waiting makes no code
	if _, err := openAck(late); err == nil || !strings.Contains(err.Error(), "the manager stopped waiting") {
		t.Errorf("late popup's ack: %v, want refused", err)
	}

	popup = acks(ackCoded)
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err != nil || out != "pair popup opened on /dev/ttys001: the code shows only there\n" {
		t.Errorf("popup showing a code: %q %v", out, err)
	}
	for _, want := range []string{"CLAUDECODE=", "XDG_STATE_HOME=" + state} {
		if !strings.Contains(strings.Join(argv, "\n"), "\n"+want+"\n") {
			t.Errorf("popup %q: want %s", argv, want)
		}
	}
	if _, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "paired a phone already") {
		t.Errorf("second popup for one prompt: %v, want refused", err)
	}

	// Up but silent, or caught half-way through its ack: the prompt stays taken
	if err := box.Arrived("pair my tablet", time.Now()); err != nil {
		t.Fatal(err)
	}
	popup = func(ack string) error {
		f, err := openAck(ack)
		if err == nil {
			_, err = f.WriteString(ackCoded[:2])
		}
		return err
	}
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err != nil || !strings.Contains(out, "hasn't said yet whether it showed a code") {
		t.Errorf("silent popup: %q %v", out, err)
	}
	if _, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "paired a phone already") {
		t.Errorf("popup after a silent one: %v, want refused", err)
	}
}
