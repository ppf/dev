package todo

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
// lookups fail (0 while they don't).
type fact struct {
	Link    string `json:"link"`
	First   prSeen `json:"first"`
	Last    prSeen `json:"last"`
	Checked int64  `json:"checked"`
	Err     string `json:"err,omitempty"`
	Failing int64  `json:"failing,omitempty"`
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
	if it.Added != 0 && now.Sub(time.Unix(it.Added, 0)) >= unchanged && len(it.Options) == 0 && it.Until <= now.Unix() {
		return &sidebar.Check{Kind: sidebar.CheckUnchanged, Since: it.Added}
	}
	return nil
}

// Lookup looks up the PRs the open items link to, those not looked up within ttl, at most limit of them (0: all),
// with view, and keeps what it finds beside the list, not in it: the list's Updated stays the boss's last change. A
// failed lookup is kept as the item's Err and tried again after ttl; a lookup older than one kept meanwhile is
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
			facts[k] = fc
		}
		return s.writeChecks(facts)
	})
}

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
