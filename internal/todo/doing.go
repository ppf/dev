package todo

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// MaxDoing is the longest doing line, in characters: one row under a card.
const MaxDoing = 100

// Seen is an item's doing line and start as a board showed them: a change made from that board is refused once either
// changed since.
type Seen struct {
	Doing   *Doing   `json:"doing"`
	Started *Started `json:"started"`
}

// SetDoing sets what open item id's doer does now (d), or ends it (d nil), ending a start asked for it. user: the user
// on their own item, not an ask, only while it still reads text, and as seen when given; else the boss on its own
// (OnBoss). Anyone else's item, and ending what isn't set, are refused.
func (s Store) SetDoing(id int, text string, user bool, seen *Seen, d *Doing, now time.Time) (Item, error) {
	var it Item
	if d != nil {
		d.Text = Clean(d.Text)
		if n := utf8.RuneCountInString(d.Text); n > MaxDoing {
			return it, Refused{fmt.Errorf("doing line is %d characters, at most %d", n, MaxDoing)}
		}
	}
	err := s.update(now, refusing(func(f *file) error {
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
		if i < 0 {
			return fmt.Errorf("no open item %d", id)
		}
		p := &f.Items[i]
		switch {
		case text != "" && p.Text != text:
			return fmt.Errorf("item %d is %q now, not %q: look at the list again", id, p.Text, text)
		case user && !Own(p.On, p.Session, s.boss):
			return fmt.Errorf("item %d is on %s, not the user's", id, p.On)
		case user && len(p.Options) > 0:
			return fmt.Errorf("item %d is a question: answer it with dev todo answer", id)
		case !user && !OnBoss(p.On, p.Session):
			return fmt.Errorf("item %d isn't your own (--on boss): only the boss's own items are marked doing by the boss", id)
		case seen != nil && (!equal(p.Doing, seen.Doing) || !equal(p.Started, seen.Started)):
			return fmt.Errorf("item %d changed since it was read: look at the list again", id)
		case d == nil && p.Doing == nil:
			return fmt.Errorf("item %d isn't marked doing", id)
		}
		if d != nil {
			// Its doer is on it: no worker is to be started on it
			d.At = now.Unix()
			p.Started = nil
		}
		p.Doing = d
		it = *p
		return nil
	}))
	return it, err
}

// equal reports a and b both nil, or pointing at equal values.
func equal[T comparable](a, b *T) bool { return a == b || a != nil && b != nil && *a == *b }

// checkPartOf refuses linking item id (0: one being added) to partner: itself, no open item, or one already linked.
func (f *file) checkPartOf(id, partner int) error {
	if partner == 0 {
		return nil
	}
	if partner == id {
		return fmt.Errorf("--part-of %d: an item isn't part of itself", partner)
	}
	if !f.open(partner) {
		return fmt.Errorf("--part-of %d: no open item %d", partner, partner)
	}
	for _, it := range f.Items {
		if it.ID == id {
			continue
		}
		if (it.ID == partner && it.PartOf != 0) || it.PartOf == partner || (id != 0 && it.PartOf == id) {
			return fmt.Errorf("--part-of %d: already linked to another item", partner)
		}
	}
	return nil
}

// checkFor refuses ask it (id 0: one being added) waiting for item it.For: no ask, itself, no open item, or an ask.
func (f *file) checkFor(id int, it Item) error {
	switch n := it.For; {
	case n == 0:
		return nil
	case len(it.Options) == 0:
		return fmt.Errorf("--for %d: only an ask (dev todo ask) is what an item waits on", n)
	case n == id:
		return fmt.Errorf("--for %d: an ask doesn't wait on itself", n)
	case !f.open(n):
		return fmt.Errorf("--for %d: no open item %d", n, n)
	case slices.ContainsFunc(f.Items, func(x Item) bool { return x.ID == n && len(x.Options) > 0 }):
		return fmt.Errorf("--for %d: item %d is an ask itself", n, n)
	}
	return nil
}

// askFor is the open ask item id waits on (its For), the newest when several; nil for none.
func (f *file) askFor(id int) *Item {
	var ask *Item
	for i, it := range f.Items {
		if it.For == id && it.Done == 0 && len(it.Options) > 0 && (ask == nil || it.ID > ask.ID) {
			ask = &f.Items[i]
		}
	}
	return ask
}

// unlinkGone clears the links whose other half left the list.
func (f *file) unlinkGone() {
	for i, it := range f.Items {
		if it.PartOf != 0 && !slices.ContainsFunc(f.Items, func(x Item) bool { return x.ID == it.PartOf }) {
			f.Items[i].PartOf = 0
		}
	}
}

// dropDoneLinks clears the links of ids whose other half is ticked, still shown: it links nothing new.
func (f *file) dropDoneLinks(ids ...int) {
	done := func(id int) bool {
		return slices.ContainsFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done != 0 })
	}
	for i, it := range f.Items {
		mine := slices.Contains(ids, it.ID) || slices.Contains(ids, it.PartOf)
		if it.PartOf != 0 && mine && (it.Done != 0 || done(it.PartOf)) {
			f.Items[i].PartOf = 0
		}
	}
}

// open reports id an open item.
func (f *file) open(id int) bool {
	return slices.ContainsFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
}

// unlink clears the links other items store to id.
func (f *file) unlink(id int) {
	for i, it := range f.Items {
		if it.PartOf == id {
			f.Items[i].PartOf = 0
		}
	}
}

// partner is the id of the item linked to id from its side (--part-of id), an open one before a ticked one; 0 for none.
func partner(items []Item, id int) int {
	found := 0
	for _, it := range items {
		switch {
		case it.PartOf != id:
		case it.Done == 0:
			return it.ID
		case found == 0:
			found = it.ID
		}
	}
	return found
}

// tick ticks it at at, by whom (DoneBy): what its doer did ends.
func (it *Item) tick(at int64, by string) { it.Done, it.Doing, it.DoneBy = at, nil, by }

// OnIt is what a list's text says of an item someone works on (Doing): the user on their own, boss (the wording for its
// reader) on the boss's own; "" for none.
func (it Shown) OnIt(boss string) string {
	if it.Doing == nil {
		return ""
	}
	who := "the user is on it"
	if OnBoss(it.On, it.Session) {
		who = boss
	}
	if it.Doing.Text != "" {
		who += ": " + it.Doing.Text
	}
	return who
}
