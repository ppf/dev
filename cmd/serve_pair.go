package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// pairGuard is set to the ack file in the --popup popup: dev serve pair creates it before making a code (refused
// once --popup stopped waiting), writes there what it showed, and waits for Enter before the popup closes.
const pairGuard = "DEV_SERVE_PAIR_IN_POPUP"

// ackCoded is the ack of a popup that showed a code; any other is why it showed none.
const ackCoded = "code"

var pairTmux, pairSelf = tmux.New, managerSelf // test seams

// pairAckWait is how long --popup waits for the popup's ack file (tmux answers a popup on a client showing one
// already with success and nothing shown), pairShowWait then for what it showed.
var pairAckWait, pairShowWait = 10 * time.Second, 10 * time.Second

// errShowUnknown: the popup is up but hasn't said what it showed.
var errShowUnknown = errors.New("not said")

// pairPopup opens dev serve pair in a popup on the user's manager client, once per user prompt; the code shows only there.
func pairPopup(out io.Writer) error {
	c := pairTmux()
	self, err := pairSelf(c, "dev serve pair --popup")
	if err != nil {
		return err
	}
	p, err := self.Box.UserPrompt()
	switch {
	case err != nil:
		return err
	case p == nil:
		return errors.New("dev serve pair --popup: the user hasn't prompted the manager yet: pair only when they ask")
	}
	clients, err := tmux.Query[sidebar.Client](c, "list-clients", "-t", tmux.SessionTarget(sidebar.Manager))
	if err != nil {
		return err
	}
	client, ok := sidebar.ActiveClient(clients, "")
	if !ok {
		return errors.New("dev serve pair --popup: no client shows the manager: ask the user to switch to it, then run it again")
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	// The popup runs in tmux's folder: a relative one would be another there
	state := os.Getenv("XDG_STATE_HOME")
	if state != "" {
		if state, err = filepath.Abs(state); err != nil {
			return err
		}
	}
	// In the state folder the popup gets, not this process's TMPDIR, which a sandbox may give it alone
	base, err := authDir()
	if err == nil {
		base, err = filepath.Abs(base)
	}
	if err == nil {
		err = os.MkdirAll(base, 0o700)
	}
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(base, "pair-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := self.Box.ClaimPair(*p); err != nil {
		return fmt.Errorf("dev serve pair --popup: %w", err)
	}
	// Detached: display-popup returns only once the user closes it. CLAUDECODE emptied: the user pairs in it; this
	// state folder, not the tmux server's, holds the running dev serve's records
	err = c.Start("display-popup", "-c", client.TTY, "-E", "-b", "rounded", "-T", " pair a phone ",
		"-s", "bg=terminal,fg=terminal", "-w", "80%", "-h", "12", "-e", pairGuard+"="+filepath.Join(dir, "ack"),
		"-e", "CLAUDECODE=", "-e", "XDG_STATE_HOME="+state, "--", tmux.ShellJoin(bin, "serve", "pair"))
	if err == nil {
		err = awaitAck(dir)
	}
	if errors.Is(err, errShowUnknown) {
		_, err = fmt.Fprintf(out, "pair popup opened on %s; it hasn't said yet whether it showed a code: ask the user\n", client.TTY)
		return err
	}
	if err != nil {
		return errors.Join(err, self.Box.UnclaimPair(*p))
	}
	_, err = fmt.Fprintf(out, "pair popup opened on %s: the code shows only there\n", client.TTY)
	return err
}

// awaitAck waits for the popup's ack file in dir, then for what it showed: nil for a code, errShowUnknown when it
// doesn't say in time.
func awaitAck(dir string) error {
	ack := filepath.Join(dir, "ack")
	for end := time.Now().Add(pairAckWait); !exists(ack); time.Sleep(50 * time.Millisecond) {
		if time.Now().Before(end) {
			continue
		}
		// Closed at once: a popup after it finds no folder and makes no code; one before it is up
		closed := dir + ".closed"
		if err := os.Rename(dir, closed); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(closed) }()
		if ack = filepath.Join(closed, "ack"); !exists(ack) {
			return errors.New("dev serve pair --popup: the popup didn't show (another popup or menu open on the user's client, or a slow start?): ask them to close it, then run it again")
		}
		break
	}
	for end := time.Now().Add(pairShowWait); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		switch b, err := os.ReadFile(ack); {
		case err != nil:
			return err
		case len(b) == 0 || b[len(b)-1] != '\n': // not all written yet
		case string(b) == ackCoded+"\n":
			return nil
		default:
			return fmt.Errorf("dev serve pair --popup: the popup showed no code: %s", strings.TrimSpace(string(b)))
		}
	}
	return errShowUnknown
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// openAck creates the popup's ack file, refused once --popup stopped waiting (its folder gone) or for a file there
// already, so no file of the caller's is changed.
func openAck(ack string) (*os.File, error) {
	f, err := os.OpenFile(ack, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the manager stopped waiting for this popup: ask it again (%w)", err)
	}
	return f, nil
}

// writeAck tells --popup what the popup showed, never the code.
func writeAck(f *os.File, coded bool, failed error) error {
	what := ackCoded
	switch {
	case failed != nil:
		what = failed.Error()
	case !coded:
		what = "dev serve listens on this Mac only: a phone can't reach it"
	}
	_, err := f.WriteString(what + "\n")
	return errors.Join(err, f.Close())
}
