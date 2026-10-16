package sidebar

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

func TestModelAgentPane(t *testing.T) {
	r := paneRow{Session: "app", Window: 1, ID: "%1", Path: "/r", Title: "t", Command: "claude", State: "idle", StateCmd: "claude", Cost: "0.5",
		Active: true, Width: 40, Height: 20, Project: "p", Sidebar: "%0", Selected: "app"}
	want := agent.Pane{Session: "app", Window: 1, ID: "%1", Path: "/r", Title: "t", Command: "claude", State: "idle", StateCmd: "claude", Cost: "0.5"}
	if got := r.agentPane(); got != want {
		t.Errorf("agentPane = %+v, want %+v", got, want)
	}
}

// modelRows is one server: app (claude, lazygit, nvim in window 2), app-2 (lazygit and nvim only),
// web (codex, grouped by @project), stale (claude exited, its state left behind), gen (generic title),
// side (an agent-looking sidebar) and w2 (agent only in window 2).
func modelRows() []paneRow {
	return []paneRow{
		{Session: "app", Window: 1, ID: "%0", Command: "dev", Sidebar: "%0", Active: true, Width: 40, Height: 30},
		{Session: "app", Window: 1, ID: "%1", Path: "/app", Title: "✳ Fix login", Command: "claude", State: "working", StateCmd: "claude", Cost: "1.5", Sidebar: "%0"},
		{Session: "app", Window: 1, ID: "%2", Path: "/app", Command: "lazygit", Sidebar: "%0"},
		{Session: "app", Window: 2, ID: "%3", Path: "/app", Command: "nvim", Sidebar: "%0"},
		{Session: "app-2", Window: 1, ID: "%4", Path: "/app2", Command: "lazygit"},
		{Session: "app-2", Window: 1, ID: "%5", Path: "/app2", Command: "nvim"},
		{Session: "web", Window: 1, ID: "%6", Path: "/web", Title: "repo | task", Command: "codex", State: "idle", StateCmd: "claude", Cost: "4", Project: "zz"},
		{Session: "stale", Window: 1, ID: "%7", Path: "/stale", Title: "✳ Old", Command: "zsh", State: "waiting", StateCmd: "claude", Cost: "3"},
		{Session: "gen", Window: 1, ID: "%8", Path: "/gen", Title: "✳ gen", Command: "claude", State: "idle", StateCmd: "claude"},
		{Session: "side", Window: 1, ID: "%9", Path: "/side", Command: "claude", StateCmd: "claude", Sidebar: "%9"},
		{Session: "w2", Window: 2, ID: "%10", Path: "/w2", Command: "claude", StateCmd: "claude"},
	}
}

func TestModelBuildSnapshot(t *testing.T) {
	var branched []string
	branch := func(path string) string {
		branched = append(branched, path)
		return "br" + path
	}
	pr := func(path, branch string) PR {
		if path != "/app" || branch != "br/app" {
			return PR{}
		}
		return PR{Number: 7, State: "open", URL: "u"}
	}
	clients := []Client{{TTY: "/dev/a", Session: "app"}}
	s, err := buildSnapshot(modelRows(), clients, "%0", "host", branch, pr)
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{
		{Name: "app", Group: "app", Branch: "br/app", Agent: "Claude", State: "working", Title: "Fix login", PR: PR{Number: 7, State: "open", URL: "u"}, Cost: "$1.50"},
		{Name: "app-2", Group: "app"},
		{Name: "gen", Group: "gen", Branch: "br/gen", Agent: "Claude", State: "idle"},
		{Name: "side", Group: "side"},
		{Name: "stale", Group: "stale"},
		{Name: "w2", Group: "w2"},
		{Name: "web", Group: "zz", Branch: "br/web", Agent: "Codex", Title: "task on repo"},
	}
	if !slices.Equal(s.sessions, want) {
		t.Errorf("sessions =\n%+v\nwant\n%+v", s.sessions, want)
	}
	if want := []string{"/app", "/web", "/gen"}; !slices.Equal(branched, want) {
		t.Errorf("branch lookups = %q, want %q (agent panes only)", branched, want)
	}
	if s.current != "app" || s.width != 40 || s.height != 30 || !s.focused || !slices.Equal(s.clients, clients) {
		t.Errorf("own pane: %+v", s)
	}

	// No client views app: no PR lookups, as another session's sidebar is out of sight
	s, err = buildSnapshot(modelRows(), []Client{{TTY: "/dev/a", Session: "web"}}, "%0", "host", branch,
		func(string, string) PR { t.Error("PR looked up for an unviewed sidebar"); return PR{} })
	if err != nil || s.sessions[0].PR != (PR{}) {
		t.Errorf("unviewed: %v, %+v", err, s.sessions[0])
	}

	if _, err := buildSnapshot(modelRows(), nil, "%99", "host", branch, pr); err == nil {
		t.Error("missing own pane: want an error")
	}
}

func TestModelSyncSelection(t *testing.T) {
	sessions := []Session{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	viewing := func(session string) []Client { return []Client{{TTY: "/dev/x", Session: session, Activity: 1}} }
	for _, tt := range []struct {
		name     string
		selected string
		s        snapshot
		want     string
	}{
		{"unfocused, client viewing own session adopts it", "a",
			snapshot{sessions: sessions, current: "b", clients: viewing("b")}, "b"},
		{"focused keeps selection", "a",
			snapshot{sessions: sessions, current: "b", focused: true, clients: viewing("b")}, "a"},
		{"client elsewhere keeps selection", "a",
			snapshot{sessions: sessions, current: "b", clients: viewing("c")}, "a"},
		{"most recently active client decides", "a",
			snapshot{sessions: sessions, current: "b", clients: []Client{{Session: "b", Activity: 1}, {Session: "c", Activity: 2}}}, "a"},
		{"no client keeps selection", "a", snapshot{sessions: sessions, current: "b"}, "a"},
		{"empty takes current", "", snapshot{sessions: sessions, current: "b", focused: true}, "b"},
		{"vanished falls back to current", "gone", snapshot{sessions: sessions, current: "c", focused: true}, "c"},
		{"current unlisted falls back to first", "gone", snapshot{sessions: sessions, current: "x", focused: true}, "a"},
		{"no sessions", "gone", snapshot{current: "x", focused: true}, ""},
	} {
		if got := syncSelection(tt.selected, tt.s); got != tt.want {
			t.Errorf("%s: syncSelection(%q) = %q, want %q", tt.name, tt.selected, got, tt.want)
		}
	}
}

func TestModelOrderOption(t *testing.T) {
	if got := orderOption(nil); got != "" {
		t.Errorf("no sessions: %q", got)
	}
	if got := orderOption([]Session{{Name: "a"}, {Name: "b"}}); got != "a\nb\n" {
		t.Errorf("orderOption = %q", got)
	}
}

// modelFakeServer answers the refresh queries: sessions app (own pane %0) and web, @sidebar_selected_session
// as selected, one client viewing app; listErr fails list-panes.
type modelFakeServer struct {
	selected string
	focused  bool
	listErr  error
	calls    [][]string
}

func (f *modelFakeServer) client() *tmux.Client {
	return tmux.NewFake(func(args []string) (string, error) {
		f.calls = append(f.calls, args)
		switch args[0] {
		case "list-panes":
			if f.listErr != nil {
				return "", f.listErr
			}
			active := "0"
			if f.focused {
				active = "1"
			}
			return modelPaneLine("app", "%0", active, "%0", f.selected) + modelPaneLine("web", "%1", "1", "", f.selected), nil
		case "list-clients":
			return tmux.FormatRow("/dev/ttys001", "app", "5"), nil
		}
		return "", nil
	})
}

func modelPaneLine(session, id, active, sidebar, selected string) string {
	return tmux.FormatRow(session, "1", id, "/", "", "zsh", "", "", "", active, "40", "30", "", sidebar, selected)
}

func modelTestModel(c *tmux.Client) Model {
	return Model{c: c, pane: "%0", host: "host",
		branches: &branchCache{git: (&modelFakeGit{}).run, now: time.Now, entries: map[string]branchEntry{}},
		prs:      &prCache{gh: (&prFakeGH{}).run, branch: prOnBranch(""), now: time.Now, sem: make(chan struct{}, prLookups), entries: map[prKey]prEntry{}}}
}

func TestModelRefresh(t *testing.T) {
	f := &modelFakeServer{focused: true}
	m := modelTestModel(f.client())
	msg := m.refresh()().(refreshMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	want := [][]string{
		{"list-panes", "-a", "-F", "#{session_name}\x1f#{window_index}\x1f#{pane_id}\x1f#{pane_current_path}\x1f#{pane_title}\x1f" +
			"#{pane_current_command}\x1f#{@agent_state}\x1f#{@agent_cmd}\x1f#{@agent_cost}\x1f#{pane_active}\x1f#{pane_width}\x1f#{pane_height}\x1f" +
			"#{@project}\x1f#{@sidebar_pane}\x1f#{@sidebar_selected_session}\x1f"},
		{"list-clients", "-F", "#{client_tty}\x1f#{client_session}\x1f#{client_activity}\x1f"},
		{"set-option", "-gq", "@sidebar_session_order", "app\nweb\n"},
	}
	if !slices.EqualFunc(f.calls, want, slices.Equal) {
		t.Errorf("first refresh calls =\n%q\nwant\n%q", f.calls, want)
	}
	if msg.selected != "app" || msg.read != "" || msg.order != "app\nweb\n" || msg.snap.current != "app" || len(msg.snap.sessions) != 2 {
		t.Errorf("msg = %+v", msg)
	}

	// apply writes the synced selection
	f.calls = nil
	m = m.apply(msg)
	if want := []string{"set-option", "-gq", "@sidebar_selected_session", "app"}; len(f.calls) != 1 || !slices.Equal(f.calls[0], want) {
		t.Errorf("apply calls = %q", f.calls)
	}

	// Nothing changed: only the two queries
	f.selected, f.calls = "app", nil
	if msg := m.refresh()().(refreshMsg); msg.err != nil || len(f.calls) != 2 {
		t.Errorf("unchanged refresh: err %v, calls %q", msg.err, f.calls)
	}

	// Another sidebar selected web: kept while focused, replaced by the viewed session once not
	f.selected, f.calls = "web", nil
	if msg := m.refresh()().(refreshMsg); msg.selected != "web" || len(f.calls) != 2 {
		t.Errorf("focused: selected %q, calls %q", msg.selected, f.calls)
	}
	f.focused, f.calls = false, nil
	msg = m.refresh()().(refreshMsg)
	if msg.selected != "app" || msg.read != "web" || len(f.calls) != 2 {
		t.Errorf("unfocused: selected %q, calls %q", msg.selected, f.calls)
	}

	f.listErr = errors.New("boom")
	if msg := m.refresh()().(refreshMsg); msg.err == nil {
		t.Error("list-panes failure: want an error")
	}
}

func TestModelApply(t *testing.T) {
	old := snapshot{current: "app", sessions: []Session{{Name: "app"}}}
	c, calls := recorder(nil)
	m := Model{c: c, snap: old, selected: "app", order: "app\n"}
	if err := m.setSelection("web"); err != nil {
		t.Fatal(err)
	}
	*calls = nil

	// A refresh started before the key: its snapshot lands, its selection sync is neither applied nor written
	next := snapshot{current: "app", sessions: []Session{{Name: "app"}, {Name: "web"}}}
	got := m.apply(refreshMsg{snap: next, selected: "app", read: "other", order: "app\nweb\n", gen: 0})
	if got.selected != "web" || len(got.snap.sessions) != 2 || got.order != "app\nweb\n" || len(*calls) != 0 {
		t.Errorf("stale gen: %+v, calls %q", got, *calls)
	}
	got = got.apply(refreshMsg{snap: next, selected: "app", read: "other", order: "app\nweb\n", gen: m.gen})
	if want := []string{"set-option", "-gq", "@sidebar_selected_session", "app"}; got.selected != "app" || len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("current gen: selected %q, calls %q", got.selected, *calls)
	}

	boom := errors.New("boom")
	failed := got.apply(refreshMsg{err: boom, gen: m.gen})
	if failed.err != boom || failed.selected != "app" || len(failed.snap.sessions) != 2 || failed.order != "app\nweb\n" {
		t.Errorf("failed refresh: %+v", failed)
	}
	if ok := failed.apply(refreshMsg{snap: next, selected: "app", order: "app\nweb\n", gen: m.gen}); ok.err != nil {
		t.Errorf("success after failure kept err %v", ok.err)
	}
}

func TestModelSetSelection(t *testing.T) {
	c, calls := recorder(nil)
	m := Model{c: c, selected: "a", gen: 3}
	if err := m.setSelection("b"); err != nil {
		t.Fatal(err)
	}
	if m.selected != "b" || m.gen != 4 {
		t.Errorf("selected %q gen %d", m.selected, m.gen)
	}
	if want := []string{"set-option", "-gq", "@sidebar_selected_session", "b"}; len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("calls = %q", *calls)
	}
}

func TestModelClient(t *testing.T) {
	m := Model{snap: snapshot{current: "app", clients: []Client{
		{TTY: "/dev/a", Session: "app", Activity: 1},
		{TTY: "/dev/b", Session: "web", Activity: 9},
		{TTY: "/dev/c", Session: "app", Activity: 2},
	}}}
	if got := m.client(); got != "/dev/c" {
		t.Errorf("client = %q, want /dev/c", got)
	}
	m.snap.current = "none"
	if got := m.client(); got != "" {
		t.Errorf("no client viewing: %q", got)
	}
}
