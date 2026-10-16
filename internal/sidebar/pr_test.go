package sidebar

import (
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

func prTestCache(g *prFakeGH, now *time.Time) *prCache {
	return &prCache{gh: g.run, branch: prOnBranch("feat"), now: func() time.Time { return *now }, sem: make(chan struct{}, prLookups), entries: map[prKey]prEntry{}}
}

func TestPRCache(t *testing.T) {
	g := &prFakeGH{out: map[string]string{"/r": `{"number":12,"state":"OPEN","isDraft":false,"url":"https://x/pull/12"}`}}
	now := time.Unix(1000, 0)
	p := prTestCache(g, &now)
	want := PR{Number: 12, State: "open", URL: "https://x/pull/12"}

	if got := p.get("", "feat"); got != (PR{}) || p.get("/r", "") != (PR{}) {
		t.Errorf("no path or branch = %+v, want none", got)
	}
	if got := p.get("/r", "feat"); got != (PR{}) {
		t.Errorf("first get = %+v, want none until the lookup lands", got)
	}
	p.wg.Wait()
	if got := p.get("/r", "feat"); got != want {
		t.Errorf("after the lookup = %+v, want %+v", got, want)
	}
	now = now.Add(prTTL - time.Second)
	p.get("/r", "feat")
	p.wg.Wait()
	if got, want := g.log(), []string{"/r: pr view --json number,state,isDraft,url"}; !slices.Equal(got, want) {
		t.Fatalf("within the TTL: calls %q, want %q", got, want)
	}

	// Expired: the old PR shows while one lookup runs, however many ticks ask
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.out["/r"] = `{"number":12,"state":"MERGED","isDraft":false,"url":"https://x/pull/12"}`
	g.mu.Unlock()
	now = now.Add(time.Second)
	for range 3 {
		if got := p.get("/r", "feat"); got != want {
			t.Errorf("expired, lookup running = %+v, want %+v", got, want)
		}
	}
	close(g.gate)
	p.wg.Wait()
	if got := len(g.log()); got != 2 {
		t.Errorf("expired: %d calls, want 2", got)
	}
	want.State = "merged"
	if got := p.get("/r", "feat"); got != want {
		t.Errorf("after the refresh = %+v, want %+v", got, want)
	}

	// A failed lookup keeps the PR found and adds why
	g.mu.Lock()
	g.gate, g.err = nil, errors.New("gh pr view: offline")
	g.mu.Unlock()
	now = now.Add(prTTL)
	p.get("/r", "feat")
	p.wg.Wait()
	want.Err = "gh pr view: offline"
	if got := p.get("/r", "feat"); got != want {
		t.Errorf("failed refresh = %+v, want %+v", got, want)
	}
	// ...and with none found yet, it is none with the reason
	p.get("/other", "feat")
	p.wg.Wait()
	if got := p.get("/other", "feat"); got != (PR{Err: "gh pr view: offline"}) {
		t.Errorf("failed first lookup = %+v, want none with the reason", got)
	}

	// gh's own no-PR answer is none, cached for the TTL too
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	p.get("/nopr", "feat")
	p.wg.Wait()
	calls := len(g.log())
	if got := p.get("/nopr", "feat"); got != (PR{}) || len(g.log()) != calls {
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
		got, err := p.lookup(prKey{"/r", "b"})
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: %+v, %v; want %+v, error %v", tt.out, got, err, tt.want, tt.wantErr)
		}
	}
	// gh 2.x capitalises its no-PR message
	noPR := func(string, ...string) ([]byte, error) {
		return nil, errors.New(`gh pr view: No pull requests found for branch "b"`)
	}
	p := &prCache{gh: noPR, branch: prOnBranch("b")}
	if got, err := p.lookup(prKey{"/r", "b"}); got != (PR{}) || err != nil {
		t.Errorf("no PR: %+v, %v; want none, no error", got, err)
	}

	// The checkout moved off the key's branch before gh ran: its answer, PR or none, is another key's
	open := (&prFakeGH{out: map[string]string{"/r": `{"number":2,"state":"OPEN","isDraft":false,"url":"u"}`}}).run
	for name, gh := range map[string]func(string, ...string) ([]byte, error){"PR": open, "no PR": noPR} {
		p := &prCache{gh: gh, branch: prOnBranch("other")}
		if got, err := p.lookup(prKey{"/r", "b"}); got != (PR{}) || err == nil {
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

// runGH against a gh script on PATH: no prompts, and stderr in the error rather than on the screen.
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

	out, err := runGH(dir, "pr", "view")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "1 1 "+dir+" pr view\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if _, err := runGH(dir, "fail"); err == nil || err.Error() != "gh fail: no pull requests" {
		t.Errorf("error %v, want gh's stderr in it", err)
	}
}
