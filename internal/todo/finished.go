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

	"github.com/ppf/dev/internal/sidebar"
)

const (
	// KeptFor is how long a ticked item stays among its list's finished items once it left the list: Reopen finds it
	// that long.
	KeptFor = 24 * time.Hour
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
	Options  []string `json:"options,omitempty"`
	Context  []string `json:"context,omitempty"`
	Details  string   `json:"details,omitempty"`
	Result   string   `json:"result,omitempty"`
	DoneBy   string   `json:"done_by,omitempty"`
	On       string   `json:"on,omitempty"`
	Assignee Assignee `json:"assignee"`
	DoneAt   int64    `json:"done_at"`
	Quiet    bool     `json:"quiet,omitempty"`
	Decided  *Decided `json:"decided,omitempty"`
}

// Shown is f as the boards show a ticked card: in Done, its assignee as stored.
func (f Finished) Shown() Shown {
	return Shown{ID: f.ID, Text: f.Text, Link: f.Link, Answer: f.Answer, Options: f.Options, On: f.On, Quiet: f.Quiet, Done: true,
		Context: f.Context, Details: f.Details, Result: f.Result, DoneBy: f.DoneBy, Decided: f.Decided,
		Placement: Placement{Column: sidebar.ColumnDone, Assignee: f.Assignee}}
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

// finished is items ticked within KeptFor of now, each once (ids restart: by id, added, done and text), newest first,
// at most keptMax.
func finished(items []Item, now time.Time) []Item {
	items = slices.DeleteFunc(items, func(it Item) bool { return now.Sub(time.Unix(it.Done, 0)) >= KeptFor })
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
		out = append(out, Finished{ID: it.ID, Text: it.Text, Link: it.Link, Answer: it.Answer, Options: it.Options, Context: it.Context, Details: it.Details, Result: it.Result, DoneBy: it.DoneBy, On: it.On, Assignee: by, DoneAt: it.Done, Quiet: it.Quiet, Decided: it.Decided})
	}
	return out
}

// Reopen puts the user's ticked item id back on the list as an open item, only while it reads text: one still shown
// (ticked under doneShown ago), else the newest finished one. It keeps its number when no item has it (numbers restart),
// else gets the next one, and its fields but those that would tick it again or no longer hold: a passed Until, DoneOn,
// the answer, its doer, start and other half. It returns the item as reopened. The list is written before the
// finished items, so a failure between keeps it twice, never none.
func (s Store) Reopen(id int, text string, now time.Time) (Item, error) {
	return s.reopen(id, text, nil, now)
}

// Overrule reopens the user's ticked item id, an ask decided for them (Decided), while it reads text, and answers it
// with pick, one of the options it was picked over (its label, or its number in the ask), in the same write: it stays
// ticked, by the user, with no Decided. It returns the item as answered and what was decided; via is where (Item.Via).
// Refused, changing nothing, for an item Reopen refuses (not the user's, ticked over a day ago), one not decided for the
// user, or a pick not among the others. A StillDone error says it is answered all the same.
func (s Store) Overrule(id int, text, pick, via string, now time.Time) (it Item, was Decided, err error) {
	it, err = s.reopen(id, text, func(p *Item, orig Item) error {
		if orig.Decided == nil {
			return Refused{fmt.Errorf("item %d wasn't decided for the user: reopen it to answer it again", id)}
		}
		was = *orig.Decided
		want := Split(Clean(pick))
		names := func(o string) bool {
			c := Split(o)
			return strings.EqualFold(c.Label, want.Label) && (want.Detail == "" || strings.EqualFold(c.Detail, want.Detail))
		}
		// As Pick reads it: an option's label, else its number in the ask, as its keys are
		if n, err := strconv.Atoi(want.Label); !slices.ContainsFunc(p.Options, names) && err == nil && n >= 1 && n <= len(p.Options) {
			want = Split(p.Options[n-1])
		}
		i := slices.IndexFunc(was.Over, names)
		if i < 0 {
			return Refused{fmt.Errorf("item %d's other options are %s, not %q", id, strings.Join(quoted(was.OverLabels()), ", "), pick)}
		}
		p.tick(now.Unix(), DoneByYou)
		p.Answer, p.Via = answerOf(p.Options, was.Over[i]), via
		return nil
	}, now)
	return it, was, err
}

// StillDone is a reopen's failure once the list is written: the item is reopened (or overruled), and still shows among
// the finished items too.
type StillDone struct{ error }

func (e StillDone) Unwrap() error { return e.error }

// quoted is each of ss in double quotes.
func quoted(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// reopen is Reopen, running then (when not nil) on the reopened item before it is written: its refusal writes nothing.
func (s Store) reopen(id int, text string, then func(p *Item, orig Item) error, now time.Time) (Item, error) {
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
		it.Asked, it.Dropped, it.For = 0, nil, 0
		// Reopened, the user answers it: no one settles it again
		it.Result, it.DoneBy, it.Obvious, it.Decided = "", "", "", nil
		if it.Until != 0 && now.Unix() >= it.Until {
			it.Until = 0
		}
		if then != nil {
			if err := then(&it, orig); err != nil {
				return err
			}
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
			return StillDone{fmt.Errorf("#%d reopened as #%d, but it still shows in Done: %w", orig.ID, it.ID, err)}
		}
		return nil
	})
	return it, err
}
