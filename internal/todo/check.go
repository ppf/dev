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
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// LookupTTL is how long an item's PR lookup holds before the inbox watcher looks it up again; lookupFailing how long
// its lookups fail before the item says so.
const (
	LookupTTL     = 10 * time.Minute
	lookupFailing = 2 * LookupTTL
)

// prSeen is a linked PR as a lookup saw it; At, ReviewAt and ChecksAt are when its state, review and checks were first
// seen so (At: when it merged or closed, for those).
type prSeen struct {
	State    string `json:"state"`
	Review   string `json:"review,omitempty"`
	Checks   string `json:"checks,omitempty"`
	At       int64  `json:"at"`
	ReviewAt int64  `json:"review_at,omitempty"`
	ChecksAt int64  `json:"checks_at,omitempty"`
}

// since keeps old's times for what p sees as old did.
func (p prSeen) since(old prSeen) prSeen {
	if p.State == old.State {
		p.At = old.At
	}
	if p.Review == old.Review {
		p.ReviewAt = old.ReviewAt
	}
	if p.Checks == old.Checks {
		p.ChecksAt = old.ChecksAt
	}
	return p
}

var githubPR = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/pull/(\d+)(?:[/?#]|$)`)

// lookedUp is the number of the GitHub PR link names, which gh looks up; 0 for another link (a GitLab MR's too).
func lookedUp(link string) int {
	m := githubPR.FindStringSubmatch(link)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// fact is what lookups found of an item's PR: First is its state at the first lookup after the item was added or
// replaced, Last the latest; Checked when it was last looked up, Err why that failed, Failing since when its
// lookups fail (0 while they don't). Told is when its boss was last told each reason (Nudges).
type fact struct {
	Link    string           `json:"link"`
	First   prSeen           `json:"first"`
	Last    prSeen           `json:"last"`
	Checked int64            `json:"checked"`
	Err     string           `json:"err,omitempty"`
	Failing int64            `json:"failing,omitempty"`
	Told    map[string]int64 `json:"told,omitempty"`
}

// key is the item's facts key: its id and when it was added, so a replaced item starts its PR's baseline afresh.
func (it Item) key() string { return strconv.Itoa(it.ID) + ":" + strconv.FormatInt(it.Added, 10) }

// checksPath is the file of the list's facts: beside the lists, not among them, so it is never read as a list.
func (s Store) checksPath() string {
	return filepath.Join(filepath.Dir(s.path), "checks", filepath.Base(s.path))
}

// readChecks loads the list's facts; none without a file, nor with one that isn't JSON, which it removes: a cache, the
// next lookups rebuild it.
func (s Store) readChecks() (map[string]fact, error) {
	facts := map[string]fact{}
	data, err := os.ReadFile(s.checksPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return facts, nil
	case err != nil:
		return nil, err
	}
	if json.Unmarshal(data, &facts) != nil {
		return map[string]fact{}, ignoreMissing(os.Remove(s.checksPath()))
	}
	return facts, nil
}

// check is why open item it may be out of date at now: its PR merged or closed, its lookups failing, its PR's move
// since its first lookup, in that order; else unchanged for two days, unless an ask or dated (Until to come); nil otherwise.
func check(it Item, f fact, now time.Time) *sidebar.Check {
	pr := lookedUp(it.Link)
	if pr != 0 && f.Link == it.Link {
		last, first := f.Last, f.First
		switch {
		case last.State == "MERGED":
			return &sidebar.Check{Kind: sidebar.CheckMerged, PR: pr, Since: last.At}
		case last.State == "CLOSED":
			return &sidebar.Check{Kind: sidebar.CheckClosed, PR: pr, Since: last.At}
		case f.Failing != 0 && f.Checked-f.Failing >= int64(lookupFailing/time.Second):
			return &sidebar.Check{Kind: sidebar.CheckLookupFailing, PR: pr, Since: f.Failing}
		case last.State == "":
		case last.Review != first.Review && last.Review == "APPROVED":
			return &sidebar.Check{Kind: sidebar.CheckApproved, PR: pr, Since: last.ReviewAt}
		case last.Review != first.Review && last.Review == "CHANGES_REQUESTED":
			return &sidebar.Check{Kind: sidebar.CheckChanges, PR: pr, Since: last.ReviewAt}
		case last.Checks != first.Checks && last.Checks == sidebar.CIPassed:
			return &sidebar.Check{Kind: sidebar.CheckGreen, PR: pr, Since: last.ChecksAt}
		case last.Checks != first.Checks && last.Checks == sidebar.CIFailed:
			return &sidebar.Check{Kind: sidebar.CheckFailing, PR: pr, Since: last.ChecksAt}
		}
	}
	if it.stale(now) {
		return &sidebar.Check{Kind: sidebar.CheckUnchanged, Since: it.Added}
	}
	return nil
}

// stale reports an open item unchanged for two days at now: not an ask, a dated item before its Until, nor the boss's
// own while no one does it (OnBoss, not Doing; started too).
func (it Item) stale(now time.Time) bool {
	parked := OnBoss(it.On, it.Session) && it.Doing == nil
	return it.Added != 0 && now.Sub(time.Unix(it.Added, 0)) >= unchanged && len(it.Options) == 0 && it.Until <= now.Unix() && !parked
}

// A linked PR's state as Shown.PRState gives it.
const (
	PROpen   = "open"
	PRMerged = "merged"
	PRClosed = "closed"
)

// prState is the state of the GitHub PR open item it links to, as its last lookup saw it (gh's OPEN, MERGED, CLOSED
// in lower case); "" before one, or for another link.
func prState(it Item, f fact) string {
	if lookedUp(it.Link) == 0 || f.Link != it.Link {
		return ""
	}
	return strings.ToLower(f.Last.State)
}

// Lookup looks up the PRs the open items link to, those not looked up within ttl, at most limit of them (0: all),
// with view, and keeps what it finds beside the list, not in it, then ticks the --done-on items whose PR it finds so
// (Ticked): the list's Updated stays the boss's last change. A failed lookup is kept as the item's Err and tried again after ttl; a lookup older than one kept meanwhile is
// dropped; err is a file it couldn't read or write.
func (s Store) Lookup(now time.Time, ttl time.Duration, limit int, view func(url string) (sidebar.PRView, error)) error {
	var due []Item
	err := s.locked(func() error {
		f, facts, err := s.readBoth(now)
		if err != nil {
			return err
		}
		for _, it := range f.Items {
			if it.Done != 0 || lookedUp(it.Link) == 0 {
				continue
			}
			fc, ok := facts[it.key()]
			expired := !ok || fc.Link != it.Link || now.Sub(time.Unix(fc.Checked, 0)) >= ttl
			if expired && (limit == 0 || len(due) < limit) {
				due = append(due, it)
			}
		}
		return nil
	})
	if err != nil || len(due) == 0 {
		return err
	}
	began := time.Now()
	// gh runs outside the lock: a slow one mustn't hold up the list's other readers
	got := map[string]fact{}
	for _, it := range due {
		p, err := view(it.Link)
		fc := fact{Link: it.Link, Checked: now.Unix()}
		if err != nil {
			fc.Err, fc.Failing = err.Error(), now.Unix()
		} else {
			fc.Last = prSeen{State: p.State, Review: p.Review, Checks: p.Checks, At: now.Unix(), ReviewAt: now.Unix(), ChecksAt: now.Unix()}
			if !p.Ended.IsZero() {
				fc.Last.At = p.Ended.Unix()
			}
		}
		got[it.key()] = fc
	}
	return s.locked(func() error {
		// The list as it is now: an item added meanwhile keeps the facts another Lookup wrote for it
		f, facts, err := s.readBoth(now)
		if err != nil {
			return err
		}
		open := map[string]bool{}
		for _, it := range f.Items {
			if it.Done == 0 {
				open[it.key()] = true
			}
		}
		for k := range facts {
			if !open[k] {
				delete(facts, k)
			}
		}
		for k, fc := range got {
			old, ok := facts[k]
			switch {
			case !open[k], ok && old.Checked > fc.Checked:
				continue
			case fc.Err != "" && ok && old.Link == fc.Link:
				old.Checked, old.Err, old.Failing = fc.Checked, fc.Err, cmp.Or(old.Failing, fc.Failing)
				fc = old
			case fc.Err != "":
			case !ok || old.Link != fc.Link || old.First.State == "":
				fc.First = fc.Last
			default:
				fc.First, fc.Last = old.First, fc.Last.since(old.Last)
			}
			if ok {
				fc.Told = old.Told
			}
			facts[k] = fc
		}
		ticked := false
		for i, it := range f.Items {
			fc := facts[it.key()]
			if it.Done == 0 && it.DoneOn != "" && fc.Link == it.Link && reached(it.DoneOn, fc.Last) {
				// Ticked as written, after the lookups: its doneShown starts there
				f.Items[i].tick(now.Add(time.Since(began)).Unix(), DoneByPR)
				ticked = true
				f.tell(Ticked{ID: it.ID, Text: it.Text, PR: lookedUp(it.Link), On: it.DoneOn})
			}
		}
		if ticked {
			// Not the boss's change: Updated stays
			if err := s.write(f); err != nil {
				return err
			}
		}
		return s.writeChecks(facts)
	})
}

// The PR states --done-on names: an item ticks by itself once a lookup finds its PR so.
const (
	OnMerged   = "merged"
	OnApproved = "approved"
	OnGreen    = "green"
)

// DoneOns are the --done-on states.
var DoneOns = []string{OnMerged, OnApproved, OnGreen}

// reached reports p, a PR as a lookup saw it, in the state on: approved or its checks passed while open; merged ticks
// any.
func reached(on string, p prSeen) bool {
	switch {
	case p.State == "MERGED":
		// Past approved and green, whatever its review or checks say
		return true
	case p.State != "OPEN":
		return false
	case on == OnApproved:
		return p.Review == "APPROVED"
	case on == OnGreen:
		return p.Checks == sidebar.CIPassed
	}
	return false
}

// checkDoneOn is why it can't have its DoneOn: not a DoneOns state, or no GitHub PR link to look up.
func (it Item) checkDoneOn() error {
	switch {
	case it.DoneOn == "":
		return nil
	case len(it.Options) > 0:
		return errors.New("done on: an ask waits on the user's answer, not a PR")
	case !slices.Contains(DoneOns, it.DoneOn):
		return fmt.Errorf("done on %q: want %s", it.DoneOn, strings.Join(DoneOns, ", "))
	case lookedUp(it.Link) == 0:
		return fmt.Errorf("done on %s: needs --link to a GitHub PR, which gh looks up", it.DoneOn)
	}
	return nil
}

// Ticked is an item that ticked by itself: on its PR's move (PR, On its --done-on), else at its --until time.
type Ticked struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	PR   int    `json:"pr,omitempty"`
	On   string `json:"on,omitempty"`
}

// String is the tick as its boss is told it: `#8 "Second review of #415" ticked by itself: PR #415 approved (--done-on
// approved)`, or `… ticked by itself: its --until time came`.
func (t Ticked) String() string {
	why := "its --until time came"
	if t.PR != 0 {
		state := t.On
		if t.On == OnGreen {
			state = sidebar.CheckGreen
		}
		why = fmt.Sprintf("PR #%d %s (--done-on %s)", t.PR, state, t.On)
	}
	if t.ID == 0 {
		// Its item left the list before a take-over renumbered it
		return fmt.Sprintf("%q ticked by itself: %s", t.Text, why)
	}
	return fmt.Sprintf("#%d %q ticked by itself: %s", t.ID, t.Text, why)
}

// tell keeps t for the boss to be told.
func (f *file) tell(t Ticked) { f.Ticked = append(f.Ticked, t) }

// tickedText is ticked as a reminder's lines, "" for none.
func tickedText(ticked []Ticked) string {
	if len(ticked) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[dev todo] Ticked by itself since you last heard:")
	for _, t := range ticked {
		b.WriteString("\n" + t.String())
	}
	b.WriteString("\nAdd what comes next, if anything.")
	return b.String()
}

// withTicked is reminder after ticked's lines (tickedText).
func withTicked(ticked []Ticked, reminder string) string {
	if t := tickedText(ticked); t != "" {
		return t + "\n" + reminder
	}
	return reminder
}

// TakeTicked removes and returns the items that ticked by themselves at now, those take accepts (nil: all), for their
// boss to be told once; with tell, only once tell took them.
func (s Store) TakeTicked(now time.Time, take func(Ticked) bool, tell func([]Ticked) error) ([]Ticked, error) {
	var taken []Ticked
	err := s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		f.prune(now)
		var kept []Ticked
		for _, t := range f.Ticked {
			if take == nil || take(t) {
				taken = append(taken, t)
			} else {
				kept = append(kept, t)
			}
		}
		if len(taken) == 0 {
			return nil
		}
		if tell != nil {
			if err := tell(taken); err != nil {
				return err
			}
		}
		f.Ticked = kept
		// Not the boss's change: Updated stays
		return s.write(f)
	})
	return taken, err
}

// keepTold carries what old's boss was told (Nudges) over to it, which replaced it keeping its text, link and who does
// it: a reason isn't told again; its PR's moves count from its next lookup, its age from now.
func (s Store) keepTold(old, it Item) error {
	if s.trial {
		return nil
	}
	facts, err := s.readChecks()
	if err != nil || facts[old.key()].Told == nil {
		return err
	}
	facts[it.key()] = fact{Told: facts[old.key()].Told}
	return s.writeChecks(facts)
}

// Nudge is an open item its boss is told once about: Why it may be out of date.
type Nudge struct {
	ID     int
	Text   string
	On     string
	OnBoss bool // the boss's own (OnBoss), not a worker named boss
	Why    string
}

// String is the nudge as its boss is told it: `#5 "Deploy the cache fix after #418 merges" (on Tomas): PR #418 merged 40m ago`.
func (n Nudge) String() string {
	on := ""
	switch {
	case n.OnBoss:
		on = " (yours, --on boss)"
	case n.On != "":
		on = " (on " + n.On + ")"
	}
	return fmt.Sprintf("#%d %q%s: %s", n.ID, n.Text, on, n.Why)
}

// Reasons an item's boss is told it may be out of date, besides its PR's moves (the sidebar.Check kinds).
const (
	toldGone      = "gone"
	toldUnchanged = sidebar.CheckUnchanged
)

// Nudges are the open items to tell their boss about at now, each reason once, keeping that it was told: its PR
// merged, closed, approved, changes requested, checks green or failing since its first lookup; the worker it is on
// (Session) not among workers any more; unchanged for two days (as check, an ask and a dated item aside), told again
// after two more days without a --replaces. workers is read only when an open item is on one; with tell, they count
// as told only once tell took them. The facts of items no longer open go.
func (s Store) Nudges(now time.Time, workers func() ([]Session, error), tell func([]Nudge) error) ([]Nudge, error) {
	var out []Nudge
	err := s.locked(func() error {
		f, facts, err := s.readBoth(now)
		if err != nil {
			return err
		}
		openItems := slices.DeleteFunc(slices.Clone(f.Items), func(it Item) bool { return it.Done != 0 })
		l := List{}
		for _, it := range openItems {
			l.Items = append(l.Items, Shown{ID: it.ID, Session: it.Session})
		}
		if err := l.MarkGone(workers); err != nil {
			return err
		}
		told := false
		isOpen := map[string]bool{}
		for i, it := range openItems {
			isOpen[it.key()] = true
			fc := facts[it.key()]
			tell := func(reason, why string) {
				if fc.Told == nil {
					fc.Told = map[string]int64{}
				}
				fc.Told[reason], told = now.Unix(), true
				out = append(out, Nudge{ID: it.ID, Text: it.Text, On: it.On, OnBoss: OnBoss(it.On, it.Session), Why: why})
			}
			for _, c := range moves(it, fc) {
				if fc.Told[c.Kind] == 0 {
					tell(c.Kind, c.Long(now))
				}
			}
			if l.Items[i].Gone && fc.Told[toldGone] == 0 {
				tell(toldGone, "its session is gone")
			}
			if it.stale(now) && now.Sub(time.Unix(fc.Told[toldUnchanged], 0)) >= unchanged {
				tell(toldUnchanged, (&sidebar.Check{Kind: sidebar.CheckUnchanged, Since: it.Added}).Long(now))
			}
			if fc.Told != nil {
				facts[it.key()] = fc
			}
		}
		for k := range facts {
			if !isOpen[k] {
				delete(facts, k)
				told = true
			}
		}
		if !told {
			return nil
		}
		if len(out) > 0 && tell != nil {
			if err := tell(out); err != nil {
				return err
			}
		}
		return s.writeChecks(facts)
	})
	return out, err
}

// moves are the moves of its PR since its first lookup: merged or closed, its review approved or changes requested,
// its checks green or failing.
func moves(it Item, f fact) []sidebar.Check {
	pr := lookedUp(it.Link)
	first, last := f.First, f.Last
	if pr == 0 || f.Link != it.Link || first.State == "" || last.State == "" {
		return nil
	}
	// A merge or close ends the PR: its review and checks are no news then
	if kind, ok := stateMoves[last.State]; ok && last.State != first.State {
		return []sidebar.Check{{Kind: kind, PR: pr, Since: last.At}}
	}
	var out []sidebar.Check
	if kind, ok := reviewMoves[last.Review]; ok && last.Review != first.Review {
		out = append(out, sidebar.Check{Kind: kind, PR: pr, Since: last.ReviewAt})
	}
	if kind, ok := checksMoves[last.Checks]; ok && last.Checks != first.Checks {
		out = append(out, sidebar.Check{Kind: kind, PR: pr, Since: last.ChecksAt})
	}
	return out
}

// The sidebar.Check kind of each PR state, review and checks a move ends in (moves).
var (
	stateMoves  = map[string]string{"MERGED": sidebar.CheckMerged, "CLOSED": sidebar.CheckClosed}
	reviewMoves = map[string]string{"APPROVED": sidebar.CheckApproved, "CHANGES_REQUESTED": sidebar.CheckChanges}
	checksMoves = map[string]string{sidebar.CIPassed: sidebar.CheckGreen, sidebar.CIFailed: sidebar.CheckFailing}
)

// readBoth reads the list, pruned at now, and its facts.
func (s Store) readBoth(now time.Time) (file, map[string]fact, error) {
	f, err := s.read()
	if err != nil {
		return f, nil, err
	}
	f.prune(now)
	facts, err := s.readChecks()
	return f, facts, err
}

// writeChecks writes the list's facts; none left removes the file.
func (s Store) writeChecks(facts map[string]fact) error {
	path := s.checksPath()
	if len(facts) == 0 {
		return ignoreMissing(os.Remove(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
