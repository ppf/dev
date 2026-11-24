package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// gateSendKeys refuses keys to a, a worker whose decision went to the manager (inbox.HandedToManager), from a Claude
// in any pane but the manager's coordinator, until it is answered; the user in a shell is let through.
func gateSendKeys(c *tmux.Client, a control.Agent) error {
	// Only a decision can be handed over: keys to a working or idle agent (Esc) aren't looked into
	if os.Getenv("CLAUDECODE") == "" && os.Getenv("T3CODE_THREAD_ID") == "" || a.State != agent.Waiting {
		return nil
	}
	// A dialog closed without a hook (declined, Esc) holds no decision
	st, err := control.ReadChecked(c, a.Pane)
	if err != nil {
		return err
	}
	handed, err := inbox.HandedToManager(c, a.Pane, st.Trusted())
	if err != nil || !handed {
		return err
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		self, ok, err := inbox.Coordinator(c, pane)
		switch {
		// A pane this tmux server can't find (TMUX_PANE from another server) is no manager's
		case err != nil && !strings.Contains(err.Error(), "can't find pane"):
			return fmt.Errorf("%s's decision went to the manager; checking for the manager's pane: %w", a.Session, err)
		case err == nil && ok && self.Name == inbox.Manager:
			return nil
		}
	}
	return fmt.Errorf("%s's decision went to the manager, which asks the user and sends their pick: leave it to the manager until it is answered", a.Session)
}
