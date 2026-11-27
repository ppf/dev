package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// pairGuard marks dev serve pair run in the --popup popup: it waits for Enter before the popup closes.
const pairGuard = "DEV_SERVE_PAIR_IN_POPUP"

var pairTmux, pairSelf = tmux.New, managerSelf // test seams

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
	if err := self.Box.ClaimPair(*p); err != nil {
		return fmt.Errorf("dev serve pair --popup: %w", err)
	}
	// Detached: display-popup returns only once the user closes it (Start sees tmux failing at once). CLAUDECODE
	// emptied: the user pairs in it
	err = c.Start("display-popup", "-c", client.TTY, "-E", "-b", "rounded", "-T", " pair a phone ",
		"-s", "bg=terminal,fg=terminal", "-w", "80%", "-h", "8", "-e", pairGuard+"=1", "-e", "CLAUDECODE=",
		"--", tmux.ShellJoin(bin, "serve", "pair"))
	if err != nil {
		return errors.Join(err, self.Box.UnclaimPair(*p))
	}
	_, err = fmt.Fprintf(out, "pair popup opened on %s: the code shows only there\n", client.TTY)
	return err
}
