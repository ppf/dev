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
	// unchanged is how long an open item goes without a --replaces before it is checked as unchanged.
	unchanged = 48 * time.Hour
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
	Added   int64    `json:"added,omitempty"`   // when it was added or last replaced, unix seconds
	DoneOn  string   `json:"done_on,omitempty"` // the state of its linked PR that ticks it by itself (DoneOns)
}

// file is a list as stored; Next is the id the next add gets, reset once the list is empty; Boss is its boss
// session's name, once a change through ForPane wrote it; Ticked the items that ticked by themselves, until its boss
// is told (TakeTicked).
type file struct {
	Items   []Item   `json:"items"`
	Updated int64    `json:"updated"`
	Next    int      `json:"next"`
	Boss    string   `json:"boss,omitempty"`
	Ticked  []Ticked `json:"ticked,omitempty"`
}

// List is a list as its boards show it, for dev todo --json: items waiting on the user first, then those
// waiting on someone else, each group in the order added; a ticked item stays in place, done, for doneShown.
// Updated is the last change, unix seconds (0: never); Stale is a list unchanged for an hour.
type List struct {
	Items   []Shown `json:"items"`
	Updated int64   `json:"updated"`
	Stale   bool    `json:"stale"`
}

// Shown is an item on a board; Until is when it ticks by itself, unix seconds (0: never); Added when it was added or
// last replaced (0: an older dev's); Check why it may be out of date. A Session the board doesn't list any more is gone.
type Shown struct {
	ID      int            `json:"id"`
	Text    string         `json:"text"`
	On      string         `json:"on,omitempty"`
	Session *Session       `json:"session,omitempty"`
	Link    string         `json:"link,omitempty"`
	Done    bool           `json:"done"`
	Until   int64          `json:"until,omitempty"`
	Options []string       `json:"options,omitempty"`
	Answer  string         `json:"answer,omitempty"`
	Added   int64          `json:"added,omitempty"`
	DoneOn  string         `json:"done_on,omitempty"`
	Check   *sidebar.Check `json:"check,omitempty"`
	Gone    bool           `json:"-"` // its Session isn't among the boss's workers (MarkGone); boards work it out themselves
	// Status and StatusOn are what Owner.Status says, once SetStatus ran
	Status   string `json:"status,omitempty"`
	StatusOn string `json:"status_on,omitempty"`
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

// Store is one boss's list file; boss is its session's name when known.
type Store struct{ path, boss string }

// Boss is the name of the boss session whose list it is, when known.
func (s Store) Boss() string { return s.boss }

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
		l, err := Store{path: path}.Read(time.Now())
		if err != nil {
			unread = append(unread, err)
			continue
		}
		// An emptied list stays a file: a newer empty one mustn't win over open items
		if n, _ := l.Open(); n > 0 && (from.path == "" || l.Updated > updated) {
			from, updated = Store{path: path}, l.Updated
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
			if err := os.Rename(from.path, s.path); err != nil {
				return err
			}
			// Its checks go with it, under its lock, which guards them too
			return ignoreMissing(os.Rename(from.checksPath(), s.checksPath()))
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
	st.boss = s.Name
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
}

// Workers lists the sessions the boss session pane is in started, bosses among them, by name.
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
		if sidebar.Names(w.StartedBy, boss.ID, boss.Name, true) {
			ws = append(ws, Session{ID: w.Name, Name: w.Name})
		}
	}
	return ws, nil
}

// BossList is one boss's list, as All reads it; Err is why it couldn't be read; Closed that its boss session is gone,
// Store then its file, to look its PRs up or tick it by.
type BossList struct {
	Boss   string
	Err    error
	Closed bool
	Store  Store
	List
}

// All is each dev boss session's list with items on c's server, by session name, only the bosses keep takes (nil
// takes all); none without a server. A list it can't read comes with its error, the others still read; a boss
// closed since list-sessions is left out. With closed, the lists with open items of the bosses closed on this server
// follow (Closed), each open item checked as boss closed unless its PR moved.
func All(c *tmux.Client, now time.Time, keep func(name string) bool, closed bool) ([]BossList, error) {
	type boss struct {
		Name string `tmux:"session_name"`
		Herd string `tmux:"@dev_herd"`
	}
	sessions, err := tmux.Query[boss](c, "list-sessions")
	if err != nil && !tmux.IsNoServer(err) {
		return nil, err
	}
	slices.SortFunc(sessions, func(a, b boss) int { return strings.Compare(a.Name, b.Name) })
	var lists []BossList
	live := map[string]bool{}
	for _, sess := range sessions {
		live[sessionKey(sess.Name)] = true
		if sess.Herd == "" || keep != nil && !keep(sess.Name) {
			continue
		}
		target := tmux.SessionTarget(sess.Name)
		s, err := ForPane(c, target)
		var l List
		if err == nil {
			l, err = s.Read(now)
		}
		if err != nil {
			if _, gone := c.Run("has-session", "-t", target); gone != nil {
				continue
			}
			lists = append(lists, BossList{Boss: sess.Name, Err: err, List: List{Items: []Shown{}, Stale: true}})
			continue
		}
		if len(l.Items) > 0 {
			lists = append(lists, BossList{Boss: sess.Name, List: l})
		}
	}
	if !closed || len(sessions) == 0 {
		return lists, nil
	}
	// Any session's socket, they share the server: the first still open
	var socket string
	for _, sess := range sessions {
		if first, err := paneSession(c, tmux.SessionTarget(sess.Name)); err == nil {
			socket = first.Socket
			break
		} else if _, gone := c.Run("has-session", "-t", tmux.SessionTarget(sess.Name)); gone == nil {
			return nil, err
		}
	}
	if socket == "" {
		return lists, nil
	}
	gone, err := closedLists(socket, live, now)
	if err != nil {
		return nil, err
	}
	return append(lists, gone...), nil
}

// closedLists are the lists with open items of the bosses closed on the server at socket, by name; one it can't read
// comes with its error. live holds the key (sessionKey) of each session on it: a session by a list's name, boss or not,
// isn't closed. A list's boss is the name it stored, only a boss's name (boss-<project>, manager) whose key is its
// file's: not another server's whose socket starts alike. An older dev's list stored none: its boss is its file's
// key (legacyBoss). A list it can't read is named by its file.
func closedLists(socket string, live map[string]bool, now time.Time) ([]BossList, error) {
	probe, err := open(socket + "-x")
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(filepath.Base(probe.path), "x.json")
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(probe.path), prefix+"*.json"))
	if err != nil {
		return nil, err
	}
	var out []BossList
	for _, path := range paths {
		s := Store{path: path}
		key := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), prefix), ".json")
		if live[key] {
			continue
		}
		name := key
		f, err := s.read()
		var l List
		if err == nil {
			name = cmp.Or(f.Boss, legacyBoss(key))
			l, err = s.Read(now)
		}
		if (name != sidebar.Manager && !strings.HasPrefix(name, "boss-")) || sessionKey(name) != key {
			continue
		}
		s.boss = name
		if err != nil {
			out = append(out, BossList{Boss: name, Err: err, Closed: true, Store: s, List: List{Items: []Shown{}, Stale: true}})
			continue
		}
		if n, _ := l.Open(); n == 0 {
			continue
		}
		for i, it := range l.Items {
			if !it.Done && (it.Check == nil || !it.Check.Event()) {
				l.Items[i].Check = &sidebar.Check{Kind: sidebar.CheckBossClosed, Since: l.Updated}
			}
		}
		out = append(out, BossList{Boss: name, Closed: true, Store: s, List: l})
	}
	slices.SortFunc(out, func(a, b BossList) int { return strings.Compare(a.Boss, b.Boss) })
	return out, nil
}

var (
	hashed = regexp.MustCompile(`_.*-[0-9a-f]{8}$`)
	// A list under a session id and creation time (ForPane's earlier key): another socket's, read under this one's
	byID = regexp.MustCompile(`-_\d+-\d+$`)
)

// legacyBoss is the boss of an older dev's list whose file's key, after its socket's, is key: key itself when a boss's
// name (boss-<project>, manager), "" otherwise. Not a key another server's socket may start, as with a socket
// default-boss-x: default's key boss-x-boss-y, boss-x-manager, boss-x-_7-1700000000. Nor a hashed key (sessionKey),
// whose name is lost.
func legacyBoss(key string) string {
	if key == sidebar.Manager || strings.HasPrefix(key, "boss-") && !strings.Contains(key, "-boss-") &&
		!strings.HasSuffix(key, "-"+sidebar.Manager) && !hashed.MatchString(key) && !byID.MatchString(key) {
		return key
	}
	return ""
}

// Read is the list as its boards show it at now; a missing file is an empty list.
func (s Store) Read(now time.Time) (List, error) {
	f, err := s.read()
	if err != nil {
		return List{Items: []Shown{}}, err
	}
	f.prune(now)
	facts, err := s.readChecks()
	if err != nil {
		return List{Items: []Shown{}}, err
	}
	l := List{Items: []Shown{}, Updated: f.Updated, Stale: f.Updated != 0 && now.Sub(time.Unix(f.Updated, 0)) >= stale}
	for _, it := range f.Items {
		sh := Shown{ID: it.ID, Text: it.Text, On: it.On, Session: it.Session, Link: it.Link, Done: it.Done != 0, Until: it.Until, Options: it.Options, Answer: it.Answer, Added: it.Added, DoneOn: it.DoneOn}
		if !sh.Done {
			sh.Check = check(it, facts[it.key()], now)
		}
		l.Items = append(l.Items, sh)
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
			f.Items[i] = Item{ID: i + 1, Text: t, Added: now.Unix()}
		}
		f.Ticked = nil
		f.Next = len(texts) + 1
		return nil
	})
}

// Add appends it and returns its id; ids restart at 1 once the list is empty, ticked items shown included.
func (s Store) Add(it Item, now time.Time) (id int, err error) {
	if err := it.checkDoneOn(); err != nil {
		return 0, err
	}
	err = s.update(now, func(f *file) error {
		if len(f.Items) == 0 {
			f.Next = 1
		}
		it.ID, it.Done, it.Added = max(f.Next, 1), 0, now.Unix()
		if it.On == Me {
			it.On, it.Session = "", nil
		}
		f.Items = append(f.Items, it)
		f.Next, id = it.ID+1, it.ID
		return nil
	})
	return id, err
}

// Replace puts it in the place of open item id, keeping its number and the Text, On (with its Session), Link, Until
// and DoneOn it doesn't set; On Me gives it back to the user. An id that isn't one is an error.
func (s Store) Replace(id int, it Item, now time.Time) error {
	return s.update(now, func(f *file) error {
		i := slices.IndexFunc(f.Items, func(old Item) bool { return old.ID == id && old.Done == 0 })
		if i < 0 {
			return fmt.Errorf("no open item %d", id)
		}
		old := f.Items[i]
		// A new Added even within its second: its checks start afresh
		it.ID, it.Done, it.Added = id, 0, max(now.Unix(), old.Added+1)
		switch it.On {
		case Me:
			it.On, it.Session = "", nil
		case "":
			it.On, it.Session = old.On, old.Session
		}
		it.Text, it.Link, it.Until = cmp.Or(it.Text, old.Text), cmp.Or(it.Link, old.Link), cmp.Or(it.Until, old.Until)
		it.DoneOn = cmp.Or(it.DoneOn, old.DoneOn)
		if it.Options == nil {
			it.Options = old.Options
		}
		if err := it.checkDoneOn(); err != nil {
			return err
		}
		f.Items[i] = it
		if it.Text == old.Text && it.Link == old.Link && it.On == old.On {
			return s.keepTold(old, it)
		}
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

// DoneText ticks the user's own open item id, only while it still reads text, as item numbers restart once a list
// empties and a boss may rewrite its list under a board's row.
func (s Store) DoneText(id int, text string, now time.Time) error {
	return s.update(now, func(f *file) error {
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
		switch {
		case i < 0:
			return fmt.Errorf("no open item %d", id)
		case f.Items[i].Text != text:
			return fmt.Errorf("item %d is %q now, not %q: look at the list again", id, f.Items[i].Text, text)
		case f.Items[i].On != "":
			return fmt.Errorf("item %d is on %s, not the user's to tick", id, f.Items[i].On)
		case len(f.Items[i].Options) > 0:
			return fmt.Errorf("item %d is a question: answer it with dev todo answer", id)
		}
		f.Items[i].Done = now.Unix()
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
// as item numbers restart once a list empties. A before other than 0 also refuses an item not added before it (unix seconds).
func (s Store) Answer(id int, question, arg string, before int64, now time.Time) (Item, error) {
	return s.AnswerPick(id, question, arg, "", before, now)
}

// AnswerPick is Answer refusing an answer other than picked, the user's pick for it, when given.
func (s Store) AnswerPick(id int, question, arg, picked string, before int64, now time.Time) (Item, error) {
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
		case before != 0 && p.Added == 0:
			return fmt.Errorf("item %d has no time added (an older dev's): the user answers it", id)
		case before != 0 && p.Added >= before:
			return fmt.Errorf("item %d is newer than the user's latest prompt: the user hasn't seen it", id)
		}
		answer, err := Pick(p.Options, arg)
		if err != nil {
			return err
		}
		if picked != "" {
			want, err := Pick(p.Options, picked)
			if err != nil {
				return fmt.Errorf("the user's pick %q: %w", picked, err)
			}
			if want != answer {
				return fmt.Errorf("the user picked %q, not %q", picked, answer)
			}
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
		f.Items, f.Next, f.Ticked = nil, 1, nil
		return nil
	})
}

// prune ticks the open items whose Until has come, as at Until, keeping them in Ticked, and drops the items ticked
// over doneShown ago.
func (f *file) prune(now time.Time) {
	for i, it := range f.Items {
		if it.Done == 0 && it.Until != 0 && now.Unix() >= it.Until {
			f.Items[i].Done = it.Until
			f.tell(Ticked{ID: it.ID, Text: it.Text})
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
		return s.write(f)
	})
}

// write writes f, with its boss's name when known; its caller holds the file's lock.
func (s Store) write(f file) error {
	if s.boss != "" {
		f.Boss = s.boss
	}
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

// startShown is how many open items the list a boss gets at its start names before "+N more".
const startShown = 20

// lines is the open items, numbered, one per line after a newline; past limit (0: none) a "+N more" line. Items to
// re-check (Flagged) come first under a line saying so, each with why, then the rest under "The rest:".
func (l List) lines(limit int, now time.Time) string {
	var flagged, rest []Shown
	for _, it := range l.Items {
		switch {
		case it.Done:
		case it.Flagged():
			flagged = append(flagged, it)
		default:
			rest = append(rest, it)
		}
	}
	var b strings.Builder
	n := 0
	write := func(it Shown) {
		if n++; limit > 0 && n > limit {
			return
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
		if it.DoneOn != "" {
			b.WriteString(" (done on " + it.DoneOn + ")")
		}
		if it.Check != nil {
			b.WriteString(" ⚠ " + it.Check.Long(now))
		}
	}
	if len(flagged) > 0 {
		b.WriteString("\nRe-check these first: tick it, replace it with what's true now, or confirm it holds (dev todo add --replaces <n>):")
		for _, it := range flagged {
			write(it)
		}
		if len(rest) > 0 && (limit == 0 || n < limit) {
			b.WriteString("\nThe rest:")
		}
	}
	for _, it := range rest {
		write(it)
	}
	if limit > 0 && n > limit {
		fmt.Fprintf(&b, "\n+%d more: dev todo", n-limit)
	}
	return b.String()
}

// Flagged reports an open item to re-check: its worker is gone, or it has a Check.
func (it Shown) Flagged() bool { return !it.Done && (it.Gone || it.Check != nil) }

// whoLine asks a boss to say who does each item, as the reminders end.
const whoLine = "\nAn item a worker session or someone else does, not the user: dev todo add --replaces <n> --on <worker name|person>."

// Reminder is the items ticked by themselves (tickedText), then the open items for a boss reading worker notes at now,
// numbered, with a line asking it to tick what the notes closed; "" when none ticked and none is open.
func (l List) Reminder(ticked []Ticked, now time.Time) string {
	items := l.lines(0, now)
	if items == "" {
		return tickedText(ticked)
	}
	return withTicked(ticked, "[dev todo] Your open to do list:"+items+
		"\nTick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> \"<new text>\"."+whoLine)
}

// StartReminder is the items ticked by themselves (tickedText), then the open items for a boss starting or /clear'ed
// at now, at most startShown, with how long ago the list changed (age, e.g. "2h ago") and, when kept, that an earlier
// boss session left it; "" when none ticked and none is open.
func (l List) StartReminder(kept bool, age string, ticked []Ticked, now time.Time) string {
	items := l.lines(startShown, now)
	if items == "" {
		return tickedText(ticked)
	}
	from := "updated " + age
	if kept {
		from = "kept from your last boss session, " + from
	}
	return withTicked(ticked, "[dev todo] Your open to do list ("+from+"):"+items+
		"\nCheck each still holds: tick what's done (dev todo done <n>), update what changed (dev todo add --replaces <n> \"<new text>\")."+whoLine)
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
