package cmd

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
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

// setStatus sets the statuses of l, s's list as setStatuses does; a desktop app boss's (boss, as todoBoss reads it)
// from its workers on the desktop board (desktopOwner).
func setStatus(s todo.Store, l todo.List, boss string, stderr io.Writer) {
	if s.Boss() == "" {
		l.SetStatus(desktopOwner(boss, stderr))
		return
	}
	setStatuses(tmux.New(), []bossList{{Boss: s.Boss(), List: l}}, stderr)
}

// desktopOwner is the desktop app boss's Owner: its workers on the desktop board (desktop.Board, without its question
// files, so it removes none), each read as the rank of its desktop status (desktopSession). When the board can't be read, or a
// worker's record can't, it is Unread and stderr says why: an unread record may be a live worker's.
func desktopOwner(boss string, stderr io.Writer) todo.Owner {
	unread := func(err error) todo.Owner {
		fmt.Fprintln(stderr, "⚠ dev todo: workers not read, items on someone have no status: "+err.Error())
		return todo.Owner{Unread: true}
	}
	_, id, err := todoBoss(boss)
	if err != nil {
		return unread(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return unread(err)
	}
	p := desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	if p.Hooks, err = desktop.HookDir(); err != nil {
		return unread(err)
	}
	workers, warnings, err := desktop.Board(p, id, time.Now())
	if err != nil {
		return unread(err)
	}
	for _, w := range warnings {
		if recordID.MatchString(w.Error()) {
			return unread(w)
		}
	}
	o := todo.Owner{Workers: make([]sidebar.Session, len(workers))}
	for i, w := range workers {
		o.Workers[i] = desktopSession(w)
	}
	return o
}

// desktopSession is desktop worker w as a session whose Rank is its status's: needs you and blocked wait on the
// user, working and stuck work, waiting and rate-limited wait (as a planned wait or a rate-limit resume), and review
// ready, idle and stopped rest. ID is its app session id, which its linked items name.
func desktopSession(w desktop.Worker) sidebar.Session {
	s := sidebar.Session{Name: cmp.Or(w.Name, w.Title, w.ID), ID: w.ID, Agent: "Claude"}
	switch w.Status {
	case desktop.StatusNeedsYou, desktop.StatusBlocked:
		s.Deciding = true
	case desktop.StatusWorking, desktop.StatusStuck:
		s.State = agent.Working
	case desktop.StatusWaiting, desktop.StatusRateLimited:
		s.Resume = true
	}
	return s
}

// liveSessions is sidebar.Live's sessions; a failure is reported on stderr.
func liveSessions(c *tmux.Client, stderr io.Writer) ([]sidebar.Session, error) {
	live, err := sidebar.Live(c)
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: sessions not read, items on someone have no status: "+err.Error())
	}
	return live, err
}
