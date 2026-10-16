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
)

const (
	// prTTL is how long a lookup is reused, PR or not, before gh runs again.
	prTTL = time.Minute
	// prTimeout bounds one gh call, so a hung gh can't hold a lookup slot, or o, forever.
	prTimeout = 10 * time.Second
	// prLookups caps the gh processes running at once.
	prLookups = 2
)

// PR is a branch's pull request; Number 0 means none.
type PR struct {
	Number int
	State  string // open, draft, merged or closed
	URL    string
	Err    string // why the last lookup failed; the fields above keep the last PR found
}

// prKey is a pane's directory (gh resolves the repo and branch from it) and the branch read there.
type prKey struct{ path, branch string }

// prEntry is one key's last lookup.
type prEntry struct {
	pr      PR
	fetched time.Time
	pending bool // a lookup is running
}

// prCache looks PRs up with gh in the background: a tick reads the last result at once (gh takes
// hundreds of ms) and starts a lookup for a missing or expired key, whose result the next tick shows.
// A failure keeps the last PR found (none at first) and its message for o.
type prCache struct {
	// gh runs gh args in dir and returns its stdout; tests replace it.
	gh func(dir string, args ...string) ([]byte, error)
	// branch reads dir's current branch; tests replace it.
	branch  func(dir string) (string, error)
	now     func() time.Time
	sem     chan struct{}
	wg      sync.WaitGroup // running lookups, so tests can wait for them
	mu      sync.Mutex
	entries map[prKey]prEntry
}

// newPRCache returns an empty cache that runs the real gh.
func newPRCache() *prCache {
	branch := func(dir string) (string, error) { return runGit(dir, "branch", "--show-current") }
	return &prCache{gh: runGH, branch: branch, now: time.Now, sem: make(chan struct{}, prLookups), entries: map[prKey]prEntry{}}
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

// get returns branch's PR as last looked up in path, starting a lookup when there is none or it expired.
func (p *prCache) get(path, branch string) PR {
	if path == "" || branch == "" {
		return PR{}
	}
	k := prKey{path, branch}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[k]
	if !e.pending && (!ok || p.now().Sub(e.fetched) >= prTTL) {
		e.pending = true
		p.entries[k] = e
		p.wg.Add(1)
		go p.fetch(k)
	}
	return e.pr
}

// fetch looks k up and stores the result.
func (p *prCache) fetch(k prKey) {
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
	p.entries[k] = prEntry{pr: pr, fetched: p.now()}
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
