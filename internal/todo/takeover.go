package todo

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

const t3Prefix = "t3-"

// T3Name is the name of T3 Code thread id's list: t3: and the first 8 characters of the id as its file names it
// (mcp:0c63… is mcp_0c63…), so the name a take-over prints is one it takes back.
func T3Name(id string) string {
	id = T3ID(id)
	return "t3:" + id[:min(len(id), 8)]
}

// T3ID is T3 Code thread id as its list's file names it.
func T3ID(id string) string { return strings.Trim(unsafe.ReplaceAllString(id, "_"), "_") }

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
	// Listed is a row of the listing: not this boss's own, moved or empty (one not read is, with why); Other a list of
	// another or no known project the listing only counts
	Listed  bool `json:"-"`
	Other   bool `json:"-"`
	id      string
	store   Store
	refusal error
	unread  bool
}

// Store is the candidate's list.
func (c Candidate) Store() Store { return c.store }

// Short is its name as the listing prints it: a desktop app session id cut to local_ and 8 characters.
func (c Candidate) Short() string {
	if c.Kind == KindDesktop {
		id := strings.TrimPrefix(c.id, "local_")
		return "local_" + id[:min(len(id), 8)]
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
			c.refusal, c.WhyNot, c.unread = fmt.Errorf("%s not read: %w", c.List, err), "not read: "+err.Error(), true
		} else {
			c.count(f, now)
		}
		out = append(out, c)
	}
	if !slices.ContainsFunc(out, func(c Candidate) bool { return c.store.path == own.path }) {
		c := Candidate{List: own.Name(), Kind: own.Kind(), store: own}
		if c.Kind != KindTmux {
			c.id = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(filepath.Base(own.path), ".json"), t3Prefix), desktopPrefix)
		}
		out = append(out, c)
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
		c.classify(own, ownProject, stateErr, anyProject)
	}
	slices.SortStableFunc(out, func(a, b Candidate) int { return cmp.Compare(b.Updated, a.Updated) })
	return out, nil
}

// Kind is the kind of s's list by its file's name.
func (s Store) Kind() string {
	switch base := filepath.Base(s.path); {
	case strings.HasPrefix(base, t3Prefix):
		return KindT3
	case strings.HasPrefix(base, desktopPrefix):
		return KindDesktop
	}
	return KindTmux
}

// count sets c's open items, asks, notes and last change from its file f at now, and its moved marker's refusal; it keeps
// f's dir, which names a T3 Code list's project.
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

// classify sets why c can't be taken by own's boss, of project ownProject, stateErr being why its state
// is unknown; Listed and Other say where the listing puts it.
func (c *Candidate) classify(own Store, ownProject string, stateErr error, anyProject bool) {
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
		// Not read: listed with why; moved: left out
		c.Listed = c.unread
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

// takenStamp is the list last taken over into a list (TakeOver), by its key, and when: a take-over stopped before
// its marker is written leaves it, so its retry writes only the marker.
type takenStamp struct {
	TakenFrom string `json:"taken_from,omitempty"`
	TakenAt   int64  `json:"taken_at,omitempty"`
}

// TakenItem is an item TakeOver moved, its new number in ID, From its number before.
type TakenItem struct {
	From int
	Item
}

// TakenNote is a note TakeOver moved, its new number in ID, From its number before.
type TakenNote struct {
	From int
	Note
}

// Taken is what TakeOver moved: the open items and the notes, how many asks among the items, and the workers (by name)
// open items are on. Merged: into a list with OwnItems open items or OwnNotes notes, after them, renumbered; DroppedState
// is the older of two state notes, dropped.
type Taken struct {
	Items              []TakenItem
	Notes              []TakenNote
	Asks               int
	OnWorkers          []string
	Merged             bool
	OwnItems, OwnNotes int
	DroppedState       *Note
}

// TakeOver moves from's list into s: its items, notes, PR checks and finished items. Into a list with no open items
// and no notes, numbers kept; else merged after s's own, renumbered, the links between them, PR checks, and notes
// and ticks of the items moved following. Over MaxNotes together is refused; of two state notes the newer stays. from's file then
// holds a moved marker, which refuses its boss's writes (Moved). The move runs under both lists' locks and checks both
// again there: a list moved or emptied is refused (Refused), nothing moved. Whether from's boss runs is its caller's
// check.
func (s Store) TakeOver(from Store, now time.Time) (t Taken, err error) {
	if s.path == from.path {
		return t, Refused{fmt.Errorf("%s is this boss's own list", s.Name())}
	}
	err = lockedPair(s, from, func() error {
		// Both lists' items move: their bosses' next reminders are the full lists. First, so a failure moves nothing
		if err := errors.Join(from.forgetShown(), s.forgetShown()); err != nil {
			return err
		}
		var err error
		t, err = s.takeOver(from, now)
		return err
	})
	return t, err
}

// lockedPair runs fn under a's and b's locks, taken in path order: two moves of the same pair in opposite directions
// (a take-over, a desktop app's adopt) mustn't each hold one.
func lockedPair(a, b Store, fn func() error) error {
	if b.path < a.path {
		a, b = b, a
	}
	return a.locked(func() error { return b.locked(fn) })
}

// key is s's list's key: its file's name, as a moved marker and a taken stamp name it.
func (s Store) key() string { return strings.TrimSuffix(filepath.Base(s.path), ".json") }

// changedAround reports whether f has items or notes added at or before at, and whether it changed after it: an item
// or note added, or a write (Updated).
func (f file) changedAround(at int64) (before, after bool) {
	after = f.Updated > at
	added := func(t int64) {
		before, after = before || t <= at, after || t > at
	}
	for _, it := range f.Items {
		added(it.Added)
	}
	for _, n := range f.Notes {
		added(n.Added)
	}
	return before, after
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
	// An older dev's item on its own boss's name is the user's (Own): not once another boss has it
	for i, it := range src.Items {
		if src.Boss != "" && it.Session == nil && it.On == src.Boss {
			src.Items[i].On = ""
		}
	}
	// What it moves, as its boards show it now; a move moves the file itself as stored
	shown := src
	shown.Items, shown.Notes = slices.Clone(src.Items), slices.Clone(src.Notes)
	shown.prune(now)
	if shown.empty() {
		return t, Refused{fmt.Errorf("%s is empty (no open items, no notes): nothing to take", from.Name())}
	}
	if dst.TakenFrom == from.key() {
		// Only a list all added after the take is a fresh one under the same name (a dev boss reopened)
		switch before, after := src.changedAround(dst.TakenAt); {
		case !after:
			// The take stopped before the marker: its items are here already
			if err := s.finish(from, src, now); err != nil {
				return t, err
			}
			return t, Refused{fmt.Errorf("%s taken at %s: dev todo shows it", from.Name(), atTime(dst.TakenAt, now))}
		case before:
			return t, Refused{fmt.Errorf("%s was taken at %s and changed before its marker was written: taking it again would take its items twice; nothing moved",
				from.Name(), atTime(dst.TakenAt, now))}
		}
	}
	dst.prune(now)
	for _, it := range dst.Items {
		if it.Done == 0 {
			t.OwnItems++
		}
	}
	t.OwnNotes = len(dst.Notes)
	if t.OwnItems == 0 && t.OwnNotes == 0 {
		if err := s.move(from, src, dst, now); err != nil {
			return Taken{}, err
		}
		for _, it := range shown.Items {
			t.Items = append(t.Items, TakenItem{it.ID, it})
		}
		for _, n := range shown.Notes {
			t.Notes = append(t.Notes, TakenNote{n.ID, n})
		}
	} else {
		t.Merged = true
		if err := s.merge(from, shown, dst, &t, now); err != nil {
			return Taken{}, err
		}
	}
	t.Items = slices.DeleteFunc(t.Items, func(it TakenItem) bool { return it.Done != 0 })
	for _, it := range t.Items {
		if len(it.Options) > 0 {
			t.Asks++
		}
		if it.Session != nil && !slices.Contains(t.OnWorkers, it.Session.Name) {
			t.OnWorkers = append(t.OnWorkers, it.Session.Name)
		}
	}
	return t, nil
}

// marker is the moved marker from's list src leaves at now, pointing at s.
func (s Store) marker(src file, now time.Time) file {
	return file{Updated: now.Unix(), Boss: src.Boss, Dir: src.Dir, movedMarker: movedMarker{
		MovedTo: s.key(), MovedToName: s.Name(), MovedAt: now.Unix(),
	}}
}

// move moves from's file src into s, whose file dst has no open items and no notes, numbers kept.
func (s Store) move(from Store, src, dst file, now time.Time) error {
	// What lives beside the lists copied first, PR checks (dst has no open item a check is of) and finished items, then
	// s, then from's marker over its list: a failure between them leaves it in both, and a retry finishes (takenStamp)
	facts, err := from.readChecks()
	if err != nil {
		return err
	}
	if err := s.writeChecks(facts); err != nil {
		return err
	}
	// Its ticked items, both lists' finished ones: kept beside s
	if err := s.copyDone(from, slices.Concat(dst.dropped, dst.Items), now); err != nil {
		return err
	}
	moved := src
	moved.Boss, moved.Dir, moved.Updated = s.boss, s.dir, now.Unix()
	moved.takenStamp = takenStamp{TakenFrom: from.key(), TakenAt: now.Unix()}
	moved.Ticked = slices.Concat(dst.Ticked, src.Ticked)
	if err := s.write(moved); err != nil {
		return err
	}
	return s.finish(from, src, now)
}

// copyDone keeps from's finished items and items beside s's; from's stay until finish.
func (s Store) copyDone(from Store, items []Item, now time.Time) error {
	theirs, err := from.readDone(true)
	if err != nil {
		return err
	}
	return s.keepDone(slices.Concat(items, theirs), now)
}

// finish ends a take-over of from's list src into s: from's moved marker, then its PR checks and finished items,
// copied to s, removed.
func (s Store) finish(from Store, src file, now time.Time) error {
	if err := from.write(s.marker(src, now)); err != nil {
		return err
	}
	if err := ignoreMissing(os.Remove(from.checksPath())); err != nil {
		return err
	}
	return ignoreMissing(os.Remove(from.donePath()))
}

// merge moves from's list src, pruned at now, into s's dst, pruned too, after dst's items and notes, renumbered:
// t gets them with their numbers before and after, and the state note dropped. A note's item or a tick of an item src
// no longer shows loses its number, which may be one of dst's. Over MaxNotes together is refused, before anything is
// written. The PR checks and finished items are copied first, then s is written, stamped as taken from from, then finish.
func (s Store) merge(from Store, src, dst file, t *Taken, now time.Time) error {
	notes := src.Notes
	own := slices.IndexFunc(dst.Notes, func(n Note) bool { return n.Kind == NoteState })
	theirs := slices.IndexFunc(notes, func(n Note) bool { return n.Kind == NoteState })
	dropOwn := false
	if own >= 0 && theirs >= 0 {
		if dropOwn = notes[theirs].Added > dst.Notes[own].Added; dropOwn {
			t.DroppedState = &dst.Notes[own]
			dst.Notes = slices.Delete(slices.Clone(dst.Notes), own, own+1)
		} else {
			t.DroppedState = &notes[theirs]
			notes = slices.Delete(slices.Clone(notes), theirs, theirs+1)
		}
	}
	if n := len(dst.Notes) + len(notes); n > MaxNotes {
		return Refused{fmt.Errorf("taking %s makes %d notes, at most %d: drop %d here first (dev todo note drop <n>); nothing moved",
			from.Name(), n, MaxNotes, n-MaxNotes)}
	}
	next := max(dst.Next, 1)
	for _, it := range dst.Items {
		next = max(next, it.ID+1)
	}
	ids := map[int]int{}
	for _, it := range src.Items {
		ids[it.ID] = next
		next++
	}
	for _, it := range src.Items {
		old := it.ID
		it.ID, it.PartOf, it.For = ids[it.ID], ids[it.PartOf], ids[it.For]
		dst.Items = append(dst.Items, it)
		t.Items = append(t.Items, TakenItem{old, it})
	}
	nextNote := max(dst.NextNote, 1)
	for _, n := range dst.Notes {
		nextNote = max(nextNote, n.ID+1)
	}
	// Its numbers are never reused: not the dropped one's either
	if dropOwn {
		nextNote = max(nextNote, t.DroppedState.ID+1)
	}
	for _, n := range notes {
		old := n.ID
		n.ID, n.Item = nextNote, ids[n.Item]
		if n.ItemDone {
			// Its number may be another item's now: numbers restart once a list empties
			n.Item = 0
		}
		nextNote++
		dst.Notes = append(dst.Notes, n)
		t.Notes = append(t.Notes, TakenNote{old, n})
	}
	for _, tk := range src.Ticked {
		// Only its own item's number: one an item added since has is no longer its
		same := slices.ContainsFunc(src.Items, func(it Item) bool { return it.ID == tk.ID && it.Text == tk.Text })
		if tk.ID = ids[tk.ID]; !same {
			tk.ID = 0
		}
		dst.Ticked = append(dst.Ticked, tk)
	}
	dst.Next, dst.NextNote, dst.Updated = next, nextNote, now.Unix()
	dst.takenStamp = takenStamp{TakenFrom: from.key(), TakenAt: now.Unix()}
	dst.dropped = append(dst.dropped, src.dropped...)
	facts, err := s.readChecks()
	if err != nil {
		return err
	}
	srcFacts, err := from.readChecks()
	if err != nil {
		return err
	}
	for _, it := range t.Items {
		if f, ok := srcFacts[Item{ID: it.From, Added: it.Added}.key()]; ok {
			facts[it.key()] = f
		}
	}
	if err := s.writeChecks(facts); err != nil {
		return err
	}
	if err := s.copyDone(from, nil, now); err != nil {
		return err
	}
	if err := s.write(dst); err != nil {
		return err
	}
	return s.finish(from, src, now)
}

// Plural is "n word", an s added unless n is 1.
func Plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Empty reports s's list with no open items and no notes at now, not moved (a moved list's boss has a list elsewhere):
// one a take-over hint goes to.
func (s Store) Empty(now time.Time) (bool, error) {
	f, err := s.read()
	if err != nil || f.MovedTo != "" {
		return false, err
	}
	f.prune(now)
	return f.empty(), nil
}

// empty reports f with no open items and no notes.
func (f file) empty() bool {
	return !slices.ContainsFunc(f.Items, func(it Item) bool { return it.Done == 0 }) && len(f.Notes) == 0
}

// Kept reports whether s's list has a file: its boss wrote to it once.
func (s Store) Kept() (bool, error) {
	_, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
