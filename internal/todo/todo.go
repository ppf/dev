// Package todo is a boss's to-do list: what waits on the user, kept by the boss with dev todo and shown at
// the top of its board. One locked file per boss, keyed by its tmux session or its desktop app session, so
// it outlives the coordinator's /clear.
package todo

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/tmux"
)

const (
	// doneShown is how long a ticked item stays on the list, ticked, before it leaves.
	doneShown = 5 * time.Second
	// stale is how long a list goes unchanged before its boards dim its age: it may be out of date.
	stale = time.Hour
)

// UntilLayout is how an item's Until time is written: local date and minute.
const UntilLayout = "2006-01-02 15:04"

// Item is one thing waiting on the user. Done is when it was ticked, unix seconds; 0 while open. Until, when
// set, ticks it by itself at that time, unix seconds.
type Item struct {
	ID    int    `json:"id"`
	Text  string `json:"text"`
	On    string `json:"on,omitempty"` // who it waits on, when not the user
	Link  string `json:"link,omitempty"`
	Done  int64  `json:"done,omitempty"`
	Until int64  `json:"until,omitempty"`
}

// file is a list as stored; Next is the id the next add gets, reset once the list is empty.
type file struct {
	Items   []Item `json:"items"`
	Updated int64  `json:"updated"`
	Next    int    `json:"next"`
}

// List is a list as its boards show it, for dev todo --json: items waiting on the user first, then those
// waiting on someone else, each group in the order added; a ticked item stays in place, done, for doneShown.
// Updated is the last change, unix seconds (0: never); Stale is a list unchanged for an hour.
type List struct {
	Items   []Shown `json:"items"`
	Updated int64   `json:"updated"`
	Stale   bool    `json:"stale"`
}

// Shown is an item on a board; Until is when it ticks by itself, unix seconds (0: never).
type Shown struct {
	ID    int    `json:"id"`
	Text  string `json:"text"`
	On    string `json:"on,omitempty"`
	Link  string `json:"link,omitempty"`
	Done  bool   `json:"done"`
	Until int64  `json:"until,omitempty"`
}

// Open counts the items not ticked, and of them those waiting on someone else.
func (l List) Open() (n, on int) {
	for _, it := range l.Items {
		if !it.Done {
			n++
			if it.On != "" {
				on++
			}
		}
	}
	return n, on
}

// ErrNotBoss is ForPane's error for a session not marked a dev boss.
var ErrNotBoss = errors.New("not a boss session (dev boss): the to do list is its boss's")

// Store is one boss's list file.
type Store struct{ path string }

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// open is the store named key under $XDG_STATE_HOME/dev/todo.
func open(key string) (Store, error) {
	dir, err := config.StateDir()
	if err != nil {
		return Store{}, err
	}
	name := strings.Trim(unsafe.ReplaceAllString(key, "_"), "_")
	return Store{path: filepath.Join(dir, "todo", name+".json")}, nil
}

// Desktop is the list of the desktop app boss session id (local_…).
func Desktop(id string) (Store, error) { return open("desktop-" + id) }

// session is what keying a tmux boss's list reads of its session.
type session struct {
	Socket  string `tmux:"socket_path"`
	ID      string `tmux:"session_id"`
	Created string `tmux:"session_created"`
	Name    string `tmux:"session_name"`
	Herd    string `tmux:"@dev_herd"`
}

// ForPane is the list of the dev boss session pane is in (its coordinator's or its previews pane), keyed as
// its inbox is: socket, session id and creation time, as a restarted server reuses ids. Any other session's
// pane is an error.
func ForPane(c *tmux.Client, pane string) (Store, error) {
	out, err := c.Run("display-message", "-p", "-t", pane, tmux.Format[session]())
	if err != nil {
		return Store{}, err
	}
	rows, err := tmux.Parse[session](out)
	if err != nil {
		return Store{}, err
	}
	if len(rows) != 1 {
		return Store{}, fmt.Errorf("todo: %d rows for %s", len(rows), pane)
	}
	s := rows[0]
	if s.Herd == "" {
		return Store{}, fmt.Errorf("%s is %w", s.Name, ErrNotBoss)
	}
	return open(s.Socket + "-" + s.ID + "-" + s.Created)
}

// Read is the list as its boards show it at now; a missing file is an empty list.
func (s Store) Read(now time.Time) (List, error) {
	f, err := s.read()
	if err != nil {
		return List{Items: []Shown{}}, err
	}
	f.prune(now)
	l := List{Items: []Shown{}, Updated: f.Updated, Stale: f.Updated != 0 && now.Sub(time.Unix(f.Updated, 0)) >= stale}
	for _, it := range f.Items {
		l.Items = append(l.Items, Shown{ID: it.ID, Text: it.Text, On: it.On, Link: it.Link, Done: it.Done != 0, Until: it.Until})
	}
	slices.SortStableFunc(l.Items, func(a, b Shown) int {
		return cmp.Compare(min(len(a.On), 1), min(len(b.On), 1))
	})
	return l, nil
}

// Set replaces the list with texts, numbered from 1.
func (s Store) Set(texts []string, now time.Time) error {
	return s.update(now, func(f *file) error {
		f.Items = make([]Item, len(texts))
		for i, t := range texts {
			f.Items[i] = Item{ID: i + 1, Text: t}
		}
		f.Next = len(texts) + 1
		return nil
	})
}

// Add appends it and returns its id; ids restart at 1 once the list is empty, ticked items shown included.
func (s Store) Add(it Item, now time.Time) (id int, err error) {
	err = s.update(now, func(f *file) error {
		if len(f.Items) == 0 {
			f.Next = 1
		}
		it.ID, it.Done = max(f.Next, 1), 0
		f.Items = append(f.Items, it)
		f.Next, id = it.ID+1, it.ID
		return nil
	})
	return id, err
}

// Replace puts it in the place of open item id, keeping its number and the On, Link and Until it doesn't set;
// an id that isn't one is an error.
func (s Store) Replace(id int, it Item, now time.Time) error {
	return s.update(now, func(f *file) error {
		i := slices.IndexFunc(f.Items, func(old Item) bool { return old.ID == id && old.Done == 0 })
		if i < 0 {
			return fmt.Errorf("no open item %d", id)
		}
		old := f.Items[i]
		it.ID, it.Done = id, 0
		it.On, it.Link, it.Until = cmp.Or(it.On, old.On), cmp.Or(it.Link, old.Link), cmp.Or(it.Until, old.Until)
		f.Items[i] = it
		return nil
	})
}

// Done ticks the open items ids; an id that isn't one changes nothing and is an error.
func (s Store) Done(ids []int, now time.Time) error {
	return s.update(now, func(f *file) error {
		for _, id := range ids {
			i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
			if i < 0 {
				return fmt.Errorf("no open item %d", id)
			}
			f.Items[i].Done = now.Unix()
		}
		return nil
	})
}

// Clear empties the list.
func (s Store) Clear(now time.Time) error {
	return s.update(now, func(f *file) error {
		f.Items, f.Next = nil, 1
		return nil
	})
}

// prune ticks the open items whose Until has come, as at Until, and drops the items ticked over doneShown ago.
func (f *file) prune(now time.Time) {
	for i, it := range f.Items {
		if it.Done == 0 && it.Until != 0 && now.Unix() >= it.Until {
			f.Items[i].Done = it.Until
		}
	}
	f.Items = slices.DeleteFunc(f.Items, func(it Item) bool {
		return it.Done != 0 && now.Sub(time.Unix(it.Done, 0)) >= doneShown
	})
}

// read loads the file; a missing one is empty.
func (s Store) read() (file, error) {
	var f file
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return f, nil
	case err != nil:
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}, fmt.Errorf("todo %s: %w", s.path, err)
	}
	return f, nil
}

// update runs fn on the file under an exclusive lock, stamps it now and writes it back.
func (s Store) update(now time.Time, fn func(*file) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	f, err := s.read()
	if err != nil {
		return err
	}
	f.prune(now)
	if err := fn(&f); err != nil {
		return err
	}
	f.Updated = now.Unix()
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ParseUntil reads an --until time in now's location: "15:30" today, "2026-11-16" the end of that day,
// "2026-11-16 10:00". A time not after now is an error: its item would tick at once.
func ParseUntil(s string, now time.Time) (time.Time, error) {
	loc := now.Location()
	t, err := time.ParseInLocation("15:04", s, loc)
	if err == nil {
		y, m, d := now.Date()
		t = time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, loc)
	} else if t, err = time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		t = t.AddDate(0, 0, 1)
	} else if t, err = time.ParseInLocation(UntilLayout, s, loc); err != nil {
		return time.Time{}, fmt.Errorf("until %q: want 15:30, 2026-11-16 or \"2026-11-16 10:00\"", s)
	}
	if !t.After(now) {
		return time.Time{}, fmt.Errorf("until %q: %s has passed", s, t.Format(UntilLayout))
	}
	return t, nil
}

// Reminder is the open items for a boss reading worker notes, numbered, with a line asking it to tick what the
// notes closed; "" when none is open.
func (l List) Reminder() string {
	var b strings.Builder
	for _, it := range l.Items {
		if it.Done {
			continue
		}
		fmt.Fprintf(&b, "\n%d. %s", it.ID, it.Text)
		if it.On != "" {
			b.WriteString(" (on " + it.On + ")")
		}
		if it.Until != 0 {
			b.WriteString(" (until " + time.Unix(it.Until, 0).Format(UntilLayout) + ")")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "[dev todo] Your open to do list:" + b.String() +
		"\nTick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> \"<new text>\"."
}

// Clean is s as one board row: control characters dropped, space runs collapsed, trimmed.
func Clean(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}

var prLink = regexp.MustCompile(`/(?:pull|merge_requests)/(\d+)(?:[/?#]|$)`)

// PR is the number of the GitHub PR or GitLab MR the item links to, 0 for none.
func (it Shown) PR() int {
	m := prLink.FindStringSubmatch(it.Link)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
