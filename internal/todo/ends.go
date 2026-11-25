package todo

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// The --ends kinds: an item dev ticks by itself (DoneByDev) once its own state shows it over.
const (
	EndsRestart = "restart" // restart:V: every running coordinator started fresh on dev V or later
	EndsTest    = "test"    // test:V: a test:W item, W newer than V, is on the same list
)

// DoneByDev is Item.DoneBy of an item dev ticked by one of its fixed rules (--ends, its own clear offer); its Result
// says which.
const DoneByDev = "dev"

// OptVersion is the coordinator pane option holding "<unix> <dev version>" of its Claude's last fresh start
// (SessionStart startup): the restart rule's state. A coordinator without it counts as not restarted.
const OptVersion = "@dev_version"

// semver is a dev version: vMAJOR.MINOR.PATCH, a pre-release (a Go pseudo-version too) and build after.
var semver = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// version is v parsed; ok false for no dev version ("(devel) …", "unknown").
func version(v string) (core [3]int, pre string, ok bool) {
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return core, "", false
	}
	for i := range core {
		core[i], _ = strconv.Atoi(m[i+1])
	}
	return core, m[4], true
}

// atLeast reports dev version v at or after want, a release: a pre-release of want's own number comes before it.
func atLeast(v, want string) bool {
	a, pre, ok := version(v)
	b, _, okWant := version(want)
	if !ok || !okWant {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return pre == ""
}

// ParseEnds splits an --ends value, kind:version, refusing another kind or a version not vMAJOR.MINOR.PATCH.
func ParseEnds(ends string) (kind, v string, err error) {
	kind, v, _ = strings.Cut(ends, ":")
	if kind != EndsRestart && kind != EndsTest {
		return "", "", fmt.Errorf("ends %q: want restart:<version> or test:<version>", ends)
	}
	if _, pre, ok := version(v); !ok || pre != "" || strings.Contains(v, "+") {
		return "", "", fmt.Errorf("ends %q: the version is a release, as v0.93.0", ends)
	}
	return kind, v, nil
}

// checkEnds is why it can't have its Ends: not ParseEnds's, or an ask, which waits on the user's answer.
func (it Item) checkEnds() error {
	if it.Ends == "" {
		return nil
	}
	if len(it.Options) > 0 {
		return errors.New("ends: an ask waits on the user's answer, not on dev")
	}
	_, _, err := ParseEnds(it.Ends)
	return err
}

// Start is a running coordinator's last fresh start: its session, when (unix seconds) and the dev version it started
// on; Version "" when it recorded none (OptVersion).
type Start struct {
	Session string
	At      int64
	Version string
}

// startRow is what Starts reads of each pane.
type startRow struct {
	Session string `tmux:"session_name"`
	Role    string `tmux:"@dev_boss_pane"`
	Version string `tmux:"@dev_version"` // OptVersion
	Dead    string `tmux:"pane_dead"`
}

// Starts are the fresh starts of the coordinators running on c's server (dev boss sessions, the manager's too); none
// without a server.
func Starts(c *tmux.Client) ([]Start, error) {
	rows, err := tmux.Query[startRow](c, "list-panes", "-a")
	if tmux.IsNoServer(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Start
	for _, r := range rows {
		// A coordinator that exited (remain-on-exit keeps its pane) runs nothing to restart
		if r.Role != "coordinator" || r.Dead == "1" {
			continue
		}
		s := Start{Session: r.Session}
		if at, v, ok := strings.Cut(r.Version, " "); ok {
			if s.At, err = strconv.ParseInt(at, 10, 64); err != nil {
				return nil, fmt.Errorf("%s %s %q: %w", r.Session, OptVersion, r.Version, err)
			}
			s.Version = v
		}
		out = append(out, s)
	}
	return out, nil
}

// Expire ticks by dev the open items whose --ends its state shows over, each with why as its Result, its boss told
// (Ticked): restart:V once every running coordinator (starts, read only when such an item is open) started fresh on V
// or later after the item's Waived; test:V once a test:W item, W newer than V and than its WaivedTo, is on the list or
// finished in the last day (the newest such names it, and becomes its WaivedTo). Updated stays: not the boss's change. A starts failure or an --ends it can't read skips
// those items only and is returned, the others ticked all the same. It reports how many it ticked.
func (s Store) Expire(now time.Time, starts func() ([]Start, error)) (int, error) {
	n := 0
	var skipped []error
	err := s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		f.prune(now)
		if !slices.ContainsFunc(f.Items, func(it Item) bool { return it.Done == 0 && it.Ends != "" }) {
			return nil
		}
		kept, err := s.readDone(true)
		if err != nil {
			return err
		}
		// A newer test done since still replaces it
		tests := append(slices.Clone(f.Items), finished(append(kept, f.dropped...), now)...)
		var got []Start
		var serr error
		read := false
		for i, it := range f.Items {
			if it.Done != 0 || it.Ends == "" {
				continue
			}
			kind, v, err := ParseEnds(it.Ends)
			if err != nil {
				skipped = append(skipped, fmt.Errorf("item %d: %w", it.ID, err))
				continue
			}
			result, waive := "", ""
			switch kind {
			case EndsRestart:
				if !read {
					if got, serr = starts(); serr != nil {
						skipped = append(skipped, fmt.Errorf("restart items: %w", serr))
					}
					read = true
				}
				if serr == nil && restarted(got, v, it.Waived) {
					result = "closed by dev: restarted on " + v
				}
			case EndsTest:
				if o, w, ok := newerTest(tests, it, v); ok {
					result, waive = fmt.Sprintf("closed by dev: %s replaced %s (#%d)", w, v, o.ID), w
				}
			}
			if result == "" {
				continue
			}
			f.Items[i].tick(now.Unix(), DoneByDev)
			f.Items[i].Result, f.Items[i].WaivedTo = result, cmp.Or(waive, it.WaivedTo)
			f.tell(Ticked{ID: it.ID, Text: it.Text, Ends: it.Ends, Rule: result})
			n++
		}
		if n == 0 || s.trial {
			return nil
		}
		return s.write(f)
	})
	return n, errors.Join(err, errors.Join(skipped...))
}

// restarted reports every one of starts, at least one, a fresh start after waived on v or later.
func restarted(starts []Start, v string, waived int64) bool {
	for _, st := range starts {
		if st.At <= waived || !atLeast(st.Version, v) {
			return false
		}
	}
	return len(starts) > 0
}

// newerTest is the item of items with the newest test:W, W after v and after its WaivedTo, and W.
func newerTest(items []Item, it Item, v string) (o Item, w string, ok bool) {
	for _, x := range items {
		kind, xv, err := ParseEnds(x.Ends)
		if sameItem(x, it) || err != nil || kind != EndsTest || !newer(xv, v) || it.WaivedTo != "" && !newer(xv, it.WaivedTo) {
			continue
		}
		if !ok || newer(xv, w) {
			o, w, ok = x, xv, true
		}
	}
	return o, w, ok
}

// newer reports release a after release b.
func newer(a, b string) bool { return a != b && atLeast(a, b) }

// Withdraw ticks open item id by dev, while it reads text, with result (why), its boss told: dev's own ask that no
// longer holds, never an answer (no "you answered", no tally event). Updated stays.
func (s Store) Withdraw(id int, text, result string, now time.Time) error {
	return s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		f.prune(now)
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 && it.Text == text })
		if i < 0 {
			return fmt.Errorf("no open item %d reading %q", id, text)
		}
		f.Items[i].tick(now.Unix(), DoneByDev)
		f.Items[i].Result = result
		f.tell(Ticked{ID: id, Text: text, Rule: result})
		if s.trial {
			return nil
		}
		return s.write(f)
	})
}
