package todo

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

const t3Prefix = "t3-"

// T3Name is the name of T3 Code thread id's list: t3: and the id's first 8 characters.
func T3Name(id string) string { return "t3:" + id[:min(len(id), 8)] }

// movedMarker is what a list taken over (TakeOver) keeps: the key and name of the list it went to, and when.
type movedMarker struct {
	MovedTo     string `json:"moved_to,omitempty"`
	MovedToName string `json:"moved_to_name,omitempty"`
	MovedAt     int64  `json:"moved_at,omitempty"`
}

// Moved refuses a change to a list another boss took over: it names where the list went, and when.
type Moved struct {
	To string
	At int64
}

func (m Moved) Error() string {
	return fmt.Sprintf("list moved to %s at %s (dev todo take-over): this boss's to do list is there now; take it back there with dev todo take-over %s",
		m.To, atTime(m.At, time.Now()), m.To)
}

// refuseMoved is the Refused Moved error of a change to f when it is a moved marker.
func (s Store) refuseMoved(f file) error {
	if f.MovedTo == "" {
		return nil
	}
	return Refused{Moved{To: f.MovedToName, At: f.MovedAt}}
}

// movedText is where f went, for a read at now: "" unless it is a moved marker.
func (f file) movedText(now time.Time) string {
	if f.MovedTo == "" {
		return ""
	}
	return fmt.Sprintf("list moved to %s at %s (dev todo take-over)", f.MovedToName, atTime(f.MovedAt, now))
}

// atTime is unix time at as a moved marker shows it: its minute, with its date unless on now's day.
func atTime(at int64, now time.Time) string {
	t := time.Unix(at, 0)
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format(UntilLayout)
}

// WithDir is s writing dir, the main checkout its own T3 Code boss runs in, on each write: it names the list's project.
func (s Store) WithDir(dir string) Store {
	s.dir = dir
	return s
}

// Name is the list's name for take-over: its tmux boss's name, desktop app session id or T3Name.
func (s Store) Name() string { return cmp.Or(s.name, s.boss) }

// Kinds of a list (Candidate.Kind).
const (
	KindTmux    = "tmux"
	KindDesktop = "desktop"
	KindT3      = "t3"
)

// States of a list's boss (Candidate.State): a T3 Code thread's is unknown, as dev can't see T3 Code's threads.
const (
	StateRunning = "running"
	StateClosed  = "closed"
	StateStopped = "stopped"
	StateUnknown = "unknown"
)

// DesktopState is a desktop app session's folder (its originCwd) and whether its Claude runs.
type DesktopState struct {
	Dir     string
	Running bool
}

// Looks is what Candidates asks of the world. Socket is this tmux server's socket path: its bosses' lists are the tmux
// ones. Sessions are the session names on it, none without a server; Desktop the states of app sessions by id; Project
// the project of a folder.
type Looks struct {
	Socket   string
	Sessions func() ([]string, error)
	Desktop  func(ids []string) (map[string]DesktopState, error)
	Project  func(dir string) string
}

// Candidate is a list a boss may take over (dev todo take-over): its name (List), Kind, Project ("" unknown), its
// boss's State, Open items (Asks among them), Notes, last change (Updated, the newest note's included), and when it
// can't be taken, why (WhyNot; refusal is the longer error a take of it gives).
type Candidate struct {
	List     string `json:"list"`
	Kind     string `json:"kind"`
	Project  string `json:"project,omitempty"`
	State    string `json:"state"`
	Open     int    `json:"open"`
	Asks     int    `json:"asks"`
	Notes    int    `json:"notes"`
	Updated  int64  `json:"updated"`
	Takeable bool   `json:"takeable"`
	WhyNot   string `json:"why_not,omitempty"`
	// Listed is a row of the listing: not this boss's own, moved or empty; Other a list of another or no known project
	// the listing only counts
	Listed  bool `json:"-"`
	Other   bool `json:"-"`
	id      string
	store   Store
	refusal error
}

// Store is the candidate's list.
func (c Candidate) Store() Store { return c.store }

// Short is its name as the listing prints it: a desktop app session id cut to local_ and 8 characters.
func (c Candidate) Short() string {
	if c.Kind == KindDesktop {
		return "local_" + strings.TrimPrefix(c.id, "local_")[:min(len(strings.TrimPrefix(c.id, "local_")), 8)]
	}
	return c.List
}

// ID is its boss's id: a T3 Code thread's, a desktop app session's, a tmux boss's name.
func (c Candidate) ID() string { return cmp.Or(c.id, c.List) }

// Refusal is why a take of it is refused, nil when it is takeable.
func (c Candidate) Refusal() error { return c.refusal }

// Candidates are the lists beside own, own's among them (it may have no file yet), classified for a boss of project
// ownProject ("" unknown) at now, newest change first: a tmux boss's on this server, a desktop app session's, a T3 Code
// thread's. Only with anyProject are another project's, the manager's and those of no known project takeable.
func Candidates(own Store, ownProject string, looks Looks, anyProject bool, now time.Time) ([]Candidate, error) {
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(own.path), "*.json"))
	if err != nil {
		return nil, err
	}
	prefix := ""
	if looks.Socket != "" {
		probe, err := open(looks.Socket + "-x")
		if err != nil {
			return nil, err
		}
		prefix = strings.TrimSuffix(filepath.Base(probe.path), "x.json")
	}
	var out []Candidate
	var desktopIDs []string
	for _, path := range paths {
		base := strings.TrimSuffix(filepath.Base(path), ".json")
		c := Candidate{store: Store{path: path}}
		f, err := c.store.read()
		switch {
		case strings.HasPrefix(base, t3Prefix):
			c.id = strings.TrimPrefix(base, t3Prefix)
			c.Kind, c.List = KindT3, T3Name(c.id)
		case strings.HasPrefix(base, desktopPrefix):
			c.id = strings.TrimPrefix(base, desktopPrefix)
			c.Kind, c.List = KindDesktop, c.id
			desktopIDs = append(desktopIDs, c.id)
		case prefix != "" && strings.HasPrefix(base, prefix):
			key := strings.TrimPrefix(base, prefix)
			name := key
			if err == nil {
				name = cmp.Or(f.Boss, legacyBoss(key))
			}
			// As closedLists: only a boss's list under its own name's key
			if (name != sidebar.Manager && !strings.HasPrefix(name, "boss-")) || sessionKey(name) != key {
				continue
			}
			c.Kind, c.List, c.store.boss = KindTmux, name, name
		default:
			continue
		}
		c.store.name = c.List
		if err != nil {
			c.refusal, c.WhyNot = fmt.Errorf("%s not read: %w", c.List, err), "not read: "+err.Error()
		} else {
			c.count(f, now)
		}
		out = append(out, c)
	}
	if !slices.ContainsFunc(out, func(c Candidate) bool { return c.store.path == own.path }) {
		out = append(out, Candidate{List: own.Name(), Kind: kindOf(own), store: own})
	}
	var states map[string]DesktopState
	var statesErr error
	if len(desktopIDs) > 0 {
		states, statesErr = looks.Desktop(desktopIDs)
	}
	var sessions []string
	var sessionsErr error
	if slices.ContainsFunc(out, func(c Candidate) bool { return c.Kind == KindTmux }) {
		sessions, sessionsErr = looks.Sessions()
	}
	for i := range out {
		c := &out[i]
		var stateErr error
		switch c.Kind {
		case KindTmux:
			c.State, stateErr = StateClosed, sessionsErr
			if slices.Contains(sessions, c.List) {
				c.State = StateRunning
			}
			if c.List != sidebar.Manager {
				c.Project = strings.TrimPrefix(c.List, "boss-")
			}
		case KindDesktop:
			c.State, stateErr = StateStopped, statesErr
			if st := states[c.id]; stateErr == nil {
				if st.Running {
					c.State = StateRunning
				}
				if st.Dir != "" {
					c.Project = looks.Project(st.Dir)
				}
			}
		case KindT3:
			c.State = StateUnknown
			if c.store.dir != "" {
				c.Project = looks.Project(c.store.dir)
			}
		}
		c.classify(own, ownProject, stateErr, anyProject, now)
	}
	slices.SortStableFunc(out, func(a, b Candidate) int { return cmp.Compare(b.Updated, a.Updated) })
	return out, nil
}

// kindOf is the kind of s's list by its file's name.
func kindOf(s Store) string {
	switch base := filepath.Base(s.path); {
	case strings.HasPrefix(base, t3Prefix):
		return KindT3
	case strings.HasPrefix(base, desktopPrefix):
		return KindDesktop
	}
	return KindTmux
}

// count sets c's open items, asks, notes and last change from its file f at now, and its moved marker's refusal; it keeps
// f's dir, a T3 Code list's project.
func (c *Candidate) count(f file, now time.Time) {
	f.Items, f.Notes = slices.Clone(f.Items), slices.Clone(f.Notes)
	f.prune(now)
	for _, it := range f.Items {
		if it.Done == 0 {
			c.Open++
			if len(it.Options) > 0 {
				c.Asks++
			}
		}
	}
	c.Notes, c.Updated = len(f.Notes), f.Updated
	for _, n := range f.Notes {
		c.Updated = max(c.Updated, n.Added)
	}
	if f.MovedTo != "" {
		c.refusal = fmt.Errorf("%s moved to %s at %s: nothing left to take", c.List, f.MovedToName, atTime(f.MovedAt, now))
		c.WhyNot = "moved to " + f.MovedToName
	}
	c.store.dir = f.Dir
}

// classify sets why c can't be taken by own's boss, of project ownProject, stateErr being why its state is unknown;
// Listed and Other say where the listing puts it.
func (c *Candidate) classify(own Store, ownProject string, stateErr error, anyProject bool, now time.Time) {
	refuse := func(why string, err error) {
		if c.refusal == nil {
			c.WhyNot, c.refusal = why, err
		}
	}
	switch {
	case c.store.path == own.path:
		c.WhyNot, c.refusal = "this boss's own list", fmt.Errorf("%s is this boss's own list", c.List)
		return
	case c.refusal != nil:
		// Not read, or moved
		return
	case c.Open == 0 && c.Notes == 0:
		refuse("empty", fmt.Errorf("%s is empty (no open items, no notes): nothing to take", c.List))
		return
	}
	c.Listed = true
	switch {
	case stateErr != nil:
		// Its project may be unknown for it too: listed, so it isn't counted as another project's
		refuse("state not read: "+stateErr.Error(), fmt.Errorf("%s: can't tell whether its boss runs: %w", c.List, stateErr))
		return
	case c.State == StateRunning && c.Kind == KindTmux:
		refuse("running: close it first", fmt.Errorf("%s is running: close it first (its manager's ×, or /exit in it), then take it over", c.List))
	case c.State == StateRunning:
		refuse("running: close it first", fmt.Errorf("%s is running: close it first (/exit in it), then take it over", c.List))
	}
	if anyProject || c.Project != "" && c.Project == ownProject {
		c.Takeable = c.refusal == nil
		return
	}
	c.Listed, c.Other = false, true
	mine := cmp.Or(ownProject, "no known project")
	switch {
	case c.Kind == KindTmux && c.List == sidebar.Manager:
		refuse("the manager's", fmt.Errorf("%s is the manager's list, not %s's: --any-project takes it", c.List, mine))
	case c.Project == "":
		refuse("no known project", fmt.Errorf("%s is of no known project, not %s: --any-project takes it", c.List, mine))
	default:
		refuse("project "+c.Project, fmt.Errorf("%s is project %s, not %s: --any-project takes it", c.List, c.Project, mine))
	}
}

// Taken is what TakeOver moved: the open items and the notes, numbers kept, how many asks among the items, and the
// workers (by name) open items are on.
type Taken struct {
	Items     []Item
	Notes     []Note
	Asks      int
	OnWorkers []string
}

// TakeOver moves from's list into s, which has no open items and no notes: its items, notes, PR checks and finished
// items, numbers kept; s's own finished items stay. from's file then holds a moved marker, which refuses its boss's
// writes (Moved). The move runs under both lists' locks and checks both again there: a list moved, emptied or
// a destination no longer empty is refused (Refused), nothing moved.
func (s Store) TakeOver(from Store, now time.Time) (t Taken, err error) {
	if s.path == from.path {
		return t, Refused{fmt.Errorf("%s is this boss's own list", s.Name())}
	}
	// One order for every pair: two bosses may take each other's lists at once
	first, second := s, from
	if from.path < s.path {
		first, second = from, s
	}
	err = first.locked(func() error {
		return second.locked(func() error {
			var err error
			t, err = s.takeOver(from, now)
			return err
		})
	})
	return t, err
}

// takeOver is TakeOver under both locks.
func (s Store) takeOver(from Store, now time.Time) (Taken, error) {
	var t Taken
	dst, err := s.read()
	if err != nil {
		return t, err
	}
	src, err := from.read()
	if err != nil {
		return t, err
	}
	if src.MovedTo != "" {
		return t, Refused{fmt.Errorf("%s moved to %s at %s: nothing left to take", from.Name(), src.MovedToName, atTime(src.MovedAt, now))}
	}
	// What it moves, as its boards show it now; the file itself moves as stored
	shown := src
	shown.Items, shown.Notes = slices.Clone(src.Items), slices.Clone(src.Notes)
	shown.prune(now)
	for _, it := range shown.Items {
		if it.Done != 0 {
			continue
		}
		t.Items = append(t.Items, it)
		if len(it.Options) > 0 {
			t.Asks++
		}
		if it.Session != nil && !slices.Contains(t.OnWorkers, it.Session.Name) {
			t.OnWorkers = append(t.OnWorkers, it.Session.Name)
		}
	}
	t.Notes = shown.Notes
	if len(t.Items) == 0 && len(t.Notes) == 0 {
		return t, Refused{fmt.Errorf("%s is empty (no open items, no notes): nothing to take", from.Name())}
	}
	dst.prune(now)
	if n := slices.IndexFunc(dst.Items, func(it Item) bool { return it.Done == 0 }); n >= 0 || len(dst.Notes) > 0 {
		open := 0
		for _, it := range dst.Items {
			if it.Done == 0 {
				open++
			}
		}
		return t, Refused{fmt.Errorf("this boss's list has %s: taking over into a list with items or notes comes later: tick or drop them first (dev todo done <n>, dev todo note drop <n>)",
			counted(open, "open item", len(dst.Notes), "note"))}
	}
	// Its ticked items, both lists' finished ones: kept beside s
	kept, err := s.readDone(true)
	if err != nil {
		return t, err
	}
	theirs, err := from.readDone(true)
	if err != nil {
		return t, err
	}
	if err := s.saveDone(finished(slices.Concat(kept, dst.dropped, dst.Items, theirs), now)); err != nil {
		return t, err
	}
	if err := ignoreMissing(os.Remove(from.donePath())); err != nil {
		return t, err
	}
	if err := os.MkdirAll(filepath.Dir(s.checksPath()), 0o700); err != nil {
		return t, err
	}
	if err := ignoreMissing(os.Rename(from.checksPath(), s.checksPath())); err != nil {
		return t, err
	}
	// A rename first: a failure from here on leaves the list in one place, never two
	if err := os.Rename(from.path, s.path); err != nil {
		return t, err
	}
	moved := src
	moved.Boss, moved.Dir, moved.Updated = s.boss, s.dir, now.Unix()
	moved.Ticked = slices.Concat(dst.Ticked, src.Ticked)
	if err := s.write(moved); err != nil {
		return t, err
	}
	marker := file{Updated: now.Unix(), Boss: src.Boss, Dir: src.Dir, movedMarker: movedMarker{
		MovedTo: strings.TrimSuffix(filepath.Base(s.path), ".json"), MovedToName: s.Name(), MovedAt: now.Unix(),
	}}
	return t, from.write(marker)
}

// counted is "n <one>s and m <other>s", each singular for 1.
func counted(n int, one string, m int, other string) string {
	plural := func(k int, w string) string {
		if k == 1 {
			return fmt.Sprintf("%d %s", k, w)
		}
		return fmt.Sprintf("%d %ss", k, w)
	}
	return plural(n, one) + " and " + plural(m, other)
}
