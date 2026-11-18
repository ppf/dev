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

// A session with no agent yet isn't a worker: Under and a manager's counts agree with the board.
func TestSubtreeSkipsNoAgent(t *testing.T) {
	ss := subtreeSessions()
	noAgent := Session{Name: "pending", ID: "$pending", StartedBy: "boss-dev", Depth: 2, State: agent.Idle}
	i := 0
	for ; ss[i].Name != "d5"; i++ {
	}
	ss = append(ss[:i+1], append([]Session{noAgent}, ss[i+1:]...)...)
	for j := range ss {
		ss[j].Root = "manager"
	}
	rolls := Subtree(ss, nil)
	if r := rolls["boss-dev"]; r.Under != 5 {
		t.Errorf("boss-dev Under = %d, want 5", r.Under)
	}
	if r := rolls["manager"]; r.Under != 12 {
		t.Errorf("manager Under = %d, want 12", r.Under)
	}
	ss = []Session{
		{Name: "m", ID: "$1", Boss: true, Root: "m", Agent: "Claude", State: agent.Idle},
		{Name: "b", ID: "$2", Boss: true, Root: "m", State: agent.Idle},
		{Name: "w", ID: "$3", Root: "m", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Idle},
	}
	for i := range ss[1:] {
		ss[i+1].Depth = 0
	}
	if r := Subtree(ss, nil)["m"]; r.Under != 2 || r.Bosses != 1 {
		t.Errorf("manager with a no-agent boss: Under %d Bosses %d, want 2 and 1", r.Under, r.Bosses)
	}
	only := []Session{{Name: "b", ID: "$b", Boss: true, Agent: "Claude", State: agent.Idle}, {Name: "w", ID: "$w", StartedBy: "b", Depth: 1}}
	if r := Subtree(only, nil)["b"]; r.Under != 0 || hasRollLine(r) || r.Text() != "idle · no workers" {
		t.Errorf("only no-agent under = %+v %q", r, r.Text())
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

// Only an open item counts: on no one an ask apart from a plain one, else on others.
func TestListCountAdd(t *testing.T) {
	var c ListCount
	c.Add(false, "", 2)
	c.Add(false, "", 0)
	c.Add(true, "", 2)
	c.Add(false, "w1", 0)
	if c != (ListCount{Asks: 1, Plain: 1, On: 1}) {
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
	if got, _ := rollLine(r, 60); ansi.Strip(got) != "1 working ⚠ · 1 need you · 1 waiting" {
		t.Errorf("roll-up line = %q", ansi.Strip(got))
	}
}

// NeedCounts counts open items on no one, asks apart, and on others; a list not read is unread.
func TestNeedCounts(t *testing.T) {
	got := NeedCounts([]NeedList{
		{Boss: "a", Items: []NeedItem{{ID: 1, Options: []string{"x", "y"}}, {ID: 2}, {ID: 3, Done: true}, {ID: 4, On: "w"}}},
		{Boss: "b", Error: "bad json"},
	})
	if got["a"] != (ListCount{Asks: 1, Plain: 1, On: 1}) || got["b"] != (ListCount{Unread: true}) {
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

// A roll line's "N need you" is what NEEDS YOU counts for the subtree: asks and the user's own plain items, no "to do";
// an item on someone else is "on others".
func TestRollLineMatchesNeedsYou(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Idle}}
	list := NeedList{Boss: "b", Items: []NeedItem{
		{ID: 1, Options: []string{"x", "y"}}, {ID: 2}, {ID: 3}, {ID: 4, On: "w"},
	}}
	r := Subtree(ss, NeedCounts([]NeedList{list}))["b"]
	got, _ := rollLine(r, 60)
	if want := "3 need you · 1 on others"; ansi.Strip(got) != want {
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

// A workerless boss with plain items only keeps a red "N need you" first, without the idle its row already says; what is cut is the tail.
func TestRollLineWorkerlessPlainOnly(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1}})["b"]
	for _, width := range []int{20, 36, 40} {
		got, _ := rollLine(r, width)
		if !strings.HasPrefix(got, red+"1 need you"+reset) || !strings.HasPrefix(ansi.Strip(got), "1 need you · no w") {
			t.Errorf("width %d: %q", width, got)
		}
		if strings.Contains(strings.Split(ansi.Strip(got), " · ")[0], "…") {
			t.Errorf("width %d: need you cut: %q", width, ansi.Strip(got))
		}
	}
}

// Worker counts stay muted next to the red "need you", and the cost goes before they do.
func TestRollLineCountsMutedCostLast(t *testing.T) {
	ss := []Session{
		{Name: "b", ID: "$1", Boss: true, State: agent.Idle},
		{Name: "w1", ID: "$2", StartedBy: "b", Depth: 1, State: agent.Working, CostUSD: 4, HasCost: true},
		{Name: "w2", ID: "$3", StartedBy: "b", Depth: 1, State: agent.Waiting},
	}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1}})["b"]
	got, _ := rollLine(r, 60)
	if strings.Contains(got, red+"1 working") || strings.Contains(got, red+"1 waiting") || !strings.Contains(got, red+"1 need you") {
		t.Errorf("colours: %q", got)
	}
	if got, _ := rollLine(r, 28); strings.Contains(ansi.Strip(got), "$") {
		t.Errorf("cost shown over a dropped count: %q", ansi.Strip(got))
	}
}

// "N on others" comes last on the roll line, whole or not at all: a narrow sidebar drops it, never "N need you".
func TestRollLineOnOthers(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Working}}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1, On: 3}})["b"]
	for width, want := range map[int]string{60: "1 working · 1 need you · 3 on others", 30: "1 working · 1 need you"} {
		if got, _ := rollLine(r, width); ansi.Strip(got) != want {
			t.Errorf("%d columns: %q, want %q", width, ansi.Strip(got), want)
		}
	}
	if got := strings.Join(r.Parts(), " · "); got != "1 working · 1 to do · 3 on others" {
		t.Errorf("parts = %q", got)
	}
}
