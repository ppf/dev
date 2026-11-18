package sidebar

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/wait"
)

// subtreeSessions is a manager over three bosses, in Order: boss-noc's own turn ended, one worker on a dialog,
// two working, one idle; boss-dev idle over five working; boss-mag with no workers.
func subtreeSessions() []Session {
	w := func(name, by, state string, depth int) Session {
		return Session{Name: name, ID: "$" + name, StartedBy: by, Depth: depth, Agent: "Claude", State: state}
	}
	boss := func(name, by string, depth int, note string) Session {
		s := w(name, by, agent.Idle, depth)
		s.Boss, s.Note = true, note
		return s
	}
	dialog := w("noc-triage", "boss-noc", agent.Waiting, 2)
	dialog.Deciding = true
	return []Session{
		boss("manager", "", 0, ""),
		boss("boss-dev", "manager", 1, "finished: shipped"),
		w("d1", "boss-dev", agent.Working, 2), w("d2", "boss-dev", agent.Working, 2), w("d3", "boss-dev", agent.Working, 2),
		w("d4", "boss-dev", agent.Working, 2), w("d5", "boss-dev", agent.Working, 2),
		boss("boss-mag", "manager", 1, "finished: done"),
		boss("boss-noc", "manager", 1, "finished: VPN?"),
		w("n1", "boss-noc", agent.Working, 2), dialog, w("n3", "boss-noc", agent.Working, 2), w("n4", "boss-noc", agent.Idle, 2),
	}
}

// Boss rows: the state first, then the counts, strongest first, only when non-zero.
func TestSubtreeText(t *testing.T) {
	lists := map[string]ListCount{"boss-noc": {Asks: 1, Plain: 1}, "boss-dev": {Plain: 1}}
	rolls := Subtree(subtreeSessions(), lists)
	for name, want := range map[string]string{
		"boss-noc": "needs you · 2 questions · 2 working · 1 to do",
		"boss-dev": "5 working · 1 to do",
		"boss-mag": "idle · no workers",
		"manager":  "needs you · 2 questions · 7 working · 2 to do",
	} {
		if got := rolls[name].Text(); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, ok := rolls["d1"]; ok {
		t.Error("a worker has a roll-up")
	}
	if r := rolls["boss-noc"]; r.Rank != RankNeedsYou || r.Under != 4 {
		t.Errorf("boss-noc = %+v", r)
	}
}

func TestSubtreeEdges(t *testing.T) {
	held := wait.Wait{Until: time.Now().Add(time.Hour), Reason: "CI"}
	boss := Session{Name: "b", ID: "$b", Boss: true, Agent: "Claude", State: agent.Idle}
	under := func(s Session) []Session {
		s.Name, s.ID, s.StartedBy, s.Depth, s.Agent = "w", "$w", "b", 1, "Claude"
		return []Session{boss, s}
	}
	cases := []struct {
		name     string
		sessions []Session
		lists    map[string]ListCount
		rank     Rank
		text     string
		warn     bool
	}{
		{"planned wait", under(Session{State: agent.Idle, Wait: held}), nil, RankWaiting, "1 waiting", false},
		{"dialog during a planned wait", under(Session{State: agent.Waiting, Deciding: true, Wait: held}), nil, RankNeedsYou, "needs you · 1 question", false},
		{"rate limited", under(Session{State: agent.Idle, Resume: true, Note: "failed: rate limit"}), nil, RankWaiting, "1 waiting", true},
		{"stale needs a decision note", under(Session{State: agent.Idle, Note: "needs a decision: Bash"}), nil, RankIdle, "idle", false},
		{"asks note", under(Session{State: agent.Idle, Note: "asks: Merge?"}), nil, RankNeedsYou, "needs you · 1 question", false},
		{"resumed by hand", under(Session{State: agent.Working, Resume: true}), nil, RankWorking, "1 working", false},
		{"asks note while working", under(Session{State: agent.Working, Note: "asks: Merge?"}), nil, RankWorking, "1 working", false},
		{"idle reminder", under(Session{State: agent.Waiting}), nil, RankIdle, "idle", false},
		{"stuck worker working", under(Session{State: agent.Working, Note: "stuck: 20m"}), nil, RankWorking, "1 working", true},
		{"list not read", under(Session{State: agent.Idle}), map[string]ListCount{"b": {Unread: true}}, RankIdle, "idle", true},
		{"plain items only", under(Session{State: agent.Idle}), map[string]ListCount{"b": {Plain: 3}}, RankIdle, "idle · 3 to do", false},
		{"own asks note", []Session{{Name: "b", Boss: true, Agent: "Claude", State: agent.Idle, Note: "asks: VPN?"}}, nil, RankNeedsYou, "needs you · 1 question", false},
		{"own work only", []Session{{Name: "b", Boss: true, Agent: "Claude", State: agent.Working}}, nil, RankWorking, "working", false},
	}
	for _, c := range cases {
		r := Subtree(c.sessions, c.lists)["b"]
		if r.Rank != c.rank || r.Text() != c.text || r.Warn != c.warn {
			t.Errorf("%s: rank %d %q warn %v, want %d %q %v", c.name, r.Rank, r.Text(), r.Warn, c.rank, c.text, c.warn)
		}
	}
}

// A boss under a boss: its sessions and its list count at each ancestor, once.
func TestSubtreeGrandchild(t *testing.T) {
	ss := Order([]Session{
		{Name: "m", ID: "$1", Boss: true, State: agent.Idle},
		{Name: "b", ID: "$2", Boss: true, StartedBy: "m", State: agent.Idle},
		{Name: "c", ID: "$3", Boss: true, StartedBy: "b", State: agent.Idle},
		{Name: "w", ID: "$4", StartedBy: "c", State: agent.Idle, Note: "asks: which?"},
	})
	rolls := Subtree(ss, map[string]ListCount{"c": {Asks: 1}})
	for _, name := range []string{"m", "b", "c"} {
		if got := rolls[name].Text(); got != "needs you · 2 questions" {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// Only an open item on no one counts: an ask apart from a plain one.
func TestListCountAdd(t *testing.T) {
	var c ListCount
	c.Add(false, "", 2)
	c.Add(false, "", 0)
	c.Add(true, "", 2)
	c.Add(false, "w1", 0)
	if c != (ListCount{Asks: 1, Plain: 1}) {
		t.Errorf("count = %+v", c)
	}
}

// The sidebar reads a worker's note and rate-limit resume off its agent pane into its boss's roll-up line.
func TestSubtreeFromPanes(t *testing.T) {
	agentRow := func(session, id, pane, by, state string) paneRow {
		return paneRow{Session: session, SessionID: id, Window: 1, ID: pane, Command: "claude", StateCmd: "claude", State: state, StartedBy: by}
	}
	boss := agentRow("boss", "$1", "%1", "", agent.Idle)
	boss.Herd = "1"
	limited, stuck := agentRow("w1", "$2", "%2", "boss", agent.Idle), agentRow("w2", "$3", "%3", "boss", agent.Working)
	limited.ResumeAt, stuck.Note = "1700000000", "stuck: 20m"
	rows := []paneRow{{Session: "boss", SessionID: "$1", Window: 1, ID: "%0", Command: "dev", Sidebar: "%0", Width: 40, Height: 30, Herd: "1"}, boss, limited, stuck}
	s, err := buildSnapshot(rows, nil, "%0", "host", func(string) string { return "" }, func(string, string, string, string, string) PR { return PR{} })
	if err != nil {
		t.Fatal(err)
	}
	r := Subtree(s.sessions, NeedCounts([]NeedList{{Boss: "boss", Items: []NeedItem{{ID: 1, Text: "DM"}}}}))["boss"]
	if got, _ := rollLine(r, 60); ansi.Strip(got) != "1 working ⚠ · 1 waiting · 1 need you" {
		t.Errorf("roll-up line = %q", ansi.Strip(got))
	}
}

// NeedCounts counts open items on no one, asks apart; a list not read is unread.
func TestNeedCounts(t *testing.T) {
	got := NeedCounts([]NeedList{
		{Boss: "a", Items: []NeedItem{{ID: 1, Options: []string{"x", "y"}}, {ID: 2}, {ID: 3, Done: true}, {ID: 4, On: "w"}}},
		{Boss: "b", Error: "bad json"},
	})
	if got["a"] != (ListCount{Asks: 1, Plain: 1}) || got["b"] != (ListCount{Unread: true}) {
		t.Errorf("counts = %+v", got)
	}
}

// A list's ask alone makes a boss need you: "1 need you" in red on the sidebar.
func TestRollLineListAsk(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, State: agent.Idle}}
	got, _ := rollLine(Subtree(ss, map[string]ListCount{"b": {Asks: 1}})["b"], 40)
	if !strings.HasPrefix(got, red+"1 need you") {
		t.Errorf("line = %q", got)
	}
}

// A roll line's "N need you" is what NEEDS YOU counts for the subtree: asks and the user's own plain items, no "to do".
func TestRollLineMatchesNeedsYou(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, State: agent.Idle}}
	list := NeedList{Boss: "b", Items: []NeedItem{
		{ID: 1, Options: []string{"x", "y"}}, {ID: 2}, {ID: 3}, {ID: 4, On: "w"},
	}}
	r := Subtree(ss, NeedCounts([]NeedList{list}))["b"]
	got, _ := rollLine(r, 60)
	if want := "3 need you"; ansi.Strip(got) != want {
		t.Errorf("line = %q, want %q", ansi.Strip(got), want)
	}
	if n := needCount(NeedRows(ss, []NeedList{list}, "")); n != 3 {
		t.Errorf("NEEDS YOU = %d, want 3", n)
	}
	// Plain items alone keep the state and say them as "need you"
	r = Subtree(ss, map[string]ListCount{"b": {Plain: 2}})["b"]
	if got, _ := rollLine(r, 60); ansi.Strip(got) != "idle · 2 need you" {
		t.Errorf("plain only = %q", ansi.Strip(got))
	}
}

// A manager's "N need you" by its plain items alone stays by its state, whatever the width.
func TestRollLineManagerPlainOnly(t *testing.T) {
	ss := []Session{
		{Name: "m", ID: "$1", Boss: true, Root: "m", State: agent.Idle},
		{Name: "b", ID: "$2", Boss: true, Root: "m", Depth: 1, State: agent.Idle},
	}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 2}})["m"]
	if got, _ := rollLine(r, 22); !strings.HasPrefix(ansi.Strip(got), "idle · 2 need you") {
		t.Errorf("line = %q", ansi.Strip(got))
	}
}
