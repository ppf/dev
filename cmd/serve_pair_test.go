package cmd

import (
	"errors"
	"os"
	"slices"
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

// A popup tmux refuses, or one that never shows (tmux answers success on a client showing a popup already), gives
// the prompt back, so the next try may pair; one that shows takes it.
func TestServePairPopupStart(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	box, err := inbox.Open("/tmp/tmux-1/default", "$1")
	if err != nil {
		t.Fatal(err)
	}
	if err := box.Arrived("pair my phone", time.Now()); err != nil {
		t.Fatal(err)
	}
	var popup []string
	var refuse, shows bool
	origTmux, origSelf, origWait := pairTmux, pairSelf, pairAckWait
	t.Cleanup(func() { pairTmux, pairSelf, pairAckWait = origTmux, origSelf, origWait })
	pairAckWait = 200 * time.Millisecond
	pairSelf = func(*tmux.Client, string) (inbox.Boss, error) { return inbox.Boss{Name: inbox.Manager, Box: box}, nil }
	pairTmux = func() *tmux.Client {
		return tmux.NewFake(func(args []string) (string, error) {
			if args[0] == "list-clients" {
				return tmux.FormatRow("/dev/ttys001", "manager", "100"), nil
			}
			if popup = args; refuse {
				return "", &tmux.Error{Args: args, Stderr: "can't find client: /dev/ttys001", Err: errors.New("exit status 1")}
			}
			for _, a := range args {
				if ack, ok := strings.CutPrefix(a, pairGuard+"="); ok && shows {
					return "", os.WriteFile(ack, nil, 0o600)
				}
			}
			return "", nil
		})
	}
	refuse = true
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "can't find client") || out != "" {
		t.Errorf("refused popup: %q %v, want its error", out, err)
	}
	refuse = false
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "the popup didn't show") || out != "" {
		t.Errorf("popup with no ack: %q %v, want refused", out, err)
	}
	shows = true
	if out, _, err := execute(t, "", "serve", "pair", "--popup"); err != nil || out != "pair popup opened on /dev/ttys001: the code shows only there\n" {
		t.Errorf("popup after failed ones: %q %v", out, err)
	}
	for _, want := range []string{"CLAUDECODE=", "XDG_STATE_HOME=" + state} {
		if !slices.Contains(popup, want) {
			t.Errorf("popup %q: want %s", popup, want)
		}
	}
	if _, _, err := execute(t, "", "serve", "pair", "--popup"); err == nil || !strings.Contains(err.Error(), "paired a phone already") {
		t.Errorf("second popup for one prompt: %v, want refused", err)
	}
}
