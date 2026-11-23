package todo

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// keptFor is how long a ticked item stays among its list's finished items once it left the list.
	keptFor = 24 * time.Hour
	// keptMax is the most finished items a list keeps, the newest.
	keptMax = 50
)

// Finished is a ticked item that left its list (List.Finished): Assignee whose it was when ticked, by its stored shape,
// never running; DoneAt when, unix seconds; DoneBy who ticked it (Item.DoneBy).
type Finished struct {
	ID       int      `json:"id"`
	Text     string   `json:"text"`
	Link     string   `json:"link,omitempty"`
	Answer   string   `json:"answer,omitempty"`
	Context  []string `json:"context,omitempty"`
	Details  string   `json:"details,omitempty"`
	Result   string   `json:"result,omitempty"`
	DoneBy   string   `json:"done_by,omitempty"`
	On       string   `json:"on,omitempty"`
	Assignee Assignee `json:"assignee"`
	DoneAt   int64    `json:"done_at"`
	Quiet    bool     `json:"quiet,omitempty"`
}

// donePath is the file of the list's finished items: beside the lists, not among them, as checksPath is.
func (s Store) donePath() string {
	return filepath.Join(filepath.Dir(s.path), "done", filepath.Base(s.path))
}

// readDone loads the list's finished items as stored; none without a file, nor with one that isn't JSON, so a broken
// history stops no read or write of the list. With aside, its caller holding the list's lock, such a file is moved
// aside to <file>.<unix nanoseconds>.bad, kept.
func (s Store) readDone(aside bool) ([]Item, error) {
	path := s.donePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ignoreMissing(err)
	}
	var items []Item
	if json.Unmarshal(data, &items) != nil {
		if aside {
			return nil, os.Rename(path, path+"."+strconv.FormatInt(time.Now().UnixNano(), 10)+".bad")
		}
		return nil, nil
	}
	return items, nil
}

// keepDone adds items, ticked items its list dropped at now, to the list's finished items; its caller holds the
// list's lock. None left removes the file.
func (s Store) keepDone(items []Item, now time.Time) error {
	old, err := s.readDone(true)
	if err != nil {
		return err
	}
	return s.saveDone(finished(append(old, items...), now))
}

// saveDone writes kept as the list's finished items; its caller holds the list's lock. None left removes the file.
func (s Store) saveDone(kept []Item) error {
	path := s.donePath()
	if len(kept) == 0 {
		return ignoreMissing(os.Remove(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// finished is items ticked within keptFor of now, each once (ids restart: by id, added, done and text), newest first,
// at most keptMax.
func finished(items []Item, now time.Time) []Item {
	items = slices.DeleteFunc(items, func(it Item) bool { return now.Sub(time.Unix(it.Done, 0)) >= keptFor })
	slices.SortFunc(items, func(a, b Item) int {
		return cmp.Or(cmp.Compare(b.Done, a.Done), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Added, b.Added), strings.Compare(a.Text, b.Text))
	})
	items = slices.CompactFunc(items, sameItem)
	return items[:min(len(items), keptMax)]
}

// sameItem reports a and b one item ticked once.
func sameItem(a, b Item) bool {
	return a.ID == b.ID && a.Added == b.Added && a.Done == b.Done && a.Text == b.Text
}

// keepTicked keeps the ticked items still shown, as Set and Clear drop them with the rest at now.
func (f *file) keepTicked(now time.Time) {
	f.pruned = now
	for _, it := range f.Items {
		if it.Done != 0 {
			f.dropped = append(f.dropped, it)
		}
	}
}

// shownFinished is items as boards show them, on boss's list: each assignee as a live card's by its stored shape
// (Owner.stored), read as unread so none is running.
func shownFinished(items []Item, boss string) []Finished {
	o := Owner{Name: boss, Unread: true}
	out := make([]Finished, 0, len(items))
	for _, it := range items {
		by := o.stored(Shown{On: it.On, Session: it.Session}, Own(it.On, it.Session, boss)).Assignee
		out = append(out, Finished{ID: it.ID, Text: it.Text, Link: it.Link, Answer: it.Answer, Context: it.Context, Details: it.Details, Result: it.Result, DoneBy: it.DoneBy, On: it.On, Assignee: by, DoneAt: it.Done, Quiet: it.Quiet})
	}
	return out
}

// Reopen puts the user's ticked item id back on the list as an open item, only while it reads text: one still shown
// (ticked under doneShown ago), else the newest finished one. It keeps its number when no item has it (numbers restart),
// else gets the next one, and its fields but those that would tick it again or no longer hold: a passed Until, DoneOn,
// the answer, its doer, start and other half. It returns the item as reopened. The list is written before the
// finished items, so a failure between keeps it twice, never none.
func (s Store) Reopen(id int, text string, now time.Time) (Item, error) {
	var it Item
	err := s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		if err := s.refuseMoved(f); err != nil {
			return err
		}
		f.prune(now)
		kept, err := s.readDone(true)
		if err != nil {
			return err
		}
		done := finished(append(slices.Clone(kept), f.dropped...), now)
		was := func(x Item) bool { return x.ID == id && x.Done != 0 && x.Text == text }
		i, j := slices.IndexFunc(f.Items, was), slices.IndexFunc(done, was)
		switch {
		case i >= 0:
			it = f.Items[i]
			f.Items = slices.Delete(f.Items, i, i+1)
			// A link its other half stores to it goes too; one to a finished item went as it left (unlinkGone)
			f.unlink(it.ID)
		case j >= 0:
			it = done[j]
		default:
			return Refused{fmt.Errorf("no ticked item %d reading %q in the last day: look at the list again", id, text)}
		}
		if !Own(it.On, it.Session, s.boss) {
			return Refused{fmt.Errorf("item %d was on %s, not the user's to reopen", id, it.On)}
		}
		orig := it
		f.dropped = slices.DeleteFunc(f.dropped, func(x Item) bool { return sameItem(x, orig) })
		f.Ticked = slices.DeleteFunc(f.Ticked, func(t Ticked) bool { return t.ID == orig.ID && t.Text == orig.Text })
		if slices.ContainsFunc(f.Items, func(x Item) bool { return x.ID == it.ID }) {
			it.ID = max(f.Next, 1)
		}
		// A new Added, as a --replaces gives: its checks and age start afresh
		it.Done, it.DoneOn, it.Answer, it.Via, it.Doing, it.Started, it.PartOf, it.Added = 0, "", "", "", nil, nil, 0, max(now.Unix(), it.Added+1)
		it.Asked, it.Dropped = 0, nil
		it.Result, it.DoneBy = "", ""
		if it.Until != 0 && now.Unix() >= it.Until {
			it.Until = 0
		}
		f.Items = append(f.Items, it)
		f.Next = max(f.Next, it.ID+1)
		if s.trial {
			return nil
		}
		f.Updated = now.Unix()
		if err := s.write(f); err != nil {
			return err
		}
		if !slices.ContainsFunc(kept, func(x Item) bool { return sameItem(x, orig) }) {
			return nil
		}
		// write added prune's dropped items to the file: read it again
		if kept, err = s.readDone(true); err == nil {
			err = s.saveDone(slices.DeleteFunc(kept, func(x Item) bool { return sameItem(x, orig) }))
		}
		if err != nil {
			return fmt.Errorf("#%d reopened as #%d, but it still shows in Done: %w", orig.ID, it.ID, err)
		}
		return nil
	})
	return it, err
}
