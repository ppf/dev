package todo

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// An open item's status: the one word both boards group and mark it by (sidebar.Statuses).
const (
	StatusDecide     = sidebar.StatusDecide
	StatusYours      = sidebar.StatusYours
	StatusStarting   = sidebar.StatusStarting
	StatusWorking    = sidebar.StatusWorking
	StatusBlocked    = sidebar.StatusBlocked
	StatusAsks       = sidebar.StatusAsks
	StatusWaiting    = sidebar.StatusWaiting
	StatusIdle       = sidebar.StatusIdle
	StatusParked     = sidebar.StatusParked
	StatusGone       = sidebar.StatusGone
	StatusCheck      = sidebar.StatusCheck
	StatusBossClosed = sidebar.StatusBossClosed
)

// Statuses is the order boards list items by status within a group (sidebar.Groups).
var Statuses = sidebar.Statuses

// Owner is a list's boss as statuses read it: Name its session's, Closed when it's gone, Workers the live sessions it
// started (sidebar.Live, or a desktop boss's workers as sessions); Unread when those weren't read, so an item on someone has no status.
type Owner struct {
	Name    string
	Closed  bool
	Workers []sidebar.Session
	Unread  bool
	// Doing is what a worker an item is on does now, asked only for those; nil: its Session.Doing
	Doing func(sidebar.Session) string
	// Stopped are the IDs of Workers with no live Claude: desktop app sessions not archived, their process ended
	Stopped []string
}

// OwnerOf is boss's Owner among the live sessions: its workers are those it started, bosses among them; Unread when an
// open boss isn't among them (it closed since its list was read, or the server went), as its workers can't be told.
func OwnerOf(live []sidebar.Session, boss string, closed bool) Owner {
	o := Owner{Name: boss, Closed: closed}
	i := slices.IndexFunc(live, func(s sidebar.Session) bool { return s.Name == boss })
	if i < 0 {
		o.Unread = !closed
		return o
	}
	id := live[i].ID
	for _, s := range live {
		if s.Name != boss && sidebar.Names(s.StartedBy, id, boss, true) {
			o.Workers = append(o.Workers, s)
		}
	}
	return o
}

// Status is its status on o's list and whom it names (its worker or person, "" for none); "" for a ticked item, and
// for an item on someone while o's workers weren't read. The first that holds:
//   - the user's own, asked to start: starting, naming the boss the start went to, its boss closed too;
//   - its boss closed: check when its linked PR moved (sidebar.Check.Event), else boss closed, naming whom it's on;
//   - on no one, or unlinked on the list's own boss (an older dev filed --on <own name> as a person): decide for an
//     ask, else check when its PR moved, else yours; an ask handed to someone goes by whom it's on, as below;
//   - on the boss itself (--on boss): check when its PR moved, else working while it says it does it (Doing), else
//     parked (Parked), idle once started, naming the boss (boss for a desktop app boss's, which has no name);
//   - on a linked session o didn't start, or no longer listed: gone;
//   - on a worker in a dialog, with a question no running boss answers, or resting after a failed turn: blocked;
//   - on a worker whose turn ended on a question as text its running boss answers (sidebar.Session.AsksBoss): asks;
//   - its linked PR moved: check;
//   - on a name that is none of o's workers with an agent: waiting, on a person;
//   - on a worker, by its rank: working, waiting (a planned wait or a rate-limit resume), else idle (an agentless
//     session too).
//
// A worker's state is its own (sidebar.Session.Rank): not a boss worker's subtree, nor a WAITING marker in its reply.
// A stuck note isn't blocked: it outlives the stretch it reports until the turn ends.
func (o Owner) Status(it Shown) (status, on string) {
	moved := it.Check != nil && it.Check.Event()
	own := Own(it.On, it.Session, o.Name)
	someone := it.On
	if own {
		someone = ""
	}
	switch {
	case it.Done:
		return "", ""
	case own && it.Started != nil:
		return StatusStarting, it.Started.To
	case o.Closed && moved:
		return StatusCheck, someone
	case o.Closed:
		return StatusBossClosed, someone
	case len(it.Options) > 0 && own:
		return StatusDecide, ""
	case own:
		if moved {
			return StatusCheck, ""
		}
		return StatusYours, ""
	case OnBoss(it.On, it.Session):
		switch {
		case moved:
			return StatusCheck, o.bossName()
		case it.Doing != nil:
			return StatusWorking, o.bossName()
		case Parked(it):
			return StatusParked, o.bossName()
		}
		return StatusIdle, o.bossName()
	case o.Unread:
		return "", ""
	}
	w, worker := o.worker(it)
	switch {
	case !worker && it.Session != nil:
		return StatusGone, it.Session.Name
	case worker && blocked(w):
		return StatusBlocked, w.Name
	case worker && asking(w):
		return StatusAsks, w.Name
	case moved:
		return StatusCheck, cmp.Or(w.Name, it.On)
	case !worker:
		return StatusWaiting, it.On
	}
	switch w.Rank() {
	case sidebar.RankWorking:
		return StatusWorking, w.Name
	case sidebar.RankWaiting:
		return StatusWaiting, w.Name
	}
	return StatusIdle, w.Name
}

// blocked reports worker w waiting on the user (sidebar.Rank: a dialog, a question no running boss answers), or
// resting after a failed turn.
func blocked(w sidebar.Session) bool {
	rank := w.Rank()
	return rank == sidebar.RankNeedsYou || rank == sidebar.RankIdle && sidebar.NoteKind(w.Note) == sidebar.NoteFailed
}

// asking reports worker w's turn ended on a question as text that its running boss answers (sidebar.Session.AsksBoss).
func asking(w sidebar.Session) bool { return w.AsksBoss() }

// Parked reports an item its list's boss does itself (OnBoss) that no one has taken up: no doing line, no start.
func Parked(it Shown) bool {
	return OnBoss(it.On, it.Session) && it.Doing == nil && it.Started == nil
}

// bossName is o's session name, Boss for a desktop app boss's (no name).
func (o Owner) bossName() string { return cmp.Or(o.Name, Boss) }

// OnBoss reports an item its list's boss does itself: on Boss, linked to no session.
func OnBoss(on string, session *Session) bool { return on == Boss && session == nil }

// Box is the item's mark on a list: ☐ open, ◌ waiting on someone else than its boss, ☑ ticked.
func (it Shown) Box() string {
	switch {
	case it.Done:
		return "☑"
	case it.On != "" && !OnBoss(it.On, it.Session):
		return "◌"
	}
	return "☐"
}

// State is the item's Box and who it waits on, as a note about it names them: "☐ on you", "☐ boss's own",
// "◌ on fix-ci", "☑ answered", "☑ done".
func (it Shown) State() string {
	switch {
	case it.Done && it.Answer != "":
		return it.Box() + " answered"
	case it.Done:
		return it.Box() + " done"
	case OnBoss(it.On, it.Session):
		return it.Box() + " boss's own"
	case it.On != "":
		return it.Box() + " on " + it.On
	}
	return it.Box() + " on you"
}

// Own reports an item on no one, or unlinked (no session) on boss itself: an older dev filed --on <own name> as a
// person; its status is the user's, and the user ticks or answers it. On Boss is the boss's, even for a boss named boss.
func Own(on string, session *Session, boss string) bool {
	return on == "" || session == nil && boss != "" && on != Boss && strings.EqualFold(on, boss)
}

// linked is a cut-off just after dev todo add began linking --on to a worker session (v0.57.0): an unlinked item added
// since is a person's, never a worker started later under that name.
var linked = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC).Unix()

// worker is the live worker it is on: its linked session by id (a desktop app session's ID, else a tmux session's
// name), else, for an item added before linked, the one with an agent its on names (Match: the only one by that name).
func (o Owner) worker(it Shown) (sidebar.Session, bool) {
	name := ""
	if it.Session != nil {
		name = it.Session.ID
	} else if it.Added >= linked {
		return sidebar.Session{}, false
	} else {
		var ws []Session
		for _, w := range o.Workers {
			if w.Agent != "" {
				ws = append(ws, Session{ID: cmp.Or(w.ID, w.Name), Name: w.Name})
			}
		}
		m, ok := Match(it.On, ws)
		if !ok {
			return sidebar.Session{}, false
		}
		name = m.ID
	}
	i := slices.IndexFunc(o.Workers, func(w sidebar.Session) bool { return w.ID == name })
	if i < 0 {
		i = slices.IndexFunc(o.Workers, func(w sidebar.Session) bool { return w.Name == name })
	}
	if i < 0 {
		return sidebar.Session{}, false
	}
	return o.Workers[i], true
}

// SetStatus sets each item's Status and StatusOn (Owner.Status), its Placement (Owner.place), and Now for an item on a live worker.
func (l List) SetStatus(o Owner) {
	for i, it := range l.Items {
		l.Items[i].Status, l.Items[i].StatusOn = o.Status(it)
		l.Items[i].Placement = o.place(it)
		l.Items[i].Now, l.Items[i].Note = "", ""
		switch l.Items[i].Status {
		case StatusWorking, StatusWaiting, StatusBlocked, StatusAsks, StatusIdle, StatusCheck:
			if w, ok := o.worker(it); ok && !Own(it.On, it.Session, o.Name) && !OnBoss(it.On, it.Session) {
				l.Items[i].Now, l.Items[i].Note = w.Doing, w.Note
				if o.Doing != nil {
					l.Items[i].Now = o.Doing(w)
				}
			}
		}
	}
}
