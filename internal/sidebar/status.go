package sidebar

import "slices"

// An open to do item's status (todo.Owner.Status, which sets it; internal/todo imports this package): the one word
// both boards group and mark it by.
const (
	StatusDecide     = "decide"      // an ask (dev todo ask): the user picks an option
	StatusYours      = "yours"       // on no one: the user's own
	StatusStarting   = "starting"    // the user's own, asked to start (dev todo start) and not yet on a worker
	StatusWorking    = "working"     // on a worker that works
	StatusBlocked    = "blocked"     // on a worker that waits on the user: a dialog, a turn ended on a question, or failed
	StatusWaiting    = "waiting"     // on a person, or on a worker in a planned wait or a rate-limit resume
	StatusIdle       = "idle"        // on a worker that rests, or a session it started with no agent
	StatusGone       = "gone"        // on a worker session no longer listed
	StatusCheck      = "check"       // its linked PR moved (merged, closed, approved, …): see its check
	StatusBossClosed = "boss closed" // its boss session is gone
)

// Statuses is the order boards list items by status within a group (Groups).
var Statuses = []string{StatusDecide, StatusYours, StatusStarting, StatusWorking, StatusBlocked, StatusWaiting, StatusIdle, StatusGone,
	StatusCheck, StatusBossClosed}

// StatusGroup is a block of the boards' TO DO: its title and the statuses it holds, in Statuses order.
type StatusGroup struct {
	Title    string
	Statuses []string
}

// Groups are TO DO's groups in order: each by its first status in Statuses, In progress holding a worker's every
// state but waiting, which Waiting on others shares with a person's.
var Groups = []StatusGroup{
	{"To decide", []string{StatusDecide}},
	{"Yours", []string{StatusYours}},
	{"Starting", []string{StatusStarting}},
	{"In progress", []string{StatusWorking, StatusBlocked, StatusIdle, StatusGone}},
	{"Waiting on others", []string{StatusWaiting}},
	{"Check", []string{StatusCheck}},
	{"Boss closed", []string{StatusBossClosed}},
}

// GroupOf is status's index in Groups; -1 for none (a ticked item, or one on someone whose workers weren't read).
func GroupOf(status string) int {
	return slices.IndexFunc(Groups, func(g StatusGroup) bool { return slices.Contains(g.Statuses, status) })
}
