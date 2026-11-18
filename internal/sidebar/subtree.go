package sidebar

import (
	"fmt"
	"strings"

	"github.com/ppf/dev/internal/agent"
)

// Note kinds as inbox writes them in @dev_boss_note, "<kind>: <text>" (inbox imports this package; herd's tests pin them).
const (
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

// Rank is s's own state, the first that holds: a dialog, a planned wait or rate-limit resume, working, a turn
// that ended on a question (an asks note; a needs a decision note outlives its dialog), else idle.
func (s Session) Rank() Rank {
	switch {
	case s.Deciding:
		return RankNeedsYou
	case s.Wait.Held() || s.Resume:
		return RankWaiting
	case s.State == agent.Working:
		return RankWorking
	case NoteKind(s.Note) == NoteAsks:
		return RankNeedsYou
	}
	return RankIdle
}

// ListCount is what a boss's to do list adds to the roll-ups: its open items on no one, asks (with options) apart.
type ListCount struct {
	Asks, Plain int
	Unread      bool // the list wasn't read
}

// Add counts an item when it's open and on no one.
func (c *ListCount) Add(done bool, on string, options int) {
	switch {
	case done || on != "":
	case options > 0:
		c.Asks++
	default:
		c.Plain++
	}
}

// Rollup is a boss's whole subtree: the strongest Rank across it and every session under it, and its counts.
type Rollup struct {
	Rank      Rank
	Idle      string // the idle state's word: its own note's kind ("finished"), else "idle"
	Questions int    // dialogs and asks notes, its own too, and asks on its and its bosses' lists
	Working   int    // sessions under it working
	Waiting   int    // sessions under it in a planned wait or rate-limit resume
	ToDo      int    // plain items on its and its bosses' lists
	Under     int    // sessions under it
	Warn      bool   // a failed, stuck or high-context note, its own too, or a list not read
	CostUSD   float64
	HasCost   bool
}

// Subtree is each boss's Rollup by name, from sessions in Order (a boss's sessions follow it at a greater Depth)
// and the bosses' lists by name: the boss board's tiles and the sidebar's roll-up lines read the same.
func Subtree(sessions []Session, lists map[string]ListCount) map[string]*Rollup {
	out := map[string]*Rollup{}
	for i, s := range sessions {
		if !s.Boss {
			continue
		}
		r := &Rollup{Rank: RankIdle, Idle: "idle"}
		if k := NoteKind(s.Note); s.Note != "" && !HealthNote(k) && k != NoteDecision && k != NoteAsks {
			r.Idle = k
		}
		r.add(s, lists, true)
		for _, u := range sessions[i+1:] {
			if u.Depth <= s.Depth {
				break
			}
			r.Under++
			r.add(u, lists, false)
		}
		out[s.Name] = r
	}
	return out
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
	if HealthNote(NoteKind(s.Note)) {
		r.Warn = true
	}
	if s.HasCost {
		r.CostUSD, r.HasCost = r.CostUSD+s.CostUSD, true
	}
	if !s.Boss {
		return
	}
	l := lists[s.Name]
	r.Questions += l.Asks
	r.ToDo += l.Plain
	if l.Asks > 0 {
		r.Rank = RankNeedsYou
	}
	r.Warn = r.Warn || l.Unread
}

// Parts is the roll-up as its line's parts: the state, then the counts, strongest first, each only when non-zero;
// a working or waiting state is said by its count when it has one. "· N to do" comes last.
func (r Rollup) Parts() []string {
	var ps []string
	count := func(n int, what string) {
		if n > 0 {
			ps = append(ps, fmt.Sprintf("%d %s", n, what))
		}
	}
	switch r.Rank {
	case RankNeedsYou:
		ps = append(ps, "needs you")
		if r.Questions == 1 {
			ps = append(ps, "1 question")
		} else {
			count(r.Questions, "questions")
		}
	case RankWorking:
		if r.Working == 0 {
			ps = append(ps, "working")
		}
	case RankWaiting:
		if r.Waiting == 0 {
			ps = append(ps, "waiting")
		}
	default:
		ps = append(ps, r.Idle)
		if r.Under == 0 {
			ps = append(ps, "nothing under it")
		}
	}
	if r.Rank <= RankWorking {
		count(r.Working, "working")
	}
	if r.Rank <= RankWaiting {
		count(r.Waiting, "waiting")
	}
	count(r.ToDo, "to do")
	return ps
}

// Text is Parts as one line.
func (r Rollup) Text() string { return strings.Join(r.Parts(), " · ") }
