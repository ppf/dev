package herd

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
)

func TestParseRollup(t *testing.T) {
	run := func(name, status, conclusion string) string {
		return `{"__typename":"CheckRun","name":"` + name + `","status":"` + status + `","conclusion":"` + conclusion + `"}`
	}
	status := func(context, state string) string {
		return `{"__typename":"StatusContext","context":"` + context + `","state":"` + state + `"}`
	}
	tests := []struct {
		name   string
		checks []string
		want   ci
	}{
		{"none", nil, ci{}},
		{"passed and skipped", []string{run("test", "COMPLETED", "SUCCESS"), run("deploy", "COMPLETED", "SKIPPED")}, ci{state: ciPassed, names: []string{"test", "deploy"}}},
		{"running", []string{run("test", "IN_PROGRESS", ""), run("lint", "COMPLETED", "SUCCESS"), status("ci/x", "PENDING")}, ci{state: ciRunning, names: []string{"test", "ci/x"}}},
		{"a failure beats running", []string{run("test", "IN_PROGRESS", ""), run("lint", "COMPLETED", "FAILURE"), status("ci/x", "ERROR")}, ci{state: ciFailed, names: []string{"lint", "ci/x"}}},
		{"cancelled is failed", []string{run("test", "COMPLETED", "CANCELLED")}, ci{state: ciFailed, names: []string{"test"}}},
		{"stale isn't passed", []string{run("test", "COMPLETED", "STALE"), run("lint", "COMPLETED", "SUCCESS")}, ci{state: ciRunning, names: []string{"test"}}},
		{"a check run twice is named once", []string{run("test", "COMPLETED", "SUCCESS"), run("test", "COMPLETED", "SUCCESS"), run("lint", "COMPLETED", "SUCCESS")}, ci{state: ciPassed, names: []string{"test", "lint"}}},
	}
	for _, tt := range tests {
		got, err := parseRollup([]byte(`{"statusCheckRollup":[` + strings.Join(tt.checks, ",") + `]}`))
		if err != nil || got.state != tt.want.state || !slices.Equal(got.names, tt.want.names) {
			t.Errorf("%s: %+v, %v; want %+v", tt.name, got, err, tt.want)
		}
	}
	if _, err := parseRollup([]byte("not json")); err == nil {
		t.Error("bad output: no error")
	}
}

// A PR's checks are read in the background, once per ciTTL and one read at a time; a failed read keeps the
// last checks with the reason alone, and the next good read clears it; a PR not looked at is forgotten.
func TestCICache(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var calls []string
	out, fail := `{"statusCheckRollup":[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]}`, false
	const url = "https://github.com/o/r/pull/1"
	release := make(chan struct{})
	c := newCICache()
	c.now = func() time.Time { return now }
	c.gh = func(args ...string) ([]byte, error) {
		<-release
		calls = append(calls, strings.Join(args, " "))
		if fail {
			// As sidebar.RunGH words it
			return nil, errors.New("gh " + strings.Join(args, " ") + ": HTTP 401: Bad credentials")
		}
		return []byte(out), nil
	}
	if got := c.get(url, "%1", "1"); got.state != "" {
		t.Errorf("first get = %+v, want nothing yet", got)
	}
	// While it runs, another get starts none
	c.get(url, "%1", "1")
	close(release)
	c.wg.Wait()
	if got := c.get(url, "%1", "1"); got.state != ciPassed || len(calls) != 1 || calls[0] != "pr view "+url+" --json statusCheckRollup" {
		t.Errorf("after the read: %+v, calls %q", got, calls)
	}
	now = now.Add(ciTTL - time.Second)
	c.get(url, "%1", "1")
	c.wg.Wait()
	if len(calls) != 1 {
		t.Errorf("read again within ciTTL: %d calls", len(calls))
	}
	now = now.Add(time.Second)
	fail = true
	c.get(url, "%1", "1")
	c.wg.Wait()
	if got := c.get(url, "%1", "1"); got.state != ciPassed || got.err != "HTTP 401: Bad credentials" || len(calls) != 2 {
		t.Errorf("after a failed read: %+v, calls %d", got, len(calls))
	}
	now = now.Add(ciTTL)
	fail = false
	c.get(url, "%1", "1")
	c.wg.Wait()
	if got := c.get(url, "%1", "1"); got.state != ciPassed || got.err != "" {
		t.Errorf("after a good read: %+v", got)
	}
	now = now.Add(ciForget)
	c.get("other", "%1", "1")
	c.wg.Wait()
	if _, ok := c.entries[ciKey{url, "%1"}]; ok {
		t.Error("a PR not looked at for ciForget is kept")
	}
}

// A PR without checks yet is read again after ciEmptyTTL while just looked at (for ciEmptyFor after it was
// first, or under a newer @dev_pr_gen; a failed read too), else after ciTTL; a newer @dev_pr_gen (the worker
// ran a gh command that changed the PR) reads it at once, and a read started under an older one is read again
// once done. Each pane keeps its own gen, so two showing one PR don't make each other read it.
func TestCICacheRereads(t *testing.T) {
	now := time.Unix(1700000000, 0)
	calls := 0
	out, fail := `{"statusCheckRollup":[]}`, false
	var block chan struct{}
	const url = "https://github.com/o/r/pull/1"
	c := newCICache()
	c.now = func() time.Time { return now }
	c.gh = func(args ...string) ([]byte, error) {
		if block != nil {
			<-block
		}
		calls++
		if fail {
			return nil, errors.New("offline")
		}
		return []byte(out), nil
	}
	get := func(pane, gen string) ci {
		c.get(url, pane, gen)
		c.wg.Wait()
		return c.get(url, pane, gen)
	}
	expect := func(what string, got ci, state string, n int) {
		t.Helper()
		if got.state != state || calls != n {
			t.Errorf("%s: %+v, %d calls; want %q, %d calls", what, got, calls, state, n)
		}
	}
	fail = true
	expect("first read fails", get("%1", "1"), "", 1)
	fail = false
	now = now.Add(ciEmptyTTL - time.Second)
	expect("no checks, within ciEmptyTTL", get("%1", "1"), "", 1)
	now = now.Add(time.Second)
	expect("no checks, after ciEmptyTTL", get("%1", "1"), "", 2)
	now = now.Add(ciEmptyFor)
	expect("no checks, ciEmptyFor on", get("%1", "1"), "", 3)
	now = now.Add(ciEmptyTTL)
	expect("no checks after ciEmptyFor: ciTTL", get("%1", "1"), "", 3)
	out = `{"statusCheckRollup":[{"name":"test","status":"IN_PROGRESS"}]}`
	expect("newer gen", get("%1", "2"), ciRunning, 4)
	now = now.Add(ciEmptyTTL)
	expect("checks: ciTTL", get("%1", "2"), ciRunning, 4)
	expect("another pane", get("%2", "7"), ciRunning, 5)
	for range 3 {
		get("%1", "2")
		get("%2", "7")
	}
	expect("two panes on one PR", c.get(url, "%1", "2"), ciRunning, 5)
	// A gen bump while a read runs: that read is under the older gen, so the next get reads again
	now = now.Add(ciTTL)
	block = make(chan struct{})
	c.get(url, "%1", "2")
	expect("while a read runs", c.get(url, "%1", "3"), ciRunning, 5)
	close(block)
	c.wg.Wait()
	block = nil
	expect("read under an older gen", get("%1", "3"), ciRunning, 7)
}

// An open PR's row shows its checks' icon and names, a folded card the icon after the PR (a draft's at the
// muted level); checks that couldn't be read again keep the icon, saying why in place of the names; a folded
// one marks it ?. Only a shown boss reads an open or draft PR's checks, one with a URL.
func TestCardCI(t *testing.T) {
	pr := sidebar.PR{Number: 95, State: "open", URL: "u"}
	draft := sidebar.PR{Number: 103, State: "draft", URL: "u"}
	m := Model{width: 70, height: 60, snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "a", State: agent.Working, PR: pr}, ci: ci{state: ciFailed, names: []string{"lint", "test"}}},
		{sess: sidebar.Session{Name: "b", State: agent.Idle, PR: pr}, ci: ci{state: ciPassed, names: []string{"test"}}},
		{sess: sidebar.Session{Name: "c", State: agent.Working, PR: pr}, ci: ci{state: ciRunning, names: []string{"e2e"}, err: "gh: offline"}},
		{sess: sidebar.Session{Name: "d", State: agent.Working, PR: draft}, ci: ci{state: ciRunning, names: []string{"e2e"}}},
		{sess: sidebar.Session{Name: "e", State: agent.Idle, PR: draft}, ci: ci{state: ciRunning, names: []string{"e2e"}}},
	}}}
	got := strings.Join(m.board().lines, "\n")
	for _, w := range []string{"○ PR #95 open" + reset + "  " + sidebar.Red + "✗" + reset + " " + sidebar.Muted + "lint, test",
		"○ #95" + reset + " " + sidebar.Green + "✓", sidebar.Gold + "●" + reset + " " + sidebar.Muted + "CI unknown: gh: offline", "○ #95" + reset + " " + sidebar.Green + "✓",
		sidebar.Muted + "◌ PR #103 draft" + reset + "  " + sidebar.Gold + "●" + reset + " " + sidebar.Muted + "e2e",
		sidebar.Muted + "◌ #103" + reset + " " + sidebar.Gold + "●" + reset} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, ansi.Strip(got))
		}
	}
	rows := []row{{Session: "boss", SessionID: "$1", ID: "%1", Herd: "1", WindowActive: true},
		{Session: "w", SessionID: "$2", ID: "%2", Window: 1, Command: "claude", State: agent.Idle, StateCmd: "claude", StartedBy: "$1", PRGen: "3"}}
	shown := []sidebar.Client{{TTY: "/dev/ttys1", Session: "boss"}}
	for _, tt := range []struct {
		pr      sidebar.PR
		clients []sidebar.Client
		read    bool
	}{
		{sidebar.PR{Number: 1, State: "open", URL: "u"}, shown, true},
		{sidebar.PR{Number: 1, State: "draft", URL: "u"}, shown, true},
		{sidebar.PR{Number: 1, State: "merged", URL: "u"}, shown, false},
		{sidebar.PR{Number: 1, State: "open"}, shown, false},
		{sidebar.PR{Number: 1, State: "open", URL: "u"}, nil, false},
	} {
		look := &fakeLookups{pr: tt.pr}
		s, err := buildSnapshot(rows, tt.clients, "%1", "", look)
		if err != nil || slices.Equal(look.cis, []string{"u %2 3"}) != tt.read || (s.tiles[0].ci.state != "") != tt.read {
			t.Errorf("%+v shown %v: read %q (%v), want read %v", tt.pr, tt.clients != nil, look.cis, err, tt.read)
		}
	}
}
