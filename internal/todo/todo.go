// Package todo is a boss's to-do list: what waits on the user, kept by the boss with dev todo and shown at
// the top of its board. One locked file per boss, keyed by its tmux session's name or its desktop app session,
// so it outlives the coordinator's /clear, and a tmux boss reopened under its name gets its list back.
package todo

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/ppf/dev/internal/sidebar"
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

// Me is the --on that gives an item back to the user.
const Me = "me"

// Session is the boss's worker an item is on: its tmux session name or desktop app session id (local_…), and
// the name its board shows.
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Item is one thing waiting on the user. Done is when it was ticked, unix seconds; 0 while open. Until, when
// set, ticks it by itself at that time, unix seconds. An ask (dev todo ask) has the Options the user picks from;
// Answer is what the user answered (dev todo answer), set as it ticks.
type Item struct {
	ID      int      `json:"id"`
	Text    string   `json:"text"`
	On      string   `json:"on,omitempty"`      // who does it, when not the user: a worker's name or a person
	Session *Session `json:"session,omitempty"` // the worker On names, when it named one at add
	Link    string   `json:"link,omitempty"`
	Done    int64    `json:"done,omitempty"`
	Until   int64    `json:"until,omitempty"`
	Options []string `json:"options,omitempty"`
	Answer  string   `json:"answer,omitempty"`
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

// Shown is an item on a board; Until is when it ticks by itself, unix seconds (0: never). A Session the board
// doesn't list any more is gone.
type Shown struct {
	ID      int      `json:"id"`
	Text    string   `json:"text"`
	On      string   `json:"on,omitempty"`
	Session *Session `json:"session,omitempty"`
	Link    string   `json:"link,omitempty"`
	Done    bool     `json:"done"`
	Until   int64    `json:"until,omitempty"`
	Options []string `json:"options,omitempty"`
	Answer  string   `json:"answer,omitempty"`
	Gone    bool     `json:"-"` // its Session isn't among the boss's workers (MarkGone); boards work it out themselves
}

// MarkGone marks the open items whose Session isn't among workers, read only when one has a Session.
func (l List) MarkGone(workers func() ([]Session, error)) error {
	if !slices.ContainsFunc(l.Items, func(it Shown) bool { return !it.Done && it.Session != nil }) {
		return nil
	}
	ws, err := workers()
	if err != nil {
		return err
	}
	for i, it := range l.Items {
		l.Items[i].Gone = !it.Done && it.Session != nil && !slices.ContainsFunc(ws, func(w Session) bool { return w.ID == it.Session.ID })
	}
	return nil
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

const desktopPrefix = "desktop-"

// Desktop is the list of the desktop app boss session id (local_…).
func Desktop(id string) (Store, error) { return open(desktopPrefix + id) }

// AdoptDesktop moves into s, when it has no file yet, the newest list with open items earlier accepts; adopted
// reports it. Unreadable lists are skipped and returned in err.
func (s Store) AdoptDesktop(earlier func() (func(id string) bool, error)) (adopted bool, err error) {
	if _, err := os.Stat(s.path); !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(s.path), desktopPrefix+"*.json"))
	if err != nil || len(paths) == 0 {
		return false, err
	}
	isEarlier, err := earlier()
	if err != nil {
		return false, err
	}
	var from Store
	var updated int64
	var unread []error
	for _, path := range paths {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), desktopPrefix), ".json")
		if path == s.path || !isEarlier(id) {
			continue
		}
		l, err := Store{path}.Read(time.Now())
		if err != nil {
			unread = append(unread, err)
			continue
		}
		// An emptied list stays a file: a newer empty one mustn't win over open items
		if n, _ := l.Open(); n > 0 && (from.path == "" || l.Updated > updated) {
			from, updated = Store{path}, l.Updated
		}
	}
	if from.path != "" {
		adopted, err = s.take(from)
	}
	return adopted, errors.Join(append(unread, err)...)
}

// take moves from's file to s, when s has none and from still has one; taken reports it.
func (s Store) take(from Store) (taken bool, err error) {
	err = s.locked(func() error {
		return from.locked(func() error {
			// Under both locks: another boss may have taken it, or s got a list meanwhile
			if _, err := os.Stat(s.path); !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, err := os.Stat(from.path); errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			taken = true
			return os.Rename(from.path, s.path)
		})
	})
	return taken, err
}

// sessionKey is name for a list's key, hashed when unsafe in a file name so boss-app+api and boss-app_api differ.
func sessionKey(name string) string {
	if !unsafe.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return unsafe.ReplaceAllString(name, "_") + "-" + hex.EncodeToString(sum[:4])
}

// ignoreMissing is err, nil for a file that doesn't exist.
func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// session is what keying a tmux boss's list reads of its session.
type session struct {
	Socket  string `tmux:"socket_path"`
	Name    string `tmux:"session_name"`
	Herd    string `tmux:"@dev_herd"`
	ID      string `tmux:"session_id"`
	Created string `tmux:"session_created"`
}

// paneSession is the session pane is in.
func paneSession(c *tmux.Client, pane string) (session, error) {
	out, err := c.Run("display-message", "-p", "-t", pane, tmux.Format[session]())
	if err != nil {
		return session{}, err
	}
	rows, err := tmux.Parse[session](out)
	if err != nil {
		return session{}, err
	}
	if len(rows) != 1 {
		return session{}, fmt.Errorf("todo: %d rows for %s", len(rows), pane)
	}
	return rows[0], nil
}

// ForPane is the list of the dev boss session pane is in, keyed by socket and session name so a boss reopened
// under its name gets it back; a list under its earlier key (session id and creation time) moves to it.
func ForPane(c *tmux.Client, pane string) (Store, error) {
	s, err := paneSession(c, pane)
	if err != nil {
		return Store{}, err
	}
	if s.Herd == "" {
		return Store{}, fmt.Errorf("%s is %w", s.Name, ErrNotBoss)
	}
	st, err := open(s.Socket + "-" + sessionKey(s.Name))
	if err != nil {
		return st, err
	}
	old, err := open(s.Socket + "-" + s.ID + "-" + s.Created)
	if err != nil {
		return st, err
	}
	// Stat first: most calls find the name's list, or nothing to move, without taking locks
	if _, err := os.Stat(st.path); !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	if _, err := os.Stat(old.path); err != nil {
		return st, ignoreMissing(err)
	}
	_, err = st.take(old)
	return st, err
}

// worker is what Workers reads of a tmux session.
type worker struct {
	Name      string `tmux:"session_name"`
	StartedBy string `tmux:"@dev_started_by"`
	Herd      string `tmux:"@dev_herd"`
}

// Workers lists the sessions the boss session pane is in started, bosses aside, by name.
func Workers(c *tmux.Client, pane string) ([]Session, error) {
	boss, err := paneSession(c, pane)
	if err != nil {
		return nil, err
	}
	all, err := tmux.Query[worker](c, "list-sessions")
	if err != nil {
		return nil, err
	}
	var ws []Session
	for _, w := range all {
		if w.Herd == "" && sidebar.Names(w.StartedBy, boss.ID, boss.Name, true) {
			ws = append(ws, Session{ID: w.Name, Name: w.Name})
		}
	}
	return ws, nil
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
		l.Items = append(l.Items, Shown{ID: it.ID, Text: it.Text, On: it.On, Session: it.Session, Link: it.Link, Done: it.Done != 0, Until: it.Until, Options: it.Options, Answer: it.Answer})
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
		if it.On == Me {
			it.On, it.Session = "", nil
		}
		f.Items = append(f.Items, it)
		f.Next, id = it.ID+1, it.ID
		return nil
	})
	return id, err
}

// Replace puts it in the place of open item id, keeping its number and the Text, On (with its Session), Link and
// Until it doesn't set; On Me gives it back to the user. An id that isn't one is an error.
func (s Store) Replace(id int, it Item, now time.Time) error {
	return s.update(now, func(f *file) error {
		i := slices.IndexFunc(f.Items, func(old Item) bool { return old.ID == id && old.Done == 0 })
		if i < 0 {
			return fmt.Errorf("no open item %d", id)
		}
		old := f.Items[i]
		it.ID, it.Done = id, 0
		switch it.On {
		case Me:
			it.On, it.Session = "", nil
		case "":
			it.On, it.Session = old.On, old.Session
		}
		it.Text, it.Link, it.Until = cmp.Or(it.Text, old.Text), cmp.Or(it.Link, old.Link), cmp.Or(it.Until, old.Until)
		if it.Options == nil {
			it.Options = old.Options
		}
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

// MaxOptions is the most options an ask has: a board answers one with a key, 1 to 9.
const MaxOptions = 9

// recommended ends the label of an option the asker recommends, case aside.
const recommended = "(recommended)"

// Label is option o without its " (Recommended)"; rec reports it had one.
func Label(o string) (label string, rec bool) {
	if n := len(o) - len(recommended); n >= 0 && strings.EqualFold(o[n:], recommended) {
		return strings.TrimSpace(o[:n]), true
	}
	return o, false
}

// Pick is the answer arg gives to an item with options: the option whose label (Label) it names, case aside, else
// option n for a number 1 to len(options), else arg itself, the user's own words. Another number is an error, as is
// an empty answer.
func Pick(options []string, arg string) (string, error) {
	arg = Clean(arg)
	if arg == "" {
		return "", errors.New("empty answer")
	}
	want, _ := Label(arg)
	for _, o := range options {
		if l, _ := Label(o); strings.EqualFold(l, want) {
			return l, nil
		}
	}
	if n, err := strconv.Atoi(arg); err == nil && len(options) > 0 {
		if n < 1 || n > len(options) {
			return "", fmt.Errorf("option %d: it has %d (1-%d)", n, len(options), len(options))
		}
		l, _ := Label(options[n-1])
		return l, nil
	}
	return arg, nil
}

// Answer ticks the user's own open ask id with what arg answers it (Pick), and returns the item as answered. An item
// that isn't an ask, is on someone else or isn't open is an error; so is one whose text isn't question ("" skips the check),
// as item numbers restart once a list empties.
func (s Store) Answer(id int, question, arg string, now time.Time) (Item, error) {
	var it Item
	err := s.update(now, func(f *file) error {
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && (it.Done == 0 || it.Answer != "") })
		if i < 0 {
			return fmt.Errorf("no open item %d", id)
		}
		p := &f.Items[i]
		switch {
		case question != "" && p.Text != question:
			return fmt.Errorf("item %d is %q now, not %q: look at the list again", id, p.Text, question)
		case p.Done != 0:
			return fmt.Errorf("item %d is answered already: %s", id, p.Answer)
		case p.On != "":
			return fmt.Errorf("item %d is on %s, not the user's to answer", id, p.On)
		case len(p.Options) == 0:
			return fmt.Errorf("item %d is no question (dev todo ask): tick it with dev todo done", id)
		}
		answer, err := Pick(p.Options, arg)
		if err != nil {
			return err
		}
		p.Done, p.Answer = now.Unix(), answer
		it = *p
		return nil
	})
	return it, err
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

// locked runs fn under the file's exclusive lock.
func (s Store) locked(fn func() error) error {
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
	return fn()
}

// update runs fn on the file under an exclusive lock, stamps it now and writes it back.
func (s Store) update(now time.Time, fn func(*file) error) error {
	return s.locked(func() error {
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
	})
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

// startShown is how many open items the list a boss gets at its start names before "+N more".
const startShown = 20

// lines is the open items, numbered, one per line after a newline; past limit (0: none) a "+N more" line.
func (l List) lines(limit int) string {
	var b strings.Builder
	n := 0
	for _, it := range l.Items {
		if it.Done {
			continue
		}
		n++
		if limit > 0 && n > limit {
			continue
		}
		fmt.Fprintf(&b, "\n%d. %s", it.ID, it.Text)
		switch {
		case it.Gone:
			b.WriteString(" (on " + it.On + ", session gone)")
		case it.On != "":
			b.WriteString(" (on " + it.On + ")")
		}
		if it.Until != 0 {
			b.WriteString(" (until " + time.Unix(it.Until, 0).Format(UntilLayout) + ")")
		}
		if len(it.Options) > 0 {
			b.WriteString(" (asked, options: " + strings.Join(it.Options, " · ") + ")")
		}
	}
	if limit > 0 && n > limit {
		fmt.Fprintf(&b, "\n+%d more: dev todo", n-limit)
	}
	return b.String()
}

// whoLine asks a boss to say who does each item, as the reminders end.
const whoLine = "\nAn item a worker session or someone else does, not the user: dev todo add --replaces <n> --on <worker name|person>."

// Reminder is the open items for a boss reading worker notes, numbered, with a line asking it to tick what the
// notes closed; "" when none is open.
func (l List) Reminder() string {
	items := l.lines(0)
	if items == "" {
		return ""
	}
	return "[dev todo] Your open to do list:" + items +
		"\nTick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> \"<new text>\"." + whoLine
}

// StartReminder is the open items for a boss starting or /clear'ed, at most startShown, with how long ago the
// list changed (age, e.g. "2h ago") and, when kept, that an earlier boss session left it; "" when none is open.
func (l List) StartReminder(kept bool, age string) string {
	items := l.lines(startShown)
	if items == "" {
		return ""
	}
	from := "updated " + age
	if kept {
		from = "kept from your last boss session, " + from
	}
	return "[dev todo] Your open to do list (" + from + "):" + items +
		"\nCheck each still holds: tick what's done (dev todo done <n>), update what changed (dev todo add --replaces <n> \"<new text>\")." + whoLine
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

// Match is the worker on names: by id, else the only worker by that name, case and outer spaces aside.
func Match(on string, workers []Session) (Session, bool) {
	if i := slices.IndexFunc(workers, func(w Session) bool { return w.ID == on }); i >= 0 {
		return workers[i], true
	}
	var found []Session
	for _, w := range workers {
		if strings.EqualFold(strings.TrimSpace(w.Name), strings.TrimSpace(on)) {
			found = append(found, w)
		}
	}
	if len(found) != 1 {
		return Session{}, false
	}
	return found[0], true
}
