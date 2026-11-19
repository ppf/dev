package sidebar

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/ppf/dev/internal/agent"
)

// Note kinds as inbox writes them in @dev_boss_note, "<kind>: <text>" (inbox imports this package; herd's tests pin them).
const (
	NoteFinished = "finished"
	NoteAsks     = "asks"
	NoteDecision = "needs a decision"
	NoteFailed   = "failed"
	NoteStuck    = "stuck"
	NoteContext  = "high context"
)

// NoteKind is a note's kind, "<kind>: <text>" or "<kind>".
func NoteKind(note string) string {
	kind, _, _ := strings.Cut(note, ":")
	return kind
}

// HealthNote reports a note kind flagged with ⚠: something went wrong, or will.
func HealthNote(kind string) bool {
	return kind == NoteFailed || kind == NoteStuck || kind == NoteContext
}

// Rank is a session's or a subtree's state on a board, strongest first: the boss board's section order.
type Rank int

const (
	RankNeedsYou Rank = iota
	RankWorking
	RankWaiting
	RankIdle
)

// Rank is s's own state, the first that holds: a dialog, a planned wait, working, a rate-limit resume, a turn
// that ended on a question (an asks note; a needs a decision note outlives its dialog), else idle.
func (s Session) Rank() Rank {
	switch {
	case s.Deciding:
		return RankNeedsYou
	case s.Wait.Held():
		return RankWaiting
	case s.State == agent.Working:
		return RankWorking
	case s.Resume:
		return RankWaiting
	case NoteKind(s.Note) == NoteAsks:
		return RankNeedsYou
	}
	return RankIdle
}

// ListCount is what a boss's to do list adds to the roll-ups, by its items' statuses: asks to decide, the user's own
// plain items, and those on someone else; a closed boss's items and a moved PR's count in none, as TO DO's header has it.
type ListCount struct {
	Asks, Plain, On int
	Unread          bool // the list wasn't read
	Closed          bool // its boss session is gone: the manager's roll-up takes it (its items count in none, as TO DO's header has it)
}

// Add counts an open item by its status: none (on someone whose workers weren't read) is on someone.
func (c *ListCount) Add(done bool, status string) {
	switch {
	case done, status == StatusCheck, status == StatusBossClosed:
	case status == StatusDecide:
		c.Asks++
	case status == StatusYours:
		c.Plain++
	default:
		c.On++
	}
}

// Rollup is a boss's whole subtree: the strongest Rank across it and every session under it, and its counts.
type Rollup struct {
	Rank      Rank
	Questions int  // sessions that need the user: dialogs and asks notes, its own too
	Decide    int  // asks on its and its bosses' lists
	Working   int  // sessions under it working
	Waiting   int  // sessions under it in a planned wait or rate-limit resume
	ToDo      int  // plain items on its and its bosses' lists
	OnOthers  int  // open items on a worker or a person on its and its bosses' lists
	Under     int  // bosses and sessions with an agent under it: a session with no agent isn't a worker, as the board counts
	Bosses    int  // a manager's: the bosses in its group, at its Depth (Order)
	Warn      bool // a failed, stuck or high-context note, its own too, or a list not read
	CostUSD   float64
	HasCost   bool

	// ClosedUSD is the part of CostUSD that ended under it: the SpentUSD of it and the sessions under it
	ClosedUSD float64
	// CostErr is a SpentErr of it or a session under it: CostUSD misses that ledger, and Warn is set
	CostErr string
}

// Subtree is each boss's Rollup by name, from sessions in Order (a boss's sessions follow it at a greater Depth,
// a manager's group under its Root) and the bosses' lists by name: the boss board's tiles and the sidebar's
// roll-up lines read the same.
func Subtree(sessions []Session, lists map[string]ListCount) map[string]*Rollup {
	out := map[string]*Rollup{}
	for i, s := range sessions {
		if !s.Boss {
			continue
		}
		manager := s.Root != "" && s.Root == s.Name
		r := &Rollup{Rank: RankIdle}
		r.add(s, lists, true)
		for _, u := range under(sessions, i) {
			if u.Agent != "" || u.Boss {
				if manager && u.Boss && u.Depth == s.Depth {
					r.Bosses++
				}
				r.Under++
			}
			r.add(u, lists, false)
			r.ClosedUSD += u.SpentUSD
			r.CostErr = cmp.Or(r.CostErr, u.SpentErr)
		}
		if s.Name == Manager {
			for _, l := range lists {
				if l.Closed {
					r.addList(l)
				}
			}
		}
		r.ClosedUSD += s.SpentUSD
		if r.ClosedUSD > 0 {
			r.CostUSD, r.HasCost = r.CostUSD+r.ClosedUSD, true
		}
		// A total missing a ledger would read as whole: none shows, ⚠ says why
		if r.CostErr = cmp.Or(s.SpentErr, r.CostErr); r.CostErr != "" {
			r.Warn, r.HasCost = true, false
		}
		out[s.Name] = r
	}
	return out
}

// under is the sessions under sessions[i] in Order: a manager's whole group (its Root), else those after it at a
// greater Depth.
func under(sessions []Session, i int) []Session {
	s := sessions[i]
	manager := s.Root != "" && s.Root == s.Name
	end := i + 1
	for ; end < len(sessions); end++ {
		if u := sessions[end]; manager && u.Root != s.Root || !manager && u.Depth <= s.Depth {
			break
		}
	}
	return sessions[i+1 : end]
}

// add counts s into r, the boss itself when own: its working or waiting isn't counted, only ranked.
func (r *Rollup) add(s Session, lists map[string]ListCount, own bool) {
	rank := s.Rank()
	r.Rank = min(r.Rank, rank)
	switch {
	case rank == RankNeedsYou:
		r.Questions++
	case own:
	case rank == RankWorking:
		r.Working++
	case rank == RankWaiting:
		r.Waiting++
	}
	r.Warn = r.Warn || HealthNote(NoteKind(s.Note))
	if s.HasCost {
		r.CostUSD, r.HasCost = r.CostUSD+s.CostUSD, true
	}
	if !s.Boss {
		return
	}
	r.addList(lists[s.Name])
}

// addList counts a boss's list l into r.
func (r *Rollup) addList(l ListCount) {
	r.Decide += l.Asks
	r.ToDo += l.Plain
	r.OnOthers += l.On
	if l.Asks > 0 {
		r.Rank = RankNeedsYou
	}
	r.Warn = r.Warn || l.Unread
}

// Parts is the roll-up as its line's parts: the state, then the counts, strongest first, each only when non-zero;
// a working or waiting state is said by its count when it has one, needing the user by "N need you" (sessions) and
// "N decide" (asks). "· N to do · N on others" come last.
func (r Rollup) Parts() []string {
	var ps []string
	count := func(n int, what string) {
		if n > 0 {
			ps = append(ps, fmt.Sprintf("%d %s", n, what))
		}
	}
	switch r.Rank {
	case RankNeedsYou:
		count(r.Questions, NeedYouWord)
		count(r.Decide, DecideWord)
	case RankWorking:
		if r.Working == 0 {
			ps = append(ps, "working")
		}
	case RankWaiting:
		if r.Waiting == 0 {
			ps = append(ps, "waiting")
		}
	default:
		ps = append(ps, "idle")
		if r.Under == 0 {
			ps = append(ps, "no workers")
		}
	}
	if r.Rank <= RankWorking {
		count(r.Working, "working")
	}
	if r.Rank <= RankWaiting {
		count(r.Waiting, "waiting")
	}
	count(r.ToDo, ToDoWord)
	count(r.OnOthers, "on others")
	return ps
}

// The words of the counts the user acts on, alike on the roll lines and the sidebar's TO DO: "need you" only for
// sessions waiting on the user, "decide" for asks, "to do" for the user's own items.
const (
	NeedYouWord = "need you"
	DecideWord  = "decide"
	ToDoWord    = "to do"
)

// Text is Parts as one line.
func (r Rollup) Text() string { return strings.Join(r.Parts(), " · ") }
