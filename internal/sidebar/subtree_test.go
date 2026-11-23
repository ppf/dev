package sidebar

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/spent"
	"github.com/ppf/dev/internal/tmux"
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
		"boss-noc": "1 need you · 1 decide · 2 working · 1 to do",
		"boss-dev": "5 working · 1 to do",
		"boss-mag": "idle · no workers",
		"manager":  "1 need you · 1 decide · 7 working · 2 to do",
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
		{"dialog during a planned wait", under(Session{State: agent.Waiting, Deciding: true, Wait: held}), nil, RankNeedsYou, "1 need you", false},
		{"rate limited", under(Session{State: agent.Idle, Resume: true, Note: "failed: rate limit"}), nil, RankWaiting, "1 waiting", true},
		{"stale needs a decision note", under(Session{State: agent.Idle, Note: "needs a decision: Bash"}), nil, RankIdle, "idle", false},
		{"asks note", under(Session{State: agent.Idle, Note: "asks: Merge?"}), nil, RankNeedsYou, "1 need you", false},
		{"resumed by hand", under(Session{State: agent.Working, Resume: true}), nil, RankWorking, "1 working", false},
		{"asks note while working", under(Session{State: agent.Working, Note: "asks: Merge?"}), nil, RankWorking, "1 working", false},
		{"idle reminder", under(Session{State: agent.Waiting}), nil, RankIdle, "idle", false},
		{"stuck worker working", under(Session{State: agent.Working, Note: "stuck: 20m"}), nil, RankWorking, "1 working", true},
		{"list not read", under(Session{State: agent.Idle}), map[string]ListCount{"b": {Unread: true}}, RankIdle, "idle", true},
		{"plain items only", under(Session{State: agent.Idle}), map[string]ListCount{"b": {Plain: 3}}, RankIdle, "idle · 3 to do", false},
		{"own asks note", []Session{{Name: "b", Boss: true, Agent: "Claude", State: agent.Idle, Note: "asks: VPN?"}}, nil, RankNeedsYou, "1 need you", false},
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
		if got := rolls[name].Text(); got != "1 need you · 1 decide" {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// Only an open item counts, by its status: an ask to decide, the user's own plain item, else on others; a closed
// boss's, a moved PR's or the boss's own (on boss) in none.
func TestListCountAdd(t *testing.T) {
	var c ListCount
	c.Add(NeedItem{Status: StatusDecide, Column: ColumnTodo, Assignee: you, NeedsYou: true})
	c.Add(NeedItem{Status: StatusYours, Column: ColumnTodo, Assignee: you})
	c.Add(NeedItem{Done: true})
	c.Add(NeedItem{Status: StatusWorking, Column: ColumnProgress, On: "dev-w"})
	c.Add(NeedItem{On: "Carl"})
	c.Add(NeedItem{Status: StatusBossClosed})
	c.Add(NeedItem{Status: StatusCheck, Column: ColumnReview})
	c.Add(NeedItem{Status: StatusIdle, Column: ColumnProgress, StatusOn: "boss-dev", On: "boss"})
	c.Add(NeedItem{Status: StatusWorking, Column: ColumnProgress, StatusOn: "boss-dev", On: "boss"})
	if c != (ListCount{Asks: 1, Plain: 1, On: 2}) {
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
	r := Subtree(s.sessions, NeedCounts([]NeedList{{Boss: "boss", Items: []NeedItem{{ID: 1, Text: "DM", Status: StatusYours, Column: ColumnTodo, Assignee: you}}}}))["boss"]
	if got, _ := rollLine(r, 60); ansi.Strip(got) != "1 working ⚠ · 1 to do · 1 waiting" {
		t.Errorf("roll-up line = %q", ansi.Strip(got))
	}
}

// NeedCounts counts open items on no one, asks apart, and on others; a list not read is unread.
func TestNeedCounts(t *testing.T) {
	got := NeedCounts([]NeedList{
		{Boss: "a", Items: []NeedItem{{ID: 1, Options: []string{"x", "y"}, Status: StatusDecide, Column: ColumnTodo, Assignee: you, NeedsYou: true}, {ID: 2, Status: StatusYours, Column: ColumnTodo, Assignee: you}, {ID: 3, Done: true}, {ID: 4, On: "w", Status: StatusWaiting, Column: ColumnWaiting, StatusOn: "w"}}},
		{Boss: "b", Error: "bad json"},
	})
	if got["a"] != (ListCount{Asks: 1, Plain: 1, On: 1}) || got["b"] != (ListCount{Unread: true}) {
		t.Errorf("counts = %+v", got)
	}
}

// A list's ask alone makes a boss need you: "1 decide" in red on the sidebar.
func TestRollLineListAsk(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, State: agent.Idle}}
	got, _ := rollLine(Subtree(ss, map[string]ListCount{"b": {Asks: 1}})["b"], 40)
	if !strings.HasPrefix(got, red+"1 decide") {
		t.Errorf("line = %q", got)
	}
}

// A roll line counts by status: asks to decide, the user's own plain items to do (one filed on the boss's own name
// too: its status is yours), an item on someone else on others; TO DO counts the same list by card: need you, mine.
func TestRollLineMatchesToDo(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Idle}}
	list := NeedList{Boss: "b", Items: []NeedItem{
		{ID: 1, Options: []string{"x", "y"}, Status: StatusDecide, Column: ColumnTodo, Assignee: you, NeedsYou: true},
		{ID: 2, Status: StatusYours, Column: ColumnTodo, Assignee: you}, {ID: 3, On: "b", Status: StatusYours, Column: ColumnTodo, Assignee: you},
		{ID: 4, On: "w", Status: StatusIdle, StatusOn: "w", Column: ColumnProgress, Assignee: NeedAssignee{Kind: AssigneeWorker, Name: "w"}},
	}}
	r := Subtree(ss, NeedCounts([]NeedList{list}))["b"]
	got, _ := rollLine(r, 60)
	if want := "1 decide · 2 to do · 1 on others"; ansi.Strip(got) != want {
		t.Errorf("line = %q, want %q", ansi.Strip(got), want)
	}
	if !strings.HasPrefix(got, red+"1 decide"+reset) || !strings.Contains(got, muted+"2 to do"+reset) {
		t.Errorf("decide red, to do muted: %q", got)
	}
	if c := Count(NeedRows(ss, []NeedList{list}, "")); c != (Tally{NeedYou: 1, Mine: 3}) {
		t.Errorf("TO DO = %+v, want 1 need you, 3 mine", c)
	}
	// Plain items alone say "to do", the state left to the row above
	r = Subtree(ss, map[string]ListCount{"b": {Plain: 2}})["b"]
	if got, _ := rollLine(r, 60); ansi.Strip(got) != "2 to do" {
		t.Errorf("plain only = %q", ansi.Strip(got))
	}
}

// A manager's "N to do" by its plain items alone leads, whatever the width.
func TestRollLineManagerPlainOnly(t *testing.T) {
	ss := []Session{
		{Name: "m", ID: "$1", Boss: true, Root: "m", State: agent.Idle},
		{Name: "b", ID: "$2", Boss: true, Root: "m", Depth: 1, State: agent.Idle},
	}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 2}})["m"]
	if got, _ := rollLine(r, 22); !strings.HasPrefix(ansi.Strip(got), "2 to do") {
		t.Errorf("line = %q", ansi.Strip(got))
	}
}

// A workerless boss with plain items only keeps a muted "N to do", without the idle or "no workers" its rows already say.
func TestRollLineWorkerlessPlainOnly(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1}})["b"]
	for _, width := range []int{20, 36, 40} {
		got, _ := rollLine(r, width)
		if !strings.HasPrefix(got, muted+"1 to do"+reset) || ansi.Strip(got) != "1 to do" {
			t.Errorf("width %d: %q", width, got)
		}
		if strings.Contains(strings.Split(ansi.Strip(got), " · ")[0], "…") {
			t.Errorf("width %d: to do cut: %q", width, ansi.Strip(got))
		}
	}
}

// A workerless boss: "no workers" never shares the line with a to do, the cost or "on others".
func TestRollLineNoWorkersLast(t *testing.T) {
	boss := Session{Name: "b", ID: "$1", Boss: true, State: agent.Idle, HasCost: true, CostUSD: 28.47}
	for _, tc := range []struct {
		name  string
		list  ListCount
		wants map[int]string
	}{
		{"to do", ListCount{Plain: 16, On: 15}, map[int]string{36: "16 to do · $28.47", 40: "16 to do · $28.47 · 15 on others"}},
		{"ask open", ListCount{Asks: 16, On: 15}, map[int]string{34: "16 decide · $28.47", 40: "16 decide · $28.47 · 15 on others"}},
	} {
		r := Subtree([]Session{boss}, map[string]ListCount{"b": tc.list})["b"]
		// rollLine gets the pane width less 5
		for pane, want := range tc.wants {
			got, _ := rollLine(r, pane-5)
			if ansi.Strip(got) != want {
				t.Errorf("%s, %d columns: %q, want %q", tc.name, pane, ansi.Strip(got), want)
			}
		}
	}
	// Nothing else on the line: "no workers" fills it
	alone := Subtree([]Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}}, nil)["b"]
	alone.ClosedUSD = 1
	if got, _ := rollLine(alone, 35); ansi.Strip(got) != "no workers" {
		t.Errorf("alone = %q", ansi.Strip(got))
	}
}

// Worker counts stay muted next to the red "decide", and the cost goes before they do.
func TestRollLineCountsMutedCostLast(t *testing.T) {
	ss := []Session{
		{Name: "b", ID: "$1", Boss: true, State: agent.Idle},
		{Name: "w1", ID: "$2", StartedBy: "b", Depth: 1, State: agent.Working, CostUSD: 4, HasCost: true},
		{Name: "w2", ID: "$3", StartedBy: "b", Depth: 1, State: agent.Waiting},
	}
	r := Subtree(ss, map[string]ListCount{"b": {Asks: 1}})["b"]
	got, _ := rollLine(r, 60)
	if strings.Contains(got, red+"1 working") || strings.Contains(got, red+"1 waiting") || !strings.Contains(got, red+"1 decide") {
		t.Errorf("colours: %q", got)
	}
	if got, _ := rollLine(r, 26); strings.Contains(ansi.Strip(got), "$") {
		t.Errorf("cost shown over a dropped count: %q", ansi.Strip(got))
	}
}

// "N on others" comes last on the roll line, whole or not at all: a narrow sidebar drops it, never "N to do".
func TestRollLineOnOthers(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Working}}
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1, On: 3}})["b"]
	for width, want := range map[int]string{60: "1 working · 1 to do · 3 on others", 30: "1 working · 1 to do"} {
		if got, _ := rollLine(r, width); ansi.Strip(got) != want {
			t.Errorf("%d columns: %q, want %q", width, ansi.Strip(got), want)
		}
	}
	if got := strings.Join(r.Parts(), " · "); got != "1 working · 1 to do · 3 on others" {
		t.Errorf("parts = %q", got)
	}
}

// A boss's cost adds what ended under it, its own ledger and its bosses', but not a conversation live again
// (resumed), whose cost its pane shows.
func TestSubtreeSpent(t *testing.T) {
	ss := []Session{
		{Name: "m", ID: "$1", Boss: true, Root: "m", CostUSD: 1, HasCost: true, Conversation: "cm"},
		{Name: "b", ID: "$2", Boss: true, StartedBy: "m", Root: "m", Depth: 1, CostUSD: 2, HasCost: true, Conversation: "cb"},
		{Name: "w", ID: "$3", StartedBy: "b", Root: "m", Depth: 2, Agent: "Claude", CostUSD: 4, HasCost: true, Conversation: "resumed"},
	}
	Spend(ss, map[string]spent.Ledger{"$1": {"closed-boss": 16}, "$2": {"closed": 8, "resumed": 3}})
	rolls := Subtree(ss, nil)
	if r := rolls["b"]; r.CostUSD != 14 || r.ClosedUSD != 8 || !r.HasCost {
		t.Errorf("boss: cost %v, closed %v, has %v; want 14, 8, true", r.CostUSD, r.ClosedUSD, r.HasCost)
	}
	if r := rolls["m"]; r.CostUSD != 31 || r.ClosedUSD != 24 {
		t.Errorf("manager: cost %v, closed %v; want 31, 24", r.CostUSD, r.ClosedUSD)
	}
	// Nothing live, only what closed: the total still shows
	only := []Session{{Name: "b", ID: "$1", Boss: true}}
	Spend(only, map[string]spent.Ledger{"$1": {"c": 2.5}})
	if r := Subtree(only, nil)["b"]; r.CostUSD != 2.5 || !r.HasCost || !hasRollLine(r) {
		t.Errorf("closed only: cost %v, has %v", r.CostUSD, r.HasCost)
	}
}

// The cost goes before "N on others", so a 40-column sidebar shows it; "N on others" is dropped first.
func TestRollLineCostBeforeOnOthers(t *testing.T) {
	ss := []Session{
		{Name: "b", ID: "$1", Boss: true, State: agent.Idle, CostUSD: 6.4, HasCost: true},
		{Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Working, CostUSD: 27.82, HasCost: true},
	}
	Spend(ss, map[string]spent.Ledger{"$1": {"c": 20.71}})
	r := Subtree(ss, map[string]ListCount{"b": {Plain: 1, On: 2}})["b"]
	for width, want := range map[int]string{
		60: "1 working · 1 to do · $54.93 · 2 on others",
		35: "1 working · 1 to do · $54.93",
		25: "1 working · 1 to do",
	} {
		if got, _ := rollLine(r, width); ansi.Strip(got) != want {
			t.Errorf("%d columns: %q, want %q", width, ansi.Strip(got), want)
		}
	}
}

// The sidebar reads each session's ledger and its agent pane's conversation: the boss's roll-up adds what closed,
// not a conversation live again; an unreadable ledger is a ⚠ and the total leaves it out.
func TestSubtreeSpentFromPanes(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	ended := func(conversation string, usd string) {
		t.Helper()
		c := tmux.NewFake(func([]string) (string, error) {
			return tmux.FormatRow("/s", "$1", "100", "1", "", usd, conversation), nil
		})
		if err := spent.Ended(c, "%1", conversation); err != nil {
			t.Fatal(err)
		}
	}
	ended("closed", "8")
	ended("resumed", "3")
	boss := paneRow{Session: "boss", SessionID: "$1", Window: 1, ID: "%1", Command: "claude", StateCmd: "claude", State: agent.Idle,
		Herd: "1", Cost: "2", Socket: "/s", Created: "100", Conversation: "c-boss"}
	w := paneRow{Session: "w", SessionID: "$2", Window: 1, ID: "%2", Command: "claude", StateCmd: "claude", State: agent.Working,
		StartedBy: "boss", Cost: "4", Socket: "/s", Created: "200", Conversation: "resumed"}
	rows := []paneRow{{Session: "boss", SessionID: "$1", Window: 1, ID: "%0", Command: "dev", Sidebar: "%0", Width: 40, Height: 30, Herd: "1", Socket: "/s", Created: "100"}, boss, w}
	snap := func() *Rollup {
		t.Helper()
		s, err := buildSnapshot(rows, nil, "%0", "host", func(string) string { return "" }, func(string, string, string, string, string) PR { return PR{} })
		if err != nil {
			t.Fatal(err)
		}
		return Subtree(s.sessions, nil)["boss"]
	}
	if r := snap(); r.CostUSD != 14 || r.ClosedUSD != 8 || r.Warn {
		t.Errorf("cost %v, closed %v, warn %v; want 14, 8, false", r.CostUSD, r.ClosedUSD, r.Warn)
	}
	ledgers, _ := filepath.Glob(filepath.Join(state, "dev", "spent", "*.json"))
	if len(ledgers) != 1 {
		t.Fatalf("ledgers %v", ledgers)
	}
	if err := os.WriteFile(ledgers[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := snap(); r.HasCost || !r.Warn || !strings.Contains(r.CostErr, ledgers[0]) || !hasRollLine(r) {
		t.Errorf("unreadable: has cost %v, warn %v, err %q", r.HasCost, r.Warn, r.CostErr)
	}
}

// A manager's bosses and workers are whole or dropped, workers first: never a cut count or a dangling "…".
func TestRollLineManagerPartsWhole(t *testing.T) {
	ss := []Session{
		{Name: "m", ID: "$1", Boss: true, Root: "m", State: agent.Working, CostUSD: 1, HasCost: true},
		{Name: "b1", ID: "$2", Boss: true, StartedBy: "m", Root: "m", State: agent.Idle},
		{Name: "b2", ID: "$3", Boss: true, StartedBy: "m", Root: "m", State: agent.Idle},
	}
	for i := 4; i < 10; i++ {
		ss = append(ss, Session{Name: fmt.Sprint("w", i), ID: fmt.Sprint("$", i), StartedBy: "b1", Root: "m", Depth: 2, Agent: "Claude", State: agent.Working, CostUSD: 2, HasCost: true})
	}
	r := Subtree(ss, map[string]ListCount{"m": {Plain: 6}})["m"]
	for width, want := range map[int]string{
		60: "6 working · 6 to do · 2 bosses · 6 workers · $13.00",
		44: "6 working · 6 to do · 2 bosses · 6 workers",
		40: "6 working · 6 to do · 2 bosses",
		36: "6 working · 6 to do · 2 bosses",
		32: "6 working · 6 to do · 2 bosses",
	} {
		got, _ := rollLine(r, width)
		if ansi.Strip(got) != want {
			t.Errorf("%d columns: %q, want %q", width, ansi.Strip(got), want)
		}
	}
}

// A bare idle or working is the row above's: the roll line starts with the next part; a count stays.
func TestRollLineLeavesOutOwnState(t *testing.T) {
	boss := func(state string) Session { return Session{Name: "b", ID: "$1", Boss: true, State: state} }
	worker := func(state string) Session {
		return Session{Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: state}
	}
	list := map[string]ListCount{"b": {Plain: 7, On: 6}}
	for _, tc := range []struct {
		name string
		ss   []Session
		want string
	}{
		{"working boss, idle worker", []Session{boss(agent.Working), worker(agent.Idle)}, "7 to do · 6 on others"},
		{"working boss, no workers", []Session{boss(agent.Working)}, "7 to do · 6 on others"},
		// A held wait is not on the row above: it stays
		{"resuming boss", []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle, Resume: true}, worker(agent.Idle)}, "waiting · 7 to do · 6 on others"},
		{"idle boss, idle worker", []Session{boss(agent.Idle), worker(agent.Idle)}, "7 to do · 6 on others"},
		{"idle boss, no workers", []Session{boss(agent.Idle)}, "7 to do · 6 on others"},
		{"idle boss, working worker", []Session{boss(agent.Idle), worker(agent.Working)}, "1 working · 7 to do · 6 on others"},
		{"working boss, working worker", []Session{boss(agent.Working), worker(agent.Working)}, "1 working · 7 to do · 6 on others"},
	} {
		r := Subtree(tc.ss, list)["b"]
		for _, width := range []int{36, 40} {
			got, _ := rollLine(r, width)
			want := tc.want
			if width == 36 && strings.HasSuffix(want, " · 6 on others") && ansi.StringWidth(want) > 36 {
				want = strings.TrimSuffix(want, " · 6 on others")
			}
			if ansi.Strip(got) != want {
				t.Errorf("%s, %d columns: %q, want %q", tc.name, width, ansi.Strip(got), want)
			}
		}
	}
	// Nothing else to say: the state stays rather than an empty line
	r := Subtree([]Session{boss(agent.Working), worker(agent.Idle)}, nil)["b"]
	if got, _ := rollLine(r, 40); ansi.Strip(got) != "working" {
		t.Errorf("state only = %q", ansi.Strip(got))
	}
}

// Nothing but the state fits the width: the state, not an empty line; the first part is measured without a separator.
func TestRollLineNarrowKeepsSomething(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}, {Name: "w", ID: "$2", StartedBy: "b", Depth: 1, Agent: "Claude", State: agent.Idle}}
	r := Subtree(ss, map[string]ListCount{"b": {On: 12}})["b"]
	if got, _ := rollLine(r, 13); ansi.Strip(got) != "12 on others" {
		t.Errorf("first part = %q", ansi.Strip(got))
	}
	if got, _ := rollLine(r, 8); ansi.Strip(got) != "idle" {
		t.Errorf("nothing fits = %q", ansi.Strip(got))
	}
}

// The manager's roll-up leaves its closed bosses' items out of decide and to do, a list not read only ⚠; its TO DO
// counts them by card; no other boss's roll-up takes them.
func TestSubtreeClosedLists(t *testing.T) {
	sessions := subtreeSessions()
	needs := []NeedList{
		{Boss: "boss-noc", Items: []NeedItem{{ID: 1, Text: "a", Options: []string{"Yes"}, Status: StatusDecide, Column: ColumnTodo, Assignee: you, NeedsYou: true},
			{ID: 2, Text: "b", Status: StatusYours, Column: ColumnTodo, Assignee: you}}},
		{Boss: "boss-dev", Items: []NeedItem{{ID: 1, Text: "c", Status: StatusYours, Column: ColumnTodo, Assignee: you}}},
		{Boss: "boss-old", Closed: true, Items: []NeedItem{{ID: 1, Text: "d", Status: StatusBossClosed, Column: ColumnTodo, Assignee: you},
			{ID: 2, Text: "e", Status: StatusBossClosed, Column: ColumnTodo, Assignee: you},
			{ID: 3, Text: "f", On: "Ana", Status: StatusBossClosed, StatusOn: "Ana", Column: ColumnWaiting, Assignee: NeedAssignee{Kind: AssigneeBoss, Name: "boss-old"}, WaitingFor: "Ana"},
			{ID: 4, Text: "g", Options: []string{"Yes"}, Status: StatusBossClosed, Column: ColumnTodo, Assignee: you}, {ID: 5, Text: "h", Done: true}}},
	}
	rolls := Subtree(sessions, NeedCounts(needs))
	if got, want := rolls[Manager].Text(), "1 need you · 1 decide · 7 working · 2 to do"; got != want {
		t.Errorf("manager = %q, want %q", got, want)
	}
	if got, want := rolls["boss-dev"].Text(), "5 working · 1 to do"; got != want {
		t.Errorf("boss-dev = %q, want %q", got, want)
	}
	r := rolls[Manager]
	var yours []NeedRow
	for _, row := range NeedRows(sessions, needs, Manager) {
		if row.Yours() {
			yours = append(yours, row)
		}
	}
	// TO DO counts by the cards, as the web board: the closed boss's cards on the user too
	if c := Count(yours); c != (Tally{NeedYou: r.Questions + r.Decide, Mine: 6}) {
		t.Errorf("TO DO %+v, want %d need you · 6 mine", c, r.Questions+r.Decide)
	}
}

// A test item counts nowhere: not in the roll-ups, nor TO DO's tally; it alone doesn't make its boss need you,
// and its row's text shows after a TEST tag, the row after it untagged.
func TestQuietCountsNowhere(t *testing.T) {
	ss := []Session{{Name: "b", ID: "$1", Boss: true, State: agent.Idle}}
	list := NeedList{Boss: "b", Items: []NeedItem{
		{ID: 1, Text: "test?", Options: []string{"x", "y"}, Status: StatusDecide, Quiet: true, Column: ColumnTodo, Assignee: you, NeedsYou: true},
		{ID: 2, Text: "test", Status: StatusYours, Quiet: true, Column: ColumnTodo, Assignee: you},
		{ID: 3, Text: "real", Status: StatusYours, Column: ColumnTodo, Assignee: you},
	}}
	counts := NeedCounts([]NeedList{list})
	if counts["b"] != (ListCount{Plain: 1}) {
		t.Errorf("counts = %+v, want 1 plain", counts["b"])
	}
	if r := Subtree(ss, counts)["b"]; r.Rank == RankNeedsYou || r.Decide != 0 {
		t.Errorf("roll-up %+v: a test ask needs no one", r)
	}
	rows := NeedRows(ss, []NeedList{list}, "")
	if c := Count(rows); c != (Tally{Mine: 1}) {
		t.Errorf("TO DO = %+v, want 1 mine", c)
	}
	for _, r := range rows {
		if r.Quiet != (r.ID != 3) {
			t.Errorf("row #%d quiet = %v", r.ID, r.Quiet)
		}
		if got := ansi.Strip(Tag(r.Quiet, r.Text)); got != map[int]string{1: "TEST test?", 2: "TEST test", 3: "real"}[r.ID] {
			t.Errorf("row #%d shows %q", r.ID, got)
		}
	}
}

// A plain worker's question to its running boss is the boss's: no need you, no NEEDS YOU row, a resting dot and
// "asks its boss"; with no running boss (closed, or a session the user started) it stays the user's, as does a boss's
// own question and a dialog.
func TestAsksBoss(t *testing.T) {
	boss := Session{Name: "b", ID: "$b", Boss: true, Agent: "Claude", State: agent.Idle}
	worker := func(s Session) Session {
		s.Name, s.ID, s.StartedBy, s.Depth, s.Agent, s.Group, s.Project = "w", "$w", "b", 1, "Claude", "g", "g"
		return s
	}
	asks := worker(Session{State: agent.Idle, Note: "asks: Merge?"})
	reminder := worker(Session{State: agent.Waiting, Note: "asks: Merge?"})
	dialog := worker(Session{State: agent.Waiting, Deciding: true, Note: "asks: Merge?"})
	for _, c := range []struct {
		name     string
		sessions []Session
		rank     Rank
		text     string
		rows     int
	}{
		{"boss runs", []Session{boss, asks}, RankIdle, "idle", 0},
		{"boss runs, idle reminder", []Session{boss, reminder}, RankIdle, "idle", 0},
		{"boss runs, a dialog", []Session{boss, dialog}, RankNeedsYou, "1 need you", 1},
		{"no boss running", []Session{asks}, RankNeedsYou, "", 1},
		{"boss's coordinator gone", []Session{{Name: "b", ID: "$b", Boss: true, State: agent.Idle}, asks}, RankNeedsYou, "", 1},
		{"started by a coordinator outside tmux", []Session{boss, func() Session { s := asks; s.Outside = true; return s }()}, RankNeedsYou, "1 need you", 1},
		{"boss runs, its inbox off", []Session{{Name: "b", ID: "$b", Boss: true, InboxOff: true, Agent: "Claude", State: agent.Idle}, asks}, RankNeedsYou, "1 need you", 1},
		{"started by a worker, not a boss", []Session{{Name: "b", ID: "$b", Agent: "Claude", State: agent.Idle}, asks}, RankNeedsYou, "", 1},
	} {
		ss := slices.Clone(c.sessions)
		MarkBossRuns(ss)
		w := ss[len(ss)-1]
		if w.Rank() != c.rank {
			t.Errorf("%s: rank %d, want %d", c.name, w.Rank(), c.rank)
		}
		if r := Subtree(ss, nil)["b"]; c.text != "" && r.Text() != c.text {
			t.Errorf("%s: roll-up %q, want %q", c.name, r.Text(), c.text)
		}
		rows := NeedRows(ss, nil, "")
		if len(rows) != c.rows || Count(rows).NeedYou != c.rows {
			t.Errorf("%s: %d NEEDS YOU rows, need you %d; want %d", c.name, len(rows), Count(rows).NeedYou, c.rows)
		}
	}
	// A boss's own question stays its user's, whoever started it
	m := Session{Name: "m", ID: "$m", Boss: true, Agent: "Claude", State: agent.Idle}
	own := []Session{m, {Name: "b", ID: "$b", Boss: true, StartedBy: "m", Agent: "Claude", State: agent.Idle, Note: "asks: VPN?"}}
	MarkBossRuns(own)
	if !own[1].BossRuns || own[1].Rank() != RankNeedsYou {
		t.Errorf("a boss's own asks under a running manager: BossRuns %v rank %d, want true and needs you", own[1].BossRuns, own[1].Rank())
	}
	// The sidebar row: a resting dot, "asks its boss" in gold, not red
	ss := []Session{boss, asks, worker(Session{Name: "x", State: agent.Idle, Note: "asks: Merge?"})}
	ss[2].Name, ss[2].ID, ss[2].StartedBy = "x", "$x", "gone-boss"
	MarkBossRuns(ss)
	mdl := Model{snap: snapshot{width: 60, height: 24, current: "b", sessions: Order(ss)}}
	raw := strings.Join(mdl.render().lines, "\n")
	if n := strings.Count(raw, red+"●"+reset); n != 1 {
		t.Errorf("red dots = %d, want 1 (x, whose boss is gone):\n%s", n, raw)
	}
	if !strings.Contains(raw, gold+"asks its boss") || strings.Count(ansi.Strip(raw), "asks its boss") != 1 {
		t.Errorf("w doesn't read asks its boss in gold:\n%s", ansi.Strip(raw))
	}
}
