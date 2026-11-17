package sidebar

import (
	"fmt"
	"os"

	"github.com/ppf/dev/internal/tmux"
)

// Goto selects the n-th session (1-based) in Order and switches client to it (prefix+1..9,
// goto_session); n out of range does nothing, the current session only gets selected.
// The order is computed fresh: bash's @sidebar_session_order went stale while sidebars were hidden.
func Goto(c *tmux.Client, n int, client string) error {
	order, err := LoadOrder(c)
	if err != nil {
		return err
	}
	if n < 1 || n > len(order) {
		return nil
	}
	return selectAndSwitch(c, order[n-1], client)
}

// GotoSession is Goto for the session named session; outside tmux with no client it attaches it.
func GotoSession(c *tmux.Client, session, client string) error {
	target := tmux.SessionTarget(session)
	if _, err := c.Run("has-session", "-t", target); err != nil {
		return fmt.Errorf("no session %s: %w", session, err)
	}
	if client == "" && os.Getenv("TMUX") == "" {
		// Like switchTo: a missing sidebar never blocks the attach
		_ = Ensure(c, session, Origin{})
		return c.Interactive("attach-session", "-t", target)
	}
	return selectAndSwitch(c, session, client)
}

// selectAndSwitch selects target and switches client to it unless it is already there.
func selectAndSwitch(c *tmux.Client, target, client string) error {
	if err := setSelected(c, target); err != nil {
		return err
	}
	from, err := ResolveOrigin(c, client)
	if err != nil {
		return err
	}
	if target == from.Session {
		return nil
	}
	return switchTo(c, client, target, from)
}
