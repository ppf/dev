package todo

import (
	"cmp"
	"errors"
	"slices"
	"time"
)

// sessionMark is a boss's current session as its last-session file needs it; Ticked keeps ticked items the list
// dropped (finished items last only KeptFor), Carry the items the last file left open.
type sessionMark struct {
	Start   int64     `json:"start,omitempty"`
	Ticked  []Item    `json:"ticked,omitempty"`
	Written bool      `json:"written,omitempty"`
	Carry   []carried `json:"carry,omitempty"`
}

// carried is an item a last-session file left open, by its number and added time: numbers restart once a list
// empties, and a --replaces moves the time.
type carried struct {
	ID    int   `json:"id"`
	Added int64 `json:"added"`
}

// SessionFacts is what a list holds of one session of its boss, from Start to End (unix seconds): End is when the write
// ran, for a session gone without a close when that was found.
type SessionFacts struct {
	Start, End int64
	Added      []Item // open items added or replaced in the session
	Ticked     []Item // items ticked in the session, by when
	Asks       []Item // open asks to the user
	Notes      []Note // notes added in the session
	State      *Note  // the boss's state note, whenever written
	Left       []Item // the previous file's items still open, unchanged
}

// EndSession, under the list's lock, hands write the facts of the session its mark holds unless a file covers it
// already, then marks it written; with no mark, from (unix seconds, 0 for none) is the session's start; with only
// (unix seconds), only a session that started then is written. With next, a new session starts at now, also when
// nothing was written. A failed write marks and starts nothing.
func (s Store) EndSession(now time.Time, from, only int64, next bool, write func(SessionFacts) error) (wrote bool, err error) {
	err = s.locked(func() error {
		f, err := s.read()
		if err != nil || f.MovedTo != "" {
			return err
		}
		f.prune(now)
		m := f.Session
		if m == nil && from != 0 {
			m = &sessionMark{Start: from}
		}
		if m != nil && !m.Written && (only == 0 || m.Start == only) {
			facts, err := s.sessionFacts(f, *m, now)
			if err == nil {
				err = write(facts)
			}
			if err != nil && f.Session == nil {
				// A session from from is kept unwritten, for a later start or the manager's to write
				f.Session = m
				return errors.Join(err, s.write(f))
			}
			if err != nil {
				return err
			}
			// Ticked stays until the next session: a close whose kill fails (Unend) writes it again
			wrote = true
			m.Written, m.Carry = true, carry(facts)
			f.Session = m
		}
		if next {
			f.Session = &sessionMark{Start: now.Unix()}
			if m != nil {
				f.Session.Carry = m.Carry
			}
		}
		if !wrote && !next {
			return nil
		}
		// The list's Updated stays: nothing on it changed
		return s.write(f)
	})
	return wrote, err
}

// Unend marks the session that started at start (unix seconds) not written again, as its boss's close failed after
// its file was written: its later close writes it anew.
func (s Store) Unend(start int64) error {
	return s.locked(func() error {
		f, err := s.read()
		if err != nil || f.Session == nil || f.Session.Start != start || !f.Session.Written {
			return err
		}
		f.Session.Written = false
		return s.write(f)
	})
}

// SessionStart is when the list's session started (unix seconds, 0 for none) and whether no file covers it yet.
func (s Store) SessionStart() (start int64, unwritten bool, err error) {
	f, err := s.read()
	if err != nil || f.Session == nil {
		return 0, false, err
	}
	return f.Session.Start, f.unwritten(), nil
}

// unwritten reports whether f holds a session no last-session file covers yet.
func (f file) unwritten() bool {
	return f.MovedTo == "" && f.Session != nil && !f.Session.Written
}

// sessionFacts is what f, pruned, holds of session m up to now; its caller holds the list's lock.
func (s Store) sessionFacts(f file, m sessionMark, now time.Time) (SessionFacts, error) {
	facts := SessionFacts{Start: m.Start, End: now.Unix()}
	done, err := s.readDone(false)
	if err != nil {
		return facts, err
	}
	in := func(t int64) bool { return t >= m.Start && t <= facts.End }
	for _, it := range slices.Concat(m.Ticked, f.dropped, done, f.Items) {
		dup := func(o Item) bool { return o.ID == it.ID && o.Done == it.Done }
		if it.Done != 0 && in(it.Done) && !it.Quiet && !slices.ContainsFunc(facts.Ticked, dup) {
			facts.Ticked = append(facts.Ticked, it)
		}
	}
	slices.SortStableFunc(facts.Ticked, func(a, b Item) int { return cmp.Compare(a.Done, b.Done) })
	for _, it := range f.Items {
		switch {
		case it.Done != 0 || it.Quiet:
		case len(it.Options) > 0:
			facts.Asks = append(facts.Asks, it)
		case it.Added >= m.Start:
			facts.Added = append(facts.Added, it)
		case slices.Contains(m.Carry, carried{it.ID, it.Added}):
			facts.Left = append(facts.Left, it)
		}
	}
	for _, n := range f.Notes {
		if n.Kind == NoteState {
			facts.State = &n
		} else if in(n.Added) {
			facts.Notes = append(facts.Notes, n)
		}
	}
	return facts, nil
}

// carry is the items facts' file leaves open for the next one: those added and those left from before.
func carry(facts SessionFacts) []carried {
	var c []carried
	for _, it := range slices.Concat(facts.Left, facts.Added) {
		c = append(c, carried{it.ID, it.Added})
	}
	return c
}

// keepSessionTicked adds to f's session the items its list dropped that were ticked in it; its caller holds the lock.
func (f *file) keepSessionTicked() {
	if f.Session == nil || f.Session.Written {
		return
	}
	for _, it := range f.dropped {
		if it.Done >= f.Session.Start && !it.Quiet {
			f.Session.Ticked = append(f.Session.Ticked, Item{ID: it.ID, Text: it.Text, Link: it.Link, Result: it.Result,
				DoneBy: it.DoneBy, Answer: it.Answer, Done: it.Done})
		}
	}
}
