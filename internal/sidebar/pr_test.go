package sidebar

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// prFakeGH answers `pr view --json …` in a dir from out (gh's no-PR error when absent), or fails with err,
// logging every call. A non-nil gate holds each call until it is closed.
type prFakeGH struct {
	mu    sync.Mutex
	out   map[string]string
	err   error
	gate  chan struct{}
	calls []string
}

func (g *prFakeGH) run(dir string, args ...string) ([]byte, error) {
	g.mu.Lock()
	g.calls = append(g.calls, dir+": "+strings.Join(args, " "))
	gate, out, ok, err := g.gate, g.out[dir], g.out[dir] != "", g.err
	g.mu.Unlock()
	if gate != nil {
		<-gate
	}
	switch {
	case err != nil:
		return nil, err
	case ok:
		return []byte(out), nil
	}
	return nil, errors.New(`gh pr view: no pull requests found for branch "x"`)
}

func (g *prFakeGH) log() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

// prOnBranch is a branch reader for checkouts all on branch.
func prOnBranch(branch string) func(string) (string, error) {
	return func(string) (string, error) { return branch, nil }
}

// prFakeStore is the panes' prOption and @dev_pr_gen values, or fails every write with err.
type prFakeStore struct {
	mu     sync.Mutex
	values map[string]string
	gens   map[string]string
	err    error
}

func newPRFakeStore() *prFakeStore {
	return &prFakeStore{values: map[string]string{}, gens: map[string]string{}}
}

func (s *prFakeStore) store(pane, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.values[pane] = value
	return nil
}

func (s *prFakeStore) get(pane string) (value, gen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[pane], s.gens[pane]
}

func (s *prFakeStore) set(pane, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[pane] = value
}

// bump is dev state seeing a gh command that changes pane's PR.
func (s *prFakeStore) bump(pane string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gens[pane] += "+"
}

func (s *prFakeStore) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func prTestCache(g *prFakeGH, st *prFakeStore, now *time.Time) *prCache {
	return &prCache{gh: g.run, branch: prOnBranch("feat"), store: st.store, now: func() time.Time { return *now }, sem: make(chan struct{}, prLookups), entries: map[prKey]prEntry{}}
}

// prGet is a tick's get of path's feat PR, by the agent pane "%"+path with its stored options.
func prGet(p *prCache, st *prFakeStore, path string) PR {
	stored, gen := st.get("%" + path)
	return p.get("%"+path, stored, gen, path, "feat")
}

// prSetGH points gh's answer for /r at out, held until the returned gate closes.
func prSetGH(g *prFakeGH, out string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = make(chan struct{})
	g.out["/r"] = out
	return g.gate
}

func TestPRCache(t *testing.T) {
	g := &prFakeGH{out: map[string]string{"/r": `{"number":12,"state":"OPEN","isDraft":false,"url":"https://x/pull/12"}`}}
	st := newPRFakeStore()
	now := time.Unix(1000, 0)
	p := prTestCache(g, st, &now)
	want := PR{Number: 12, State: "open", URL: "https://x/pull/12"}

	if got := p.get("%1", "", "", "", "feat"); got != (PR{}) || p.get("%1", "", "", "/r", "") != (PR{}) {
		t.Errorf("no path or branch = %+v, want none", got)
	}
	if got := prGet(p, st, "/r"); got != (PR{}) {
		t.Errorf("first get = %+v, want none until the lookup lands", got)
	}
	p.wg.Wait()
	if got := prGet(p, st, "/r"); got != want {
		t.Errorf("after the lookup = %+v, want %+v", got, want)
	}
	stored := `{"path":"/r","branch":"feat","gen":"","pr":{"Number":12,"State":"open","URL":"https://x/pull/12","Err":""},"fetched":1000000}`
	if got, _ := st.get("%/r"); got != stored {
		t.Errorf("stored %s, want %s", got, stored)
	}
	now = now.Add(prTTL - time.Second)
	prGet(p, st, "/r")
	p.wg.Wait()
	if got, want := g.log(), []string{"/r: pr view --json number,state,isDraft,url"}; !slices.Equal(got, want) {
		t.Fatalf("within the TTL: calls %q, want %q", got, want)
	}

	// Another view (a sidebar started later, the boss previews) shows the stored lookup without gh
	other := prTestCache(g, st, &now)
	if got := prGet(other, st, "/r"); got != want || len(g.log()) != 1 {
		t.Errorf("other view = %+v after %d calls, want %+v and no new call", got, len(g.log()), want)
	}
	// ...but not one for another branch, nor one it can't read: each looks up itself
	for _, v := range []string{stored, "garbage"} {
		view := prTestCache(g, st, &now)
		if got := view.get("%/r", v, "", "/r", "main"); got != (PR{}) {
			t.Errorf("stored %s, get main = %+v, want none", v, got)
		}
		view.wg.Wait()
	}
	if got := len(g.log()); got != 3 {
		t.Errorf("another branch: %d calls, want 3 (one per get)", got)
	}
	st.set("%/r", stored)

	// Expired: the old PR shows while one lookup runs, however many ticks ask
	gate := prSetGH(g, `{"number":12,"state":"MERGED","isDraft":false,"url":"https://x/pull/12"}`)
	now = now.Add(time.Second)
	calls := len(g.log())
	for range 3 {
		if got := prGet(p, st, "/r"); got != want {
			t.Errorf("expired, lookup running = %+v, want %+v", got, want)
		}
	}
	close(gate)
	p.wg.Wait()
	if got := len(g.log()); got != calls+1 {
		t.Errorf("expired: %d calls, want %d", got, calls+1)
	}
	want.State = "merged"
	if got := prGet(p, st, "/r"); got != want {
		t.Errorf("after the refresh = %+v, want %+v", got, want)
	}
	// The other view adopts the newer stored lookup
	if got := prGet(other, st, "/r"); got != want {
		t.Errorf("other view after the refresh = %+v, want %+v", got, want)
	}

	// Bumped gen (dev state saw gh pr close): looked up again at once, the PR shown meanwhile
	st.bump("%/r")
	gate = prSetGH(g, `{"number":12,"state":"CLOSED","isDraft":false,"url":"https://x/pull/12"}`)
	calls = len(g.log())
	if got := prGet(p, st, "/r"); got != want {
		t.Errorf("bumped, lookup running = %+v, want %+v", got, want)
	}
	// A newer lookup another view stored meanwhile shows at once, with no second lookup here
	adopted := PR{Number: 12, State: "open", URL: "https://x/pull/12"}
	v, _ := json.Marshal(prStored{Path: "/r", Branch: "feat", Gen: "+", PR: adopted, Fetched: now.Add(time.Second).UnixMilli()})
	st.set("%/r", string(v))
	for range 3 {
		if got := prGet(p, st, "/r"); got != adopted {
			t.Errorf("newer stored while looking up = %+v, want %+v", got, adopted)
		}
	}
	close(gate)
	p.wg.Wait()
	want.State = "closed"
	if got := prGet(p, st, "/r"); got != want || len(g.log()) != calls+1 {
		t.Errorf("after the bump = %+v after %d calls, want %+v after %d", got, len(g.log()), want, calls+1)
	}

	// A lookup that started before the bump is looked up again: the bump may have come after gh answered
	gate = prSetGH(g, `{"number":12,"state":"OPEN","isDraft":false,"url":"https://x/pull/12"}`)
	now = now.Add(prTTL)
	prGet(p, st, "/r")
	st.bump("%/r")
	close(gate)
	p.wg.Wait()
	calls = len(g.log())
	prGet(p, st, "/r")
	p.wg.Wait()
	want.State = "open"
	if got := prGet(p, st, "/r"); got != want || len(g.log()) != calls+1 {
		t.Errorf("bumped during a lookup = %+v after %d calls, want %+v after %d", got, len(g.log()), want, calls+1)
	}

	// A failed lookup keeps the PR found and adds why, in this view only: what it found may be older
	// than another view's, so the option keeps the last success
	g.mu.Lock()
	g.err = errors.New("gh pr view: offline")
	g.mu.Unlock()
	now = now.Add(prTTL)
	before, _ := st.get("%/r")
	prGet(p, st, "/r")
	p.wg.Wait()
	ok := want
	want.Err = "gh pr view: offline"
	if got := prGet(p, st, "/r"); got != want {
		t.Errorf("failed refresh = %+v, want %+v", got, want)
	}
	if after, _ := st.get("%/r"); after != before {
		t.Errorf("failed refresh stored %s, want the last success kept: %s", after, before)
	}
	// Another view whose own lookup is due shows the last success while it looks up
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	view := prTestCache(g, st, &now)
	if got := prGet(view, st, "/r"); got != ok {
		t.Errorf("failed refresh, new view = %+v, want %+v", got, ok)
	}
	// Its lookup would otherwise land among the calls counted below
	view.wg.Wait()
	g.mu.Lock()
	g.err = errors.New("gh pr view: offline")
	g.mu.Unlock()
	// ...and with none found yet, it is none with the reason
	prGet(p, st, "/other")
	p.wg.Wait()
	if got := prGet(p, st, "/other"); got != (PR{Err: "gh pr view: offline"}) {
		t.Errorf("failed first lookup = %+v, want none with the reason", got)
	}

	// A failed write keeps the lookup here, with why, until the TTL
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	st.fail(errors.New("tmux: no pane"))
	prGet(p, st, "/down")
	p.wg.Wait()
	calls = len(g.log())
	for range 3 {
		if got := prGet(p, st, "/down"); got != (PR{Err: "tmux: no pane"}) {
			t.Errorf("failed write = %+v, want none with the reason", got)
		}
	}
	p.wg.Wait()
	if got := len(g.log()); got != calls {
		t.Errorf("failed write: %d calls, want %d (no retry within the TTL)", got, calls)
	}

	// gh's own no-PR answer is none, cached for the TTL too
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	st.fail(nil)
	prGet(p, st, "/nopr")
	p.wg.Wait()
	calls = len(g.log())
	if got := prGet(p, st, "/nopr"); got != (PR{}) || len(g.log()) != calls {
		t.Errorf("no PR = %+v after %d calls, want none and no new call", got, len(g.log()))
	}
}

func TestPRLookupStates(t *testing.T) {
	for _, tt := range []struct {
		out     string
		want    PR
		wantErr bool
	}{
		{`{"number":1,"state":"OPEN","isDraft":true,"url":"u"}`, PR{Number: 1, State: "draft", URL: "u"}, false},
		{`{"number":2,"state":"OPEN","isDraft":false,"url":"u"}`, PR{Number: 2, State: "open", URL: "u"}, false},
		{`{"number":3,"state":"MERGED","isDraft":false,"url":"u"}`, PR{Number: 3, State: "merged", URL: "u"}, false},
		{`{"number":4,"state":"CLOSED","isDraft":true,"url":"u"}`, PR{Number: 4, State: "closed", URL: "u"}, false},
		{`not json`, PR{}, true},
	} {
		p := &prCache{gh: (&prFakeGH{out: map[string]string{"/r": tt.out}}).run, branch: prOnBranch("b")}
		got, err := p.lookup(prKey{"%1", "/r", "b"})
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: %+v, %v; want %+v, error %v", tt.out, got, err, tt.want, tt.wantErr)
		}
	}
	// gh 2.x capitalises its no-PR message
	noPR := func(string, ...string) ([]byte, error) {
		return nil, errors.New(`gh pr view: No pull requests found for branch "b"`)
	}
	p := &prCache{gh: noPR, branch: prOnBranch("b")}
	if got, err := p.lookup(prKey{"%1", "/r", "b"}); got != (PR{}) || err != nil {
		t.Errorf("no PR: %+v, %v; want none, no error", got, err)
	}

	// The checkout moved off the key's branch before gh ran: its answer, PR or none, is another key's
	open := (&prFakeGH{out: map[string]string{"/r": `{"number":2,"state":"OPEN","isDraft":false,"url":"u"}`}}).run
	for name, gh := range map[string]func(string, ...string) ([]byte, error){"PR": open, "no PR": noPR} {
		p := &prCache{gh: gh, branch: prOnBranch("other")}
		if got, err := p.lookup(prKey{"%1", "/r", "b"}); got != (PR{}) || err == nil {
			t.Errorf("moved, %s: %+v, %v; want an error", name, got, err)
		}
	}
}

func TestPROpen(t *testing.T) {
	g := &prFakeGH{}
	p := &prCache{gh: g.run}
	if err := p.open("https://x/pull/12"); err == nil {
		t.Error("fake gh failure: want an error")
	}
	if got, want := g.log(), []string{": pr view https://x/pull/12 --web"}; !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}
}

// RunGH against a gh script on PATH: no prompts, and stderr in the error rather than on the screen.
func TestPRRunGH(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		`[ "$1" = fail ] && { echo "no pull requests" >&2; exit 1; }` + "\n" +
		`echo "$GH_PROMPT_DISABLED $GH_NO_UPDATE_NOTIFIER $(pwd) $*"` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	out, err := RunGH(dir, "pr", "view")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "1 1 "+dir+" pr view\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if _, err := RunGH(dir, "fail"); err == nil || err.Error() != "gh fail: no pull requests" {
		t.Errorf("error %v, want gh's stderr in it", err)
	}
}
