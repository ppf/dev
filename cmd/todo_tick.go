package cmd

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// Where the user ticked from, besides sidebar.WhereBoard and sidebar.WhereSidebar.
const (
	wherePage  = "the web page"
	whereShell = "his shell"
)

func openItems(s todo.Store, ids []int, now time.Time) ([]todo.Shown, error) {
	l, err := s.Read(now)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(l.Items, func(it todo.Shown) bool { return it.Done || !slices.Contains(ids, it.ID) }), nil
}

// tellTicked tells the owner of boss name's list that the user ticked items, where; none for a quiet item.
func tellTicked(c *tmux.Client, name string, closed bool, items []todo.Shown, where string, warn io.Writer) {
	for _, it := range items {
		if it.Quiet {
			continue
		}
		tellOwner(c, name, closed, warn, fmt.Sprintf("#%d ticked", it.ID), func(to inbox.Boss) (bool, error) {
			return to.Send(c, control.RealClock, inbox.UserTicked(name, it.ID, it.Text, where, closed))
		})
	}
}

// tellReopened tells the owner of boss name's list that the user reopened its item id as it, unless it is quiet.
func tellReopened(c *tmux.Client, name string, closed bool, id int, it todo.Item, warn io.Writer) {
	if it.Quiet {
		return
	}
	tellOwner(c, name, closed, warn, fmt.Sprintf("#%d reopened", id), func(to inbox.Boss) (bool, error) {
		return to.SendReopened(c, control.RealClock, name, id, it.Text, it.ID, closed)
	})
}

// tellOwner sends the boss of list name, or the manager for a closed boss's, what send queues in its inbox. The tick or
// reopen it tells of is done, so a failure is said on warn. A desktop app boss or a list with no boss has no dev inbox, nor has a session with its inbox off: no one is told.
func tellOwner(c *tmux.Client, name string, closed bool, warn io.Writer, what string, send func(to inbox.Boss) (queued bool, err error)) {
	if name == "" || strings.HasPrefix(name, "local_") {
		return
	}
	recipient := name
	if closed {
		recipient = inbox.Manager
	}
	to, err := inbox.Recipient(c, recipient)
	if errors.Is(err, inbox.ErrInboxOff) || errors.Is(err, inbox.ErrNotBoss) {
		return
	}
	if err != nil {
		fmt.Fprintf(warn, "⚠ dev todo: %s, no one told: %v\n", what, err)
		return
	}
	queued, err := send(to)
	switch {
	case !queued:
		fmt.Fprintf(warn, "⚠ dev todo: %s, not sent to %s (tell it yourself): %v\n", what, to.Name, err)
	case err != nil:
		fmt.Fprintf(warn, "⚠ dev todo: %s, queued in %s's inbox, not delivered yet: %v\n", what, to.Name, err)
	}
}
