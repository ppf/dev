package todo

import (
	"cmp"
	"slices"
	"strings"

	"github.com/ppf/dev/internal/sidebar"
)

// An open item's status: the one word both boards group and mark it by.
const (
	StatusDecide     = "decide"      // an ask (dev todo ask): the user picks an option
	StatusYours      = "yours"       // on no one: the user's own
	StatusWorking    = "working"     // on a worker that works
	StatusBlocked    = "blocked"     // on a worker that waits on the user: a dialog, a turn ended on a question, or failed
	StatusWaiting    = "waiting"     // on a person, or on a worker in a planned wait or a rate-limit resume
	StatusIdle       = "idle"        // on a worker that rests, or a session it started with no agent
	StatusGone       = "gone"        // on a worker session no longer listed
	StatusCheck      = "check"       // its linked PR moved (merged, closed, approved, …): see its check
	StatusBossClosed = "boss closed" // its boss session is gone
)

// Statuses is the order boards group items by status.
var Statuses = []string{StatusDecide, StatusYours, StatusWorking, StatusBlocked, StatusWaiting, StatusIdle, StatusGone,
	StatusCheck, StatusBossClosed}

// Owner is a list's boss as statuses read it: Name its session's, Closed when it's gone, Workers the live sessions it
// started (sidebar.Live, or a desktop boss's workers as sessions); Unread when those weren't read, so an item on someone has no status.
type Owner struct {
	Name    string
	Closed  bool
	Workers []sidebar.Session
	Unread  bool
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
//   - its boss closed: check when its linked PR moved (sidebar.Check.Event), else boss closed, naming whom it's on;
//   - on no one, or unlinked on the list's own boss (an older dev filed --on <own name> as a person): decide for an
//     ask, else check when its PR moved, else yours; an ask handed to someone goes by whom it's on, as below;
//   - on a linked session o didn't start, or no longer listed: gone;
//   - on a worker asking the user (sidebar.Rank: a dialog, a turn ended on a question), or resting after a failed
//     turn: blocked;
//   - its linked PR moved: check;
//   - on a name that is none of o's workers with an agent: waiting, on a person;
//   - on a worker, by its rank: working, waiting (a planned wait or a rate-limit resume), else idle (an agentless
//     session too).
//
// A worker's state is its own (sidebar.Session.Rank): not a boss worker's subtree, nor a WAITING marker in its reply.
// A stuck note isn't blocked: it outlives the stretch it reports until the turn ends.
func (o Owner) Status(it Shown) (status, on string) {
	moved := it.Check != nil && it.Check.Event()
	own := it.On == "" || it.Session == nil && o.Name != "" && strings.EqualFold(it.On, o.Name)
	someone := it.On
	if own {
		someone = ""
	}
	switch {
	case it.Done:
		return "", ""
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
	case o.Unread:
		return "", ""
	}
	w, worker := o.worker(it)
	rank := w.Rank()
	switch {
	case !worker && it.Session != nil:
		return StatusGone, it.Session.Name
	case worker && (rank == sidebar.RankNeedsYou || rank == sidebar.RankIdle && sidebar.NoteKind(w.Note) == sidebar.NoteFailed):
		return StatusBlocked, w.Name
	case moved:
		return StatusCheck, cmp.Or(w.Name, it.On)
	case !worker:
		return StatusWaiting, it.On
	}
	switch rank {
	case sidebar.RankWorking:
		return StatusWorking, w.Name
	case sidebar.RankWaiting:
		return StatusWaiting, w.Name
	}
	return StatusIdle, w.Name
}

// worker is the live worker it is on: its linked session by id (a desktop app session's ID, else a tmux session's
// name), else the one with an agent its on names (Match: the only one by that name), as the previews' cards are.
func (o Owner) worker(it Shown) (sidebar.Session, bool) {
	name := ""
	if it.Session != nil {
		name = it.Session.ID
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

// SetStatus sets each item's Status and StatusOn (Owner.Status).
func (l List) SetStatus(o Owner) {
	for i, it := range l.Items {
		l.Items[i].Status, l.Items[i].StatusOn = o.Status(it)
	}
}
