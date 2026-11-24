package todo

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ppf/dev/internal/config"
)

// The kinds of event the tally log keeps (Event.Kind, Event.Of).
const (
	TallyAnswered        = "answered"         // an ask was answered with the user's pick, whichever way it reached dev
	TallyBoss            = "boss"             // a boss settled its own ask (Decide)
	TallyManagerMarked   = "manager-marked"   // the manager settled an ask its boss marked obvious
	TallyManagerUnmarked = "manager-unmarked" // the manager settled an unmarked ask, for a why
	TallyOverruled       = "overruled"        // the user overruled a settled ask; Of is the kind it was
)

// Event is one line of the tally log: when (unix seconds), whose list, the item's number and text hash, what happened
// (Of: for an overrule, the kind of the decision overruled).
type Event struct {
	At   int64  `json:"at"`
	Boss string `json:"boss"`
	ID   int    `json:"id"`
	Hash string `json:"hash"`
	Kind string `json:"kind"`
	Of   string `json:"of,omitempty"`
}

// tallyPath is the append-only log of every list's events, beside the lists.
func tallyPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "todo", "tally.jsonl"), nil
}

// note queues an event for it on s's list, written with the list (write); not for a test item.
func (f *file) note(s Store, it Item, kind, of string, now time.Time) {
	if it.Quiet {
		return
	}
	sum := sha256.Sum256([]byte(it.Text))
	f.events = append(f.events, Event{At: now.Unix(), Boss: cmp.Or(s.boss, s.name), ID: it.ID, Hash: hex.EncodeToString(sum[:6]), Kind: kind, Of: of})
}

// appendEvents appends events to the tally log, one line each, in one write.
func appendEvents(events []Event) error {
	path, err := tallyPath()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// ReadEvents is the tally log's events; none without a log.
func ReadEvents() ([]Event, error) {
	path, err := tallyPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ignoreMissing(err)
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// Count is one list's tally: asks the user answered, settled by the boss itself, settled by the manager (marked
// obvious, unmarked), overruled by the user of each of the three kinds, and the asks open now.
type Count struct {
	Answered        int `json:"answered"`
	BossSettled     int `json:"boss_settled"`
	ManagerMarked   int `json:"manager_marked"`
	ManagerUnmarked int `json:"manager_unmarked"`
	OverruledBoss   int `json:"overruled_boss"`
	OverruledMarked int `json:"overruled_manager_marked"`
	OverruledUnmark int `json:"overruled_manager_unmarked"`
	Open            int `json:"open"`
}

func (c *Count) add(o Count) {
	c.Answered += o.Answered
	c.BossSettled += o.BossSettled
	c.ManagerMarked += o.ManagerMarked
	c.ManagerUnmarked += o.ManagerUnmarked
	c.OverruledBoss += o.OverruledBoss
	c.OverruledMarked += o.OverruledMarked
	c.OverruledUnmark += o.OverruledUnmark
	c.Open += o.Open
}

type ListCount struct {
	Boss string `json:"boss"`
	Count
}

// Tally is the tally of the events at or after since (unix seconds), one row per list in name order, with open asks
// per list (open) added, and the total of all of them.
func Tally(events []Event, open map[string]int, since int64) (rows []ListCount, total Count) {
	by := map[string]*Count{}
	row := func(boss string) *Count {
		if by[boss] == nil {
			by[boss] = &Count{}
		}
		return by[boss]
	}
	for _, e := range events {
		if e.At < since {
			continue
		}
		c := row(e.Boss)
		switch e.Kind {
		case TallyAnswered:
			c.Answered++
		case TallyBoss:
			c.BossSettled++
		case TallyManagerMarked:
			c.ManagerMarked++
		case TallyManagerUnmarked:
			c.ManagerUnmarked++
		case TallyOverruled:
			switch e.Of {
			case TallyBoss:
				c.OverruledBoss++
			case TallyManagerMarked:
				c.OverruledMarked++
			case TallyManagerUnmarked:
				c.OverruledUnmark++
			}
		}
	}
	for boss, n := range open {
		row(boss).Open = n
	}
	for boss, c := range by {
		rows = append(rows, ListCount{Boss: boss, Count: *c})
		total.add(*c)
	}
	slices.SortFunc(rows, func(a, b ListCount) int { return cmp.Compare(a.Boss, b.Boss) })
	return rows, total
}

// OpenAsks counts the user's open asks on a list (not a test item's).
func (l List) OpenAsks(boss string) int {
	n := 0
	for _, it := range l.Items {
		if !it.Done && len(it.Options) > 0 && !it.Quiet && Own(it.On, it.Session, boss) {
			n++
		}
	}
	return n
}
