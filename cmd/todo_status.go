package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// setStatuses sets the statuses (todo.Owner.Status) of each list's items from the sessions live on c's server; when
// those can't be read, the lists still print, an item on someone without a status, and stderr says why.
// With withNow, an item on a worker also gets what the worker does now (doing, a transcript read each): dev serve's
// board only, as the sidebars poll this every 2 s.
func setStatuses(c *tmux.Client, lists []bossList, now time.Time, withNow bool, stderr io.Writer) {
	live, err := liveSessions(c, stderr)
	for _, bl := range lists {
		o := todo.OwnerOf(live, bl.Boss, bl.Closed)
		o.Unread = o.Unread || err != nil
		if withNow {
			o.Doing = func(s sidebar.Session) string { return doing(s, now, stderr) }
		}
		bl.SetStatus(o)
	}
}

// setStatus sets the statuses of l, s's list: a tmux boss's as setStatuses does, a desktop app boss's (boss, as
// todoBoss reads it) from its workers on the desktop board (desktopOwner).
func setStatus(s todo.Store, l todo.List, boss string, stderr io.Writer) {
	if s.Boss() == "" {
		// Only an item on someone but the boss itself reads the workers (a ticked one too, for its assignee): the board
		// polls this every 2 s
		if !slices.ContainsFunc(l.Items, func(it todo.Shown) bool { return it.On != "" && !todo.OnBoss(it.On, it.Session) }) {
			l.SetStatus(todo.Owner{})
			return
		}
		o, unreadIDs := desktopOwner(boss, stderr)
		l.SetStatus(o)
		// An item on a worker whose record wasn't read is placed as with no workers read: no status
		for i, it := range l.Items {
			if it.Session != nil && slices.Contains(unreadIDs, it.Session.ID) {
				todo.List{Items: l.Items[i : i+1]}.SetStatus(todo.Owner{Unread: true})
			}
		}
		return
	}
	setStatuses(tmux.New(), []bossList{{Boss: s.Boss(), List: l}}, time.Now(), false, stderr)
}

// desktopOwner is the desktop app boss's Owner: its workers on the desktop board (desktop.Board, without its question
// files, so it removes none), each read as the rank of its desktop status (desktopSession), and the app session ids of
// records it couldn't read, which may be live workers'. When the board, or a live session file, can't be read, it is
// Unread: a running worker would read stopped. stderr says why.
func desktopOwner(boss string, stderr io.Writer) (todo.Owner, []string) {
	unread := func(err error) (todo.Owner, []string) {
		fmt.Fprintln(stderr, "⚠ dev todo: workers not read, items on someone have no status: "+err.Error())
		return todo.Owner{Unread: true}, nil
	}
	_, id, err := todoBoss(boss)
	switch {
	case err != nil:
		return unread(err)
	case strings.HasPrefix(id, t3Boss):
		// No workers dev can see: an item on one reads as on a person
		return todo.Owner{}, nil
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
	var unreadIDs []string
	for _, w := range warnings {
		msg := w.Error()
		// A live file with a status Board doesn't know still shows its worker, busy
		if strings.Contains(msg, p.Live) && !strings.Contains(msg, "doesn't know") {
			return unread(w)
		}
		for _, rec := range recordID.FindAllString(msg, -1) {
			fmt.Fprintln(stderr, "⚠ dev todo: "+msg)
			unreadIDs = append(unreadIDs, strings.TrimSuffix(rec, ".json"))
		}
	}
	o := todo.Owner{Workers: make([]sidebar.Session, len(workers))}
	for i, w := range workers {
		o.Workers[i] = desktopSession(w)
		if !w.Running() {
			o.Stopped = append(o.Stopped, w.ID)
		}
	}
	return o, unreadIDs
}

// desktopSession is desktop worker w as a session whose Rank is its status's: needs you and blocked wait on the
// user, working and stuck work, waiting and rate-limited wait (as a planned wait or a rate-limit resume), and review
// ready, idle and stopped rest. ID is its app session id, which its linked items name.
func desktopSession(w desktop.Worker) sidebar.Session {
	s := sidebar.Session{Name: cmp.Or(w.Name, w.Title, w.ID), ID: w.ID, Agent: "Claude", Doing: w.StatusText}
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

// doing is what tmux worker s is doing now, as the boss board's card says it (herd's own section and statusText):
// "" in a dialog (herd reads that off the screen); its planned wait; working, its tool call's description, else the
// tool; its question when its turn ended on one (herd's card says "asks its boss" instead when its boss answers it);
// else what its reply's newest WAITING marker names. A transcript not written yet says nothing.
func doing(s sidebar.Session, now time.Time, stderr io.Writer) string {
	switch {
	case s.Deciding:
		return ""
	case s.Wait.Held():
		return s.Wait.Text(now)
	case sidebar.NoteKind(s.Note) == sidebar.NoteAsks && s.State != agent.Working:
		_, ask, _ := strings.Cut(s.Note, ": ")
		return strings.Join(strings.Fields(ask), " ")
	case s.Transcript == "":
		return ""
	}
	t, err := desktop.ReadTail(s.Transcript)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "⚠ dev todo: "+s.Name+"'s transcript not read: "+err.Error())
		}
		return ""
	}
	what := cmp.Or(t.ToolDescription, t.Tool)
	if s.State != agent.Working {
		if st, marked, _ := desktop.Marker(t.Lines); st == desktop.StatusWaiting {
			what = marked
		} else {
			what = ""
		}
	}
	return strings.Join(strings.Fields(what), " ")
}

// liveSessions is sidebar.Live's sessions; a failure is reported on stderr.
func liveSessions(c *tmux.Client, stderr io.Writer) ([]sidebar.Session, error) {
	live, err := sidebar.Live(c)
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: sessions not read, items on someone have no status: "+err.Error())
	}
	return live, err
}
