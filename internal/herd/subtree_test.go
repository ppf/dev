package herd

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// sidebar keeps its own copy of the note kinds, as inbox imports it.
func TestNoteKindsPinned(t *testing.T) {
	if sidebar.NoteAsks != inbox.Asks || sidebar.NoteFinished != inbox.Finished || sidebar.NoteDecision != inbox.Decision {
		t.Error("sidebar's asks or decision kind differs from inbox's")
	}
	for _, k := range []string{inbox.Finished, inbox.Failed, inbox.Decision, inbox.Asks, inbox.Stuck, inbox.Context, inbox.WaitsOnYou} {
		if sidebar.HealthNote(k) != healthNotes[k] {
			t.Errorf("%q: sidebar.HealthNote %v, healthNotes %v", k, sidebar.HealthNote(k), healthNotes[k])
		}
	}
}

// managerRows is a manager over boss-dev (idle, its two workers working) and boss-noc (its turn ended, one
// worker on a permission dialog, one working), and boss-mag with no workers.
func managerRows() []row {
	r := func(name, id, by, state string) row {
		return row{Session: name, SessionID: id, ID: "%" + id[1:], Window: 1, Command: "claude", State: state, StateCmd: "claude", StartedBy: by}
	}
	boss := func(name, id string) row {
		b := r(name, id, "manager", agent.Idle)
		b.Herd, b.Note = "1", inbox.Note{Kind: inbox.Finished, Text: "VPN?"}.Summary()
		return b
	}
	dialog := r("noc-triage", "$8", "boss-noc", agent.Waiting)
	dialog.WaitingFor, dialog.Question = "permission_prompt", `{"tool":"Bash","command":"echo hi"}`
	return []row{{Session: "manager", SessionID: "$1", ID: "%1", Herd: "1"},
		boss("boss-dev", "$2"), r("dev-a", "$3", "boss-dev", agent.Working), r("dev-b", "$4", "boss-dev", agent.Working),
		boss("boss-mag", "$5"),
		boss("boss-noc", "$6"), r("noc-pr", "$7", "boss-noc", agent.Working), dialog}
}

var managerAsks = []todo.BossList{{Boss: "boss-noc", List: todo.List{Items: []todo.Shown{
	{ID: 1, Text: "Release now?", Options: []string{"Now", "Later"}},
	{ID: 2, Text: "DM David"},
}}}}

// An item on someone on a boss's list counts on its card as "on others", never as a NEEDS YOU row.
func TestBossCardOnOthers(t *testing.T) {
	s, err := buildSnapshot(managerRows(), nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	l := managerAsks[0]
	l.Items = append(slices.Clone(l.Items), todo.Shown{ID: 3, Text: "Deploy", On: "noc-pr"})
	s.asks = []todo.BossList{l}
	s.rollUp()
	m := board4()
	m.snap = s
	noc := s.tiles[index(s.tiles, "boss-noc")]
	if got := ansi.Strip(rollText(*noc.roll)); !strings.HasSuffix(got, " · 1 to do · 1 on others") {
		t.Errorf("boss-noc status = %q", got)
	}
	for _, r := range m.needRows() {
		if r.Text == "Deploy" {
			t.Errorf("NEEDS YOU row %+v: it's on noc-pr", r)
		}
	}
}

// A boss's card sits by its whole subtree, folded, its row the roll-up; its own note moves to the open card.
func TestBossCardsRollUp(t *testing.T) {
	s, err := buildSnapshot(managerRows(), nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.asks = managerAsks
	s.rollUp()
	m := board4()
	m.snap = s
	got := map[string]string{}
	var order []string
	for _, tl := range s.tiles {
		order = append(order, tl.sess.Name)
		got[tl.sess.Name] = strings.Join(strings.Fields(ansi.Strip(m.headRow(tl, tl.section(), true, 120))), " ")
		if tl.via == "" && !tl.folded(nil) {
			t.Errorf("%s: open, want folded", tl.sess.Name)
		}
	}
	// The dialog's worker shows as a via card
	if want := []string{"boss-noc", "noc-triage", "boss-dev", "boss-mag"}; !slices.Equal(order, want) {
		t.Errorf("tiles = %q, want %q", order, want)
	}
	for name, want := range map[string]string{
		"boss-noc": "▸ ● boss-noc needs you · 2 questions · 1 working · 1 to do",
		"boss-dev": "▸ ● boss-dev 2 working",
		"boss-mag": "▸ ● boss-mag idle · no workers",
	} {
		if !strings.HasPrefix(got[name], want) {
			t.Errorf("%s row = %q, want %q", name, got[name], want)
		}
	}
	noc := s.tiles[index(s.tiles, "boss-noc")]
	if noc.asks() || noc.options() != nil {
		t.Error("boss-noc's subtree question reads as its own ask: 1-9 would answer it")
	}
	m.folds = map[string]bool{"boss-noc": false}
	rows, _ := m.card(noc, 80)
	body := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(body, "needs you · 2 questions") || !strings.Contains(body, "finished: VPN?") {
		t.Errorf("open card:\n%s", body)
	}
	// The sidebar's roll-up reads the same sessions the same way
	side := sidebar.Subtree(sidebar.Order(sidebarSessions(managerRows())), map[string]sidebar.ListCount{"boss-noc": {Asks: 1, Plain: 1}})
	for _, name := range []string{"boss-noc", "boss-dev", "boss-mag"} {
		if b := s.tiles[index(s.tiles, name)].roll; *b != *side[name] {
			t.Errorf("%s: board %+v, sidebar %+v", name, *b, *side[name])
		}
	}
}

// sidebarSessions are rows as the sidebar's buildSnapshot reads them.
func sidebarSessions(rows []row) []sidebar.Session {
	var out []sidebar.Session
	for _, r := range rows {
		s := sidebar.Session{Name: r.Session, Group: sidebar.Group(r.Session, r.Project), ID: r.SessionID, Boss: r.Herd != "", StartedBy: r.StartedBy}
		if r.Command == "claude" {
			s.Agent, s.State, s.Note = "Claude", r.State, r.Note
			s.Deciding = agent.Deciding(r.State, r.WaitingFor)
		}
		out = append(out, s)
	}
	return out
}

// A boss's own dialog puts its card under Needs you, folded: NEEDS YOU asks it.
func TestBossCardOwnDialog(t *testing.T) {
	rows := managerRows()
	rows[1].State, rows[1].WaitingFor = agent.Waiting, "permission_prompt"
	s, err := buildSnapshot(rows, nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.rollUp()
	dev := s.tiles[index(s.tiles, "boss-dev")]
	if dev.section() != needsYouSection || !dev.folded(nil) || dev.roll.Text() != "needs you · 1 question · 2 working" {
		t.Errorf("boss-dev: section %d folded %v roll %q", dev.section(), dev.folded(nil), dev.roll.Text())
	}
}

// A plain boss's board keeps its workers' cards as they were: none rolls up.
func TestPlainBossBoardUnchanged(t *testing.T) {
	rows := managerRows()[5:]
	rows[0].StartedBy = ""
	s, err := buildSnapshot(rows, nil, "%6", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]section{}
	for _, tl := range s.tiles {
		before[tl.sess.Name] = tl.section()
	}
	s.rollUp()
	after := map[string]section{}
	for _, tl := range s.tiles {
		if tl.roll != nil {
			t.Errorf("%s rolls up", tl.sess.Name)
		}
		after[tl.sess.Name] = tl.section()
	}
	if !maps.Equal(before, after) || after["noc-triage"] != needsYouSection || after["noc-pr"] != workingSection {
		t.Errorf("sections %v, then %v", before, after)
	}
}

// A dialog the capture found answered (No or Esc fire no hook) leaves its boss's roll-up too.
func TestRollUpCaptureWins(t *testing.T) {
	s, err := buildSnapshot(managerRows(), nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	i := index(s.tiles, "noc-triage")
	s.tiles[i].deciding, s.tiles[i].sess.State = false, agent.Idle
	s.rollUp()
	if noc := s.tiles[index(s.tiles, "boss-noc")]; noc.section() != workingSection || noc.roll.Text() != "1 working" {
		t.Errorf("boss-noc: section %d, %q", noc.section(), noc.roll.Text())
	}
}

// A rate-limited worker waits; a stuck one flags its boss's card ⚠, folded and open.
func TestBossCardWaitingAndWarn(t *testing.T) {
	rows := managerRows()
	rows[2].State, rows[2].ResumeAt = agent.Idle, "1700000000"
	rows[3].Note = inbox.Note{Kind: inbox.Stuck, Text: "20m"}.Summary()
	s, err := buildSnapshot(rows, nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.rollUp()
	m := board4()
	m.snap = s
	dev := s.tiles[index(s.tiles, "boss-dev")]
	if row := ansi.Strip(m.headRow(dev, dev.section(), true, 120)); !strings.Contains(row, "boss-dev  1 working · 1 waiting ⚠") {
		t.Errorf("folded row = %q", row)
	}
	m.folds = map[string]bool{"boss-dev": false}
	rows2, _ := m.card(dev, 100)
	if body := ansi.Strip(strings.Join(rows2, "\n")); !strings.Contains(body, "⚠ in its subtree") {
		t.Errorf("open card:\n%s", body)
	}
}

// A boss's own turn-ending question folds its card (NEEDS YOU asks it); opened, it shows as an asks card does, the
// roll-up under it.
func TestBossCardOwnAsks(t *testing.T) {
	rows := managerRows()
	rows[1].Note = inbox.Note{Kind: inbox.Asks, Text: "Connect the VPN?"}.Summary()
	s, err := buildSnapshot(rows, nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.rollUp()
	m := board4()
	m.snap, m.folds = s, map[string]bool{"boss-dev": false}
	dev := s.tiles[index(s.tiles, "boss-dev")]
	if !dev.asks() || !dev.folded(nil) {
		t.Fatalf("boss-dev: asks %v folded %v", dev.asks(), dev.folded(nil))
	}
	cardRows, _ := m.card(dev, 100)
	body := ansi.Strip(strings.Join(cardRows, "\n"))
	if !strings.Contains(body, "Connect the VPN?") || !strings.Contains(body, "needs you · 1 question · 2 working") {
		t.Errorf("open card:\n%s", body)
	}
}

// Lists that couldn't be read flag every boss ⚠.
func TestRollUpListsNotRead(t *testing.T) {
	s, err := buildSnapshot(managerRows(), nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.asksErr = errors.New("no server")
	s.rollUp()
	for _, name := range []string{"boss-dev", "boss-mag", "boss-noc"} {
		if !s.tiles[index(s.tiles, name)].roll.Warn {
			t.Errorf("%s: no ⚠", name)
		}
	}
}

// A boss card's status keeps its section's colour for the state; "N to do" and "N on others" trail it muted.
func TestRollTextMutedTail(t *testing.T) {
	got := rollText(sidebar.Rollup{Rank: sidebar.RankWorking, Working: 2, ToDo: 1, OnOthers: 3})
	if want := sidebar.Gold + "2 working" + reset + sidebar.Muted + " · 1 to do · 3 on others" + reset; got != want {
		t.Errorf("rollText = %q, want %q", got, want)
	}
	got = rollText(sidebar.Rollup{Rank: sidebar.RankIdle, Under: 1, OnOthers: 2})
	if want := sidebar.Muted + "idle" + reset + sidebar.Muted + " · 2 on others" + reset; got != want {
		t.Errorf("idle: %q, want %q", got, want)
	}
}

// A folded boss card too narrow for its whole row drops "N on others" before its PR badge.
func TestBossCardOnOthersNarrow(t *testing.T) {
	m := board4()
	tl := tile{sess: sidebar.Session{Name: "boss-orbit", Agent: "Claude", State: agent.Idle, PR: sidebar.PR{Number: 87, State: "open"}},
		roll: &sidebar.Rollup{Rank: sidebar.RankWorking, Working: 2, OnOthers: 3}}
	for w, want := range map[int]string{80: "▸ ● boss-orbit 2 working · 3 on others ○ #87", 40: "▸ ● boss-orbit 2 working ○ #87"} {
		if got := strings.Join(strings.Fields(ansi.Strip(m.headRow(tl, tl.section(), true, w))), " "); got != want {
			t.Errorf("%d columns: %q, want %q", w, got, want)
		}
	}
}

// Only a boss list with an item on no one, or one not read, is something NEEDS YOU shows.
func TestWaitsOnUser(t *testing.T) {
	on := todo.BossList{Boss: "b", List: todo.List{Items: []todo.Shown{{ID: 1, Text: "x", On: "w"}}}}
	mine := todo.BossList{Boss: "b", List: todo.List{Items: []todo.Shown{{ID: 1, Text: "x"}}}}
	bad := todo.BossList{Boss: "b", Err: errors.New("bad")}
	if waitsOnUser(nil) || waitsOnUser([]todo.BossList{on}) || !waitsOnUser([]todo.BossList{on, mine}) || !waitsOnUser([]todo.BossList{bad}) {
		t.Error("waitsOnUser: want true only for an item on no one or a list not read")
	}
}

// A folded boss card too narrow for its counts drops them whole, "N on others" first, never cutting one mid-word.
func TestBossCardCountsDropWhole(t *testing.T) {
	m := board4()
	tl := tile{sess: sidebar.Session{Name: "boss-orbit-long-name", Agent: "Claude", State: agent.Idle},
		roll: &sidebar.Rollup{Rank: sidebar.RankIdle, Under: 1, ToDo: 1, OnOthers: 1}}
	for w, want := range map[int]string{
		80: "▸ ● boss-orbit-long-name idle · 1 to do · 1 on others",
		51: "▸ ● boss-orbit-long-name idle · 1 to do",
		40: "▸ ● boss-orbit-long-name idle",
	} {
		if got := strings.Join(strings.Fields(ansi.Strip(m.headRow(tl, tl.section(), true, w))), " "); got != want {
			t.Errorf("%d columns: %q, want %q", w, got, want)
		}
	}
}
