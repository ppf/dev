package sidebar

import "slices"

// An open to do item's status (todo.Owner.Status, which sets it; internal/todo imports this package): the one word
// both boards group and mark it by.
const (
	StatusDecide     = "decide"      // an ask (dev todo ask): the user picks an option
	StatusYours      = "yours"       // on no one: the user's own
	StatusStarting   = "starting"    // the user's own, asked to start (dev todo start) and not yet on a worker
	StatusWorking    = "working"     // on a worker that works
	StatusBlocked    = "blocked"     // on a worker that waits on the user: a dialog, a question no running boss answers, or a failed turn
	StatusAsks       = "asks"        // on a tmux worker whose turn ended on a question as text its running boss answers (StatusWord)
	StatusWaiting    = "waiting"     // on a person, or on a worker in a planned wait or a rate-limit resume
	StatusIdle       = "idle"        // on a worker that rests, or a session it started with no agent
	StatusParked     = "parked"      // the boss's own (--on boss) that no one has taken up: no doing line, no start
	StatusGone       = "gone"        // on a worker session no longer listed
	StatusCheck      = "check"       // its linked PR moved (merged, closed, approved, …): see its check
	StatusBossClosed = "boss closed" // its boss session is gone
)

// Statuses is the order boards list items by status within a group (Groups).
var Statuses = []string{StatusDecide, StatusYours, StatusStarting, StatusParked, StatusWorking, StatusBlocked, StatusAsks, StatusWaiting,
	StatusIdle, StatusGone, StatusCheck, StatusBossClosed}

// StatusWord is how the boards say status: asks as "asks its boss", the others as they are.
func StatusWord(status string) string {
	if status == StatusAsks {
		return "asks its boss"
	}
	return status
}

// ParkedLine is what a parked item says where its now line goes, its boss named.
func ParkedLine(boss string) string { return boss + " has not taken it up" }

// StatusColours are the boards' colours of each status word, the cards' section colours: working gold, waiting
// green, idle muted.
var StatusColours = map[string]string{
	StatusDecide:     Red,
	StatusYours:      "",
	StatusStarting:   Gold,
	StatusWorking:    Gold,
	StatusBlocked:    Red,
	StatusAsks:       Gold,
	StatusWaiting:    Green,
	StatusIdle:       Muted,
	StatusParked:     Muted,
	StatusGone:       Red,
	StatusCheck:      Gold,
	StatusBossClosed: Muted,
}

// StatusGroup is a block of the boards' TO DO: its title and the statuses it holds, in Statuses order.
type StatusGroup struct {
	Title    string
	Statuses []string
}

// Groups are TO DO's groups in order: each by its first status in Statuses, To do holding the boss's parked items (its
// cards' column), In progress a worker's every state but waiting, which Waiting on others shares with a person's.
var Groups = []StatusGroup{
	{"To decide", []string{StatusDecide}},
	{"Yours", []string{StatusYours}},
	{"Starting", []string{StatusStarting}},
	{"To do", []string{StatusParked}},
	{"In progress", []string{StatusWorking, StatusBlocked, StatusAsks, StatusIdle, StatusGone}},
	{"Waiting on others", []string{StatusWaiting}},
	{"Check", []string{StatusCheck}},
	{"Boss closed", []string{StatusBossClosed}},
}

// GroupOf is status's index in Groups; -1 for none (a ticked item, or one on someone whose workers weren't read).
func GroupOf(status string) int {
	return slices.IndexFunc(Groups, func(g StatusGroup) bool { return slices.Contains(g.Statuses, status) })
}

// A to do item's column on the boards, where its card sits (todo.Owner.place sets it), in board order (Columns).
const (
	ColumnTodo     = "todo"
	ColumnProgress = "progress"
	ColumnWaiting  = "waiting"
	ColumnReview   = "review"
	ColumnDone     = "done"
)

// Columns is the boards' column order; ColumnTitles their titles.
var Columns = []string{ColumnTodo, ColumnProgress, ColumnWaiting, ColumnReview, ColumnDone}

var ColumnTitles = map[string]string{
	ColumnTodo:     "To do",
	ColumnProgress: "In progress",
	ColumnWaiting:  "Waiting",
	ColumnReview:   "Review",
	ColumnDone:     "Done",
}

// Whose a card is (todo.Assignee.Kind): the user's, its list's boss's, or a worker's.
const (
	AssigneeYou    = "you"
	AssigneeBoss   = "boss"
	AssigneeWorker = "worker"
)
