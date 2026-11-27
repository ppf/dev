package todo

import (
	"slices"
	"time"
)

// SessionMark is a boss's current session as its last-session file needs it; Ticked keeps ticked items the list
// dropped (finished items last only KeptFor), Carry the items the last file left open.
type SessionMark struct {
	Start   int64  `json:"start,omitempty"`
	Open    []int  `json:"open,omitempty"`
	Ticked  []Item `json:"ticked,omitempty"`
	Written bool   `json:"written,omitempty"`
	Carry   []int  `json:"carry,omitempty"`
}

// SessionFacts is what a list holds of one session of its boss, from Start to End (unix seconds).
type SessionFacts struct {
	Start, End int64
	Added      []Item // open items added (or numbered anew) in the session
	Ticked     []Item // items ticked in the session, by when
	Asks       []Item // open asks to the user
	Notes      []Note // notes added in the session
	State      *Note  // the boss's state note, whenever written
	Left       []Item // the previous file's items still open
}

// EndSession, under the list's lock, hands write the facts of the session its mark holds unless a file covers it
// already, then marks it written; with no mark, from (unix seconds, 0 for none) is the session's start. With next, a
// new session starts at now, also when nothing was written. A failed write marks and starts nothing.
func (s Store) EndSession(now time.Time, from int64, next bool, write func(SessionFacts) error) (wrote bool, err error) {
	err = s.locked(func() error {
		f, err := s.read()
		if err != nil || f.MovedTo != "" {
			return err
		}
		f.prune(now)
		m := f.Session
		if m == nil && from != 0 {
			m = &SessionMark{Start: from}
		}
		if m != nil && !m.Written {
			facts, err := s.sessionFacts(f, *m, now)
			if err != nil {
				return err
			}
			if err := write(facts); err != nil {
				return err
			}
			wrote = true
			m.Written, m.Carry, m.Ticked = true, carry(facts), nil
			f.Session = m
		}
		if next {
			var open []int
			for _, it := range f.Items {
				if it.Done == 0 {
					open = append(open, it.ID)
				}
			}
			f.Session = &SessionMark{Start: now.Unix(), Open: open}
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

// Unwritten reports whether the list holds a session no last-session file covers yet.
func (s Store) Unwritten() (bool, error) {
	f, err := s.read()
	if err != nil {
		return false, err
	}
	return f.unwritten(), nil
}

// unwritten reports whether f holds a session no last-session file covers yet.
func (f file) unwritten() bool {
	return f.MovedTo == "" && f.Session != nil && !f.Session.Written
}

// sessionFacts is what f, pruned, holds of session m up to now; its caller holds the list's lock.
func (s Store) sessionFacts(f file, m SessionMark, now time.Time) (SessionFacts, error) {
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
	slices.SortStableFunc(facts.Ticked, func(a, b Item) int { return int(a.Done - b.Done) })
	for _, it := range f.Items {
		switch {
		case it.Done != 0 || it.Quiet:
		case len(it.Options) > 0:
			facts.Asks = append(facts.Asks, it)
		case !slices.Contains(m.Open, it.ID):
			facts.Added = append(facts.Added, it)
		case slices.Contains(m.Carry, it.ID):
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
func carry(facts SessionFacts) []int {
	var ids []int
	for _, it := range slices.Concat(facts.Left, facts.Added) {
		ids = append(ids, it.ID)
	}
	return ids
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
