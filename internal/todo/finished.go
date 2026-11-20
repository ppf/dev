package todo

import (
	"cmp"
	"encoding/json"
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

// Finished is a ticked item that left its list (List.Finished): Assignee whose it was when ticked, DoneAt when, unix seconds.
type Finished struct {
	ID       int        `json:"id"`
	Text     string     `json:"text"`
	Link     string     `json:"link,omitempty"`
	Answer   string     `json:"answer,omitempty"`
	On       string     `json:"on,omitempty"`
	Assignee FinishedBy `json:"assignee"`
	DoneAt   int64      `json:"done_at"`
	Quiet    bool       `json:"quiet,omitempty"`
}

// FinishedBy is whose a finished item was, by its stored on and session: Kind "you" (Own), "worker" (on a session,
// Name its name) or "boss" (on a person: the list's boss followed it up, Name the boss, "boss" for a desktop app one's).
type FinishedBy struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
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
	kept := finished(append(old, items...), now)
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

// shownFinished is items as boards show them, on boss's list.
func shownFinished(items []Item, boss string) []Finished {
	out := make([]Finished, 0, len(items))
	for _, it := range items {
		var by FinishedBy
		switch {
		case Own(it.On, it.Session, boss):
			by = FinishedBy{Kind: "you"}
		case it.Session != nil:
			by = FinishedBy{Kind: "worker", Name: it.Session.Name}
		default:
			by = FinishedBy{Kind: "boss", Name: cmp.Or(boss, "boss")}
		}
		out = append(out, Finished{ID: it.ID, Text: it.Text, Link: it.Link, Answer: it.Answer, On: it.On, Assignee: by, DoneAt: it.Done, Quiet: it.Quiet})
	}
	return out
}
