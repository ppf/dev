package sidebar

import "github.com/ppf/dev/internal/tmux"

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
	target := order[n-1]
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
