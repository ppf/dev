package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// pairGuard is set to the ack file in the --popup popup: dev serve pair writes it empty once the popup shows, and
// waits for Enter before the popup closes.
const pairGuard = "DEV_SERVE_PAIR_IN_POPUP"

var pairTmux, pairSelf = tmux.New, managerSelf // test seams

// pairAckWait is how long --popup waits for the popup's ack: tmux answers a popup on a client showing one already
// with success and nothing shown.
var pairAckWait = 3 * time.Second

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
	dir, err := os.MkdirTemp("", "dev-pair-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ack := filepath.Join(dir, "ack")
	if err := self.Box.ClaimPair(*p); err != nil {
		return fmt.Errorf("dev serve pair --popup: %w", err)
	}
	// Detached: display-popup returns only once the user closes it. CLAUDECODE emptied: the user pairs in it; this
	// state folder, not the tmux server's, holds the running dev serve's records
	err = c.Start("display-popup", "-c", client.TTY, "-E", "-b", "rounded", "-T", " pair a phone ",
		"-s", "bg=terminal,fg=terminal", "-w", "80%", "-h", "12", "-e", pairGuard+"="+ack, "-e", "CLAUDECODE=",
		"-e", "XDG_STATE_HOME="+os.Getenv("XDG_STATE_HOME"), "--", tmux.ShellJoin(bin, "serve", "pair"))
	if err == nil {
		err = awaitAck(ack)
	}
	if err != nil {
		return errors.Join(err, self.Box.UnclaimPair(*p))
	}
	_, err = fmt.Fprintf(out, "pair popup opened on %s: the code shows only there\n", client.TTY)
	return err
}

// awaitAck waits pairAckWait for the popup's dev serve pair to write ack.
func awaitAck(ack string) error {
	for end := time.Now().Add(pairAckWait); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(ack); err == nil {
			return nil
		}
	}
	return errors.New("dev serve pair --popup: the popup didn't show (another popup or menu open on the user's client?): ask them to close it, then run it again")
}
