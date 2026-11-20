package todo

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// MaxDoing is the longest doing line, in characters: one row under a card.
const MaxDoing = 100

// SetDoing sets what open item id's doer does now (d), or ends it (d nil). user: the user on their own item, not an ask,
// only while it still reads text, ending a start asked for it; else the boss on its own (OnBoss). Anyone else's item, and ending what isn't set,
// are refused.
func (s Store) SetDoing(id int, text string, user bool, d *Doing, now time.Time) (Item, error) {
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
		case d == nil && p.Doing == nil:
			return fmt.Errorf("item %d isn't marked doing", id)
		}
		if d != nil {
			// The user does it: no worker is to be started on it
			d.At = now.Unix()
			if user {
				p.Started = nil
			}
		}
		p.Doing = d
		it = *p
		return nil
	}))
	return it, err
}

// checkPartOf refuses linking item id (0: one being added) to partner: itself, no open item, or one already linked.
func (f *file) checkPartOf(id, partner int) error {
	if partner == 0 {
		return nil
	}
	if partner == id {
		return fmt.Errorf("--part-of %d: an item isn't part of itself", partner)
	}
	if !slices.ContainsFunc(f.Items, func(it Item) bool { return it.ID == partner && it.Done == 0 }) {
		return fmt.Errorf("--part-of %d: no open item %d", partner, partner)
	}
	for _, it := range f.Items {
		// A ticked half still shown is done: it links nothing
		if it.ID == id || it.Done != 0 {
			continue
		}
		if (it.ID == partner && it.PartOf != 0) || it.PartOf == partner || (id != 0 && it.PartOf == id) {
			return fmt.Errorf("--part-of %d: already linked to another item", partner)
		}
	}
	return nil
}

// unlinkGone clears the links whose other half left the list.
func (f *file) unlinkGone() {
	for i, it := range f.Items {
		if it.PartOf != 0 && !slices.ContainsFunc(f.Items, func(x Item) bool { return x.ID == it.PartOf }) {
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

// tick ticks it at at: what its doer did ends.
func (it *Item) tick(at int64) { it.Done, it.Doing = at, nil }

// doingLine is what lines() says of an item someone works on: the user on their own, the boss on its own.
func doingLine(it Shown) string {
	who := "the user is on it"
	if OnBoss(it.On, it.Session) {
		who = "you are on it"
	}
	if it.Doing.Text != "" {
		who += ": " + it.Doing.Text
	}
	return " (" + who + ")"
}
