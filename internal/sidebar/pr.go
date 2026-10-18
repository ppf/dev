package sidebar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

const (
	// prTTL is how long a lookup is reused, PR or not, before gh runs again.
	prTTL = time.Minute
	// prTimeout bounds one gh call, so a hung gh can't hold a lookup slot, or o, forever.
	prTimeout = 10 * time.Second
	// prLookups caps the gh processes running at once.
	prLookups = 2
)

// prOption is the agent pane option holding its last lookup, shared by every sidebar and the boss
// previews. dev state bumps the pane's @dev_pr_gen when the agent runs a gh command that changes the
// PR, which makes every lookup stored under an older one stale.
const prOption = "@dev_pr"

// PR is a branch's pull request; Number 0 means none.
type PR struct {
	Number int
	State  string // open, draft, merged or closed
	URL    string
	Err    string // why the last lookup failed; the fields above keep the last PR found
}

// prKey is an agent pane, its directory (gh resolves the repo and branch from it) and the branch read there.
type prKey struct{ pane, path, branch string }

// prEntry is one key's last lookup.
type prEntry struct {
	pr      PR
	fetched time.Time
	gen     string // the pane's @dev_pr_gen when the lookup started
	pending bool   // a lookup is running
}

// prStored is a lookup as prOption holds it.
type prStored struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Gen     string `json:"gen"`
	PR      PR     `json:"pr"`
	Fetched int64  `json:"fetched"` // unix ms
}

// prCache looks PRs up with gh in the background: a tick reads the last result at once (gh takes
// hundreds of ms) and starts a lookup for a missing or expired key, whose result the next tick shows.
// Results go to the pane's prOption, so one lookup serves every view, and a newer one found there is
// adopted; a lookup from before the pane's last @dev_pr_gen bump is looked up again.
// A failure keeps the last PR found (none at first) and its message for o.
type prCache struct {
	// gh runs gh args in dir and returns its stdout; tests replace it.
	gh func(dir string, args ...string) ([]byte, error)
	// branch reads dir's current branch; tests replace it.
	branch func(dir string) (string, error)
	// store writes value to pane's prOption; tests replace it.
	store   func(pane, value string) error
	now     func() time.Time
	sem     chan struct{}
	wg      sync.WaitGroup // running lookups, so tests can wait for them
	mu      sync.Mutex
	entries map[prKey]prEntry
}

// newPRCache returns an empty cache that runs the real gh and stores through c.
func newPRCache(c *tmux.Client) *prCache {
	branch := func(dir string) (string, error) { return runGit(dir, "branch", "--show-current") }
	store := func(pane, value string) error {
		_, err := c.Run("set-option", "-p", "-t", pane, prOption, value)
		return err
	}
	return &prCache{gh: runGH, branch: branch, store: store, now: time.Now, sem: make(chan struct{}, prLookups), entries: map[prKey]prEntry{}}
}

// runGH runs gh args in dir without prompts; stderr is kept for the error, it would land on the TUI.
func runGH(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	// gh --web hands its stdout to the browser launcher, which may keep it open
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("gh %s: timed out after %s", strings.Join(args, " "), prTimeout)
	}
	if err != nil {
		if e, ok := errors.AsType[*exec.ExitError](err); ok && len(e.Stderr) > 0 {
			return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.Join(strings.Fields(string(e.Stderr)), " "))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// get returns branch's PR as last looked up in path by pane, the agent pane whose prOption reads stored
// and @dev_pr_gen gen, starting a lookup when there is none, it expired or gen moved on.
func (p *prCache) get(pane, stored, gen, path, branch string) PR {
	if path == "" || branch == "" {
		return PR{}
	}
	k := prKey{pane, path, branch}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[k]
	var s prStored
	if json.Unmarshal([]byte(stored), &s) == nil && s.Path == path && s.Branch == branch {
		if fetched := time.UnixMilli(s.Fetched); !ok || fetched.After(e.fetched) {
			e = prEntry{pr: s.PR, fetched: fetched, gen: s.Gen, pending: e.pending}
			p.entries[k], ok = e, true
		}
	}
	if !e.pending && (!ok || e.gen != gen || p.now().Sub(e.fetched) >= prTTL) {
		e.pending = true
		p.entries[k] = e
		p.wg.Add(1)
		go p.fetch(k, gen)
	}
	return e.pr
}

// fetch looks k up and stores the result under gen, here and in k's pane.
func (p *prCache) fetch(k prKey, gen string) {
	defer p.wg.Done()
	p.sem <- struct{}{}
	pr, err := p.lookup(k)
	<-p.sem
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		pr = p.entries[k].pr
		pr.Err = err.Error()
	}
	e := prEntry{pr: pr, fetched: p.now(), gen: gen}
	v, _ := json.Marshal(prStored{Path: k.path, Branch: k.branch, Gen: gen, PR: pr, Fetched: e.fetched.UnixMilli()})
	if err := p.store(k.pane, string(v)); err != nil {
		// Kept here for o, after the lookup's own error
		e.pr.Err = strings.TrimPrefix(pr.Err+"; "+err.Error(), "; ")
	}
	p.entries[k] = e
}

// lookup asks gh for the PR of k's checkout in any state. Without a branch argument gh finds the
// PR's head through the branch's push remote, so a PR from a fork is found too.
func (p *prCache) lookup(k prKey) (PR, error) {
	out, err := p.gh(k.path, "pr", "view", "--json", "number,state,isDraft,url")
	none := err != nil && strings.Contains(strings.ToLower(err.Error()), "no pull requests found")
	if err != nil && !none {
		return PR{}, err
	}
	// gh answered for the branch checked out now; if that isn't k's branch, the answer belongs to another key
	if branch, err := p.branch(k.path); err != nil || branch != k.branch {
		return PR{}, fmt.Errorf("gh pr view: %s is no longer on %s", k.path, k.branch)
	}
	if none {
		return PR{}, nil
	}
	var v struct {
		Number  int    `json:"number"`
		State   string `json:"state"`
		IsDraft bool   `json:"isDraft"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return PR{}, fmt.Errorf("gh pr view: %w", err)
	}
	state := strings.ToLower(v.State)
	if state == "open" && v.IsDraft {
		state = "draft"
	}
	return PR{Number: v.Number, State: state, URL: v.URL}, nil
}

// open shows url in the browser gh is configured with.
func (p *prCache) open(url string) error {
	_, err := p.gh("", "pr", "view", url, "--web")
	return err
}
