package cmd

import (
	"fmt"
	"os"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// gateSendKeys refuses keys to a, a worker whose decision went to the manager (inbox.HandedToManager), from any pane
// but the manager's coordinator, until it is answered.
func gateSendKeys(c *tmux.Client, a control.Agent) error {
	handed, err := inbox.HandedToManager(c, a.Pane, a.State)
	if err != nil || !handed {
		return err
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		if self, ok, err := inbox.Coordinator(c, pane); err == nil && ok && self.Name == inbox.Manager {
			return nil
		}
	}
	return fmt.Errorf("%s's decision went to the manager, which asks the user and sends their pick: leave it to the manager until it is answered", a.Session)
}
