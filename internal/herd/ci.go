package herd

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// CI states, as a PR row shows them; "" until a read succeeded, or for a PR without checks.
const (
	ciPassed  = "passed"
	ciFailed  = "failed"
	ciRunning = "running"
)

// ciTTL is how long a PR's checks are kept before gh is asked again, ciEmptyTTL while it has none (a PR just
// opened or pushed, its checks not started yet); ciReads caps the reads running at once; a PR not looked at for
// ciForget is forgotten (merged, closed, its worker gone).
const (
	ciTTL      = time.Minute
	ciEmptyTTL = 10 * time.Second
	ciReads    = 2
	ciForget   = 10 * time.Minute
)

// ci is an open PR's checks: failed when any failed or was cancelled, else running while any runs, else
// passed; names are the checks that make it (the failed ones, the running ones, else all).
type ci struct {
	state string
	names []string
	err   string // why the last read failed; the fields above keep the last checks read
}

// rollupCheck is one of gh's statusCheckRollup entries: a check run (name, status, conclusion) or a commit
// status (context, state).
type rollupCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// failedChecks and runningChecks are the conclusions and states that make a check failed or running; any
// other one passed (success, skipped, neutral, and whatever gh adds).
var (
	failedChecks = []string{"FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "ERROR"}
	// A stale check's result is for an older commit: not passed yet
	runningChecks = []string{"QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED", "EXPECTED", "STALE"}
)

// parseRollup reads gh pr view --json statusCheckRollup's output.
func parseRollup(out []byte) (ci, error) {
	var v struct {
		Checks []rollupCheck `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return ci{}, err
	}
	var failed, running, all []string
	// A check run on push and pull_request, or re-run, is named once
	add := func(names *[]string, name string) {
		if !slices.Contains(*names, name) {
			*names = append(*names, name)
		}
	}
	for _, c := range v.Checks {
		name := c.Name
		if name == "" {
			name = c.Context
		}
		add(&all, name)
		switch {
		case slices.Contains(failedChecks, c.Conclusion), slices.Contains(failedChecks, c.State):
			add(&failed, name)
		case slices.Contains(runningChecks, c.Status), slices.Contains(runningChecks, c.State), slices.Contains(runningChecks, c.Conclusion):
			add(&running, name)
		}
	}
	switch {
	case len(failed) > 0:
		return ci{state: ciFailed, names: failed}, nil
	case len(running) > 0:
		return ci{state: ciRunning, names: running}, nil
	case len(all) > 0:
		return ci{state: ciPassed, names: all}, nil
	}
	return ci{}, nil
}

// ciCache reads PRs' checks with gh in the background: a refresh gets the last read at once and starts a
// read for a PR not read yet, read over ciTTL ago (ciEmptyTTL when it had no checks) or before its pane's
// @dev_pr_gen moved on, whose result the next refresh shows.
type ciCache struct {
	// gh runs gh args; tests replace it.
	gh      func(args ...string) ([]byte, error)
	now     func() time.Time
	sem     chan struct{}
	wg      sync.WaitGroup // running reads, so tests can wait for them
	mu      sync.Mutex
	entries map[string]ciEntry
}

// ciEntry is a PR's last read.
type ciEntry struct {
	ci      ci
	read    time.Time
	gen     string    // the pane's @dev_pr_gen when the read started
	seen    time.Time // last looked at
	pending bool      // a read is running
}

func newCICache() *ciCache {
	gh := func(args ...string) ([]byte, error) { return sidebar.RunGH("", args...) }
	return &ciCache{gh: gh, now: time.Now, sem: make(chan struct{}, ciReads), entries: map[string]ciEntry{}}
}

// get is the checks of the PR at url as last read, starting a read when due; gen is its pane's @dev_pr_gen.
func (c *ciCache) get(url, gen string) ci {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for u, e := range c.entries {
		if !e.pending && now.Sub(e.seen) >= ciForget {
			delete(c.entries, u)
		}
	}
	e, ok := c.entries[url]
	e.seen = now
	ttl := ciTTL
	if e.ci.state == "" && e.ci.err == "" {
		ttl = ciEmptyTTL
	}
	if !e.pending && (!ok || e.gen != gen || now.Sub(e.read) >= ttl) {
		e.pending = true
		c.wg.Add(1)
		go c.fetch(url, gen)
	}
	c.entries[url] = e
	return e.ci
}

// fetch reads url's checks under gen; a failure keeps the last ones read, with why.
func (c *ciCache) fetch(url, gen string) {
	defer c.wg.Done()
	c.sem <- struct{}{}
	args := []string{"pr", "view", url, "--json", "statusCheckRollup"}
	out, err := c.gh(args...)
	<-c.sem
	var got ci
	if err == nil {
		got, err = parseRollup(out)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[url]
	if err != nil {
		got = e.ci
		// The reason only: a card's row would cut it off behind the call gh's errors start with
		got.err = strings.TrimPrefix(err.Error(), "gh "+strings.Join(args, " ")+": ")
	}
	c.entries[url] = ciEntry{ci: got, read: c.now(), gen: gen, seen: e.seen}
}
