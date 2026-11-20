package todo

import (
	"cmp"

	"github.com/ppf/dev/internal/sidebar"
)

// Assignee is whose a card is: Kind sidebar.AssigneeYou, AssigneeBoss or AssigneeWorker; Name its boss's or worker's
// session name (Boss for a desktop app boss); Running: a worker while live, the boss while live and read.
type Assignee struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Running bool   `json:"running,omitempty"`
}

// Placement is where an item's card sits (a sidebar column), whose it is, the person it waits for, and whether it
// needs the user (a red mark); set by SetStatus (Owner.place).
type Placement struct {
	Column     string   `json:"column,omitempty"`
	Assignee   Assignee `json:"assignee,omitzero"`
	WaitingFor string   `json:"waiting_for,omitempty"`
	NeedsYou   bool     `json:"needs_you,omitempty"`
}

// place is its placement on o's list, the first that holds (Status's order, with the column's own branches):
//   - ticked: done, whose it was;
//   - started (dev todo start), whoever's: in progress, the list's boss's; a late start needs the user;
//   - its boss closed: review when its PR moved (needing the user when theirs), else waiting when on a person the
//     closed boss no longer follows up, else to do, the user's or the closed boss's (on it or a session);
//   - the user's own: an ask to do, needing the user; in progress while Doing; review when its PR moved, needing the
//     user; else to do;
//   - the boss's own (OnBoss): review when its PR moved, in progress while Doing, else to do;
//   - workers unread: a linked session's in progress, a person's waiting;
//   - a linked session gone: to do, its boss's;
//   - a worker blocked: in progress, needing the user;
//   - its PR moved: review, the worker's, a person's boss's;
//   - on a person: waiting, the boss's;
//   - on a worker: working in progress, a planned wait or rate limit waiting, idle in review while its PR is open,
//     else in progress.
//
// An ask handed to someone goes by whom it is on; a quiet item is placed like any other.
func (o Owner) place(it Shown) Placement {
	own := Own(it.On, it.Session, o.Name)
	onBoss := OnBoss(it.On, it.Session)
	moved := it.Check != nil && it.Check.Event()
	p := o.stored(it, own)
	w, live := o.worker(it)
	if live && !own && !onBoss {
		p = Placement{Assignee: Assignee{Kind: sidebar.AssigneeWorker, Name: w.Name, Running: true}}
	}
	boss := o.boss()
	at := func(column string, needsYou bool) Placement {
		p.Column, p.NeedsYou = column, needsYou
		return p
	}
	switch {
	case it.Done:
		return at(sidebar.ColumnDone, false)
	case it.Started != nil:
		return Placement{Column: sidebar.ColumnProgress, Assignee: boss, NeedsYou: it.StartLate}
	case o.Closed:
		if p.Assignee.Kind == sidebar.AssigneeWorker {
			p.Assignee = boss
		}
		switch {
		case moved:
			return at(sidebar.ColumnReview, own)
		case p.WaitingFor != "":
			return at(sidebar.ColumnWaiting, false)
		}
		return at(sidebar.ColumnTodo, false)
	case own:
		switch {
		case len(it.Options) > 0:
			return at(sidebar.ColumnTodo, true)
		case it.Doing != nil:
			return at(sidebar.ColumnProgress, false)
		case moved:
			return at(sidebar.ColumnReview, true)
		}
		return at(sidebar.ColumnTodo, false)
	case onBoss:
		switch {
		case moved:
			return at(sidebar.ColumnReview, false)
		case it.Doing != nil:
			return at(sidebar.ColumnProgress, false)
		}
		return at(sidebar.ColumnTodo, false)
	case o.Unread:
		if it.Session != nil {
			return at(sidebar.ColumnProgress, false)
		}
		return at(sidebar.ColumnWaiting, false)
	case !live && it.Session != nil:
		return Placement{Column: sidebar.ColumnTodo, Assignee: boss}
	case live && blocked(w):
		return at(sidebar.ColumnProgress, true)
	case moved:
		return at(sidebar.ColumnReview, false)
	case !live:
		return at(sidebar.ColumnWaiting, false)
	}
	switch w.Rank() {
	case sidebar.RankWorking:
		return at(sidebar.ColumnProgress, false)
	case sidebar.RankWaiting:
		return at(sidebar.ColumnWaiting, false)
	}
	if it.PRState == PROpen {
		return at(sidebar.ColumnReview, false)
	}
	return at(sidebar.ColumnProgress, false)
}

// stored is whose it is by its stored shape alone, unless a live worker takes it: the user's own, the boss's
// (OnBoss), its linked session's, else the boss's, waiting for the person it's on.
func (o Owner) stored(it Shown, own bool) Placement {
	switch {
	case own:
		return Placement{Assignee: Assignee{Kind: sidebar.AssigneeYou}}
	case OnBoss(it.On, it.Session):
		return Placement{Assignee: o.boss()}
	case it.Session != nil:
		return Placement{Assignee: Assignee{Kind: sidebar.AssigneeWorker, Name: it.Session.Name}}
	}
	return Placement{Assignee: o.boss(), WaitingFor: it.On}
}

// boss is o as an assignee, running while neither closed nor unread (not among the live sessions, or those unread).
func (o Owner) boss() Assignee {
	return Assignee{Kind: sidebar.AssigneeBoss, Name: o.bossName(), Running: !o.Closed && !o.Unread}
}

// where is its column's title and whom it names, as a refusal says it: "in Waiting (Marek)".
func (p Placement) where() string {
	who := cmp.Or(p.WaitingFor, p.Assignee.Name)
	if who == "" {
		return "in " + sidebar.ColumnTitles[p.Column]
	}
	return "in " + sidebar.ColumnTitles[p.Column] + " (" + who + ")"
}
