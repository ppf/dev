package cmd

import (
	"fmt"
	"io"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// setStatuses sets the statuses (todo.Owner.Status) of each list's items from the sessions live on c's server; when
// those can't be read, the lists still print, an item on someone without a status, and stderr says why.
func setStatuses(c *tmux.Client, lists []bossList, stderr io.Writer) {
	live, err := liveSessions(c, stderr)
	for _, bl := range lists {
		o := todo.OwnerOf(live, bl.Boss, bl.Closed)
		o.Unread = o.Unread || err != nil
		bl.SetStatus(o)
	}
}

// setStatus sets the statuses of l, s's list as setStatuses does; a desktop app boss's workers aren't read, so an
// item on someone has none.
func setStatus(s todo.Store, l todo.List, stderr io.Writer) {
	if s.Boss() == "" {
		l.SetStatus(todo.Owner{Unread: true})
		return
	}
	setStatuses(tmux.New(), []bossList{{Boss: s.Boss(), List: l}}, stderr)
}

// liveSessions is sidebar.Live's sessions; a failure is reported on stderr.
func liveSessions(c *tmux.Client, stderr io.Writer) ([]sidebar.Session, error) {
	live, err := sidebar.Live(c)
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: sessions not read, items on someone have no status: "+err.Error())
	}
	return live, err
}
