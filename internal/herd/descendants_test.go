package herd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// A manager's board tiles the sessions it started, plus those its bosses started (deeper too) while they need the
// user (a dialog, a question its turn ended on, or one its hook recorded, for the capture to tell), each tagged
// with the boss that started it; never in started. A worker's own sessions and a cycle outside aren't followed.
func TestSnapshotDescendants(t *testing.T) {
	agentRow := func(name, id, pane, by, state string) row {
		return row{Session: name, SessionID: id, ID: pane, Window: 1, Command: "claude", State: state, StateCmd: "claude", StartedBy: by}
	}
	deciding := agentRow("w-dialog", "$3", "%3", "boss-a", agent.Waiting)
	deciding.WaitingFor, deciding.Question = "permission_prompt", `{"tool":"Bash","command":"echo hi"}`
	asks := agentRow("w-asks", "$4", "%4", "boss-a", agent.Idle)
	asks.Note = inbox.Note{Kind: inbox.Asks, Text: "Merge?"}.Summary()
	bossA, bossB := agentRow("boss-a", "$2", "%2", "manager", agent.Idle), agentRow("boss-b", "$6", "%6", "boss-a", agent.Idle)
	bossA.Herd, bossB.Herd = "1", "1"
	deep := agentRow("w-deep", "$7", "%7", "boss-b", agent.Waiting)
	deep.WaitingFor = "permission_prompt"
	outside := agentRow("w-outside", "$8", "%8", "boss-x", agent.Waiting)
	outside.WaitingFor = "permission_prompt"
	asking := agentRow("w-asking", "$10", "%10", "boss-b", agent.Working)
	asking.Question = `{"tool":"AskUserQuestion"}`
	sub := agentRow("w-sub", "$11", "%11", "$3", agent.Waiting) // started by w-dialog, a worker
	sub.WaitingFor = "permission_prompt"
	cycleX, cycleY := agentRow("x", "$12", "%12", "y", agent.Waiting), agentRow("y", "$13", "%13", "x", agent.Waiting)
	cycleX.Herd, cycleY.Herd, cycleX.WaitingFor = "1", "1", "permission_prompt"
	rows := []row{{Session: "manager", SessionID: "$1", ID: "%1", Herd: "1"}, bossA, deciding, asks,
		agentRow("w-busy", "$5", "%5", "boss-a", agent.Working), bossB, deep, outside,
		agentRow("w-idle-prompt", "$9", "%9", "boss-b", agent.Idle), asking, sub, cycleX, cycleY}
	s, err := buildSnapshot(rows, nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range s.tiles {
		got = append(got, tl.sess.Name+" via "+tl.via)
	}
	if want := []string{"w-asks via boss-a", "w-deep via boss-b", "w-dialog via boss-a", "w-asking via boss-b", "boss-a via "}; !slices.Equal(got, want) {
		t.Errorf("tiles = %q, want %q", got, want)
	}
	if q := s.tiles[index(s.tiles, "w-dialog")].question; q.Tool != "Bash" || q.Command != "echo hi" {
		t.Errorf("w-dialog's question = %+v", q)
	}
	if len(s.started) != 1 || !s.started["boss-a"] {
		t.Errorf("started = %v, want only boss-a", s.started)
	}
	if !slices.Equal(s.bosses, []string{"boss-a", "boss-b"}) {
		t.Errorf("bosses = %q", s.bosses)
	}
}

// dialogScreen is a pane showing a Bash permission dialog.
var dialogScreen = strings.Repeat("─", 40) + "\n Bash command\n\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"

// viaBoard is board4 with a session boss-a started waiting on a dialog, captured.
func viaBoard() Model {
	m := board4()
	q := agent.Question{Tool: "Bash", Command: "rm -rf build"}
	d, _ := parseDialog(dialogScreen)
	p := newPick(d, q, dialogScreen)
	via := tile{sess: sidebar.Session{Name: "w-dialog", Agent: "Claude", State: agent.Waiting}, deciding: true, via: "boss-a",
		question: q, pick: &p}
	m.snap.tiles = append([]tile{via}, m.snap.tiles...)
	m.selected = "w-dialog"
	return m
}

// A deeper session's card is shown, not answered: no Message… or ×, answer, reply and kill keys say where it is
// answered, ⏎ still goes to it.
func TestViaDisplayOnly(t *testing.T) {
	m := viaBoard()
	rows, hits := m.card(m.snap.tiles[0], 70)
	head, buttons := ansi.Strip(rows[1]), ansi.Strip(rows[len(rows)-2])
	if !strings.Contains(head, "w-dialog  via boss-a") || strings.Contains(buttons, "Message…") || strings.Contains(buttons, "×") || !strings.Contains(buttons, "Open") {
		t.Errorf("head %q, buttons %q: want the via tag and Open only", head, buttons)
	}
	for _, h := range hits {
		if h.kind == hitMessage || h.kind == hitClose {
			t.Errorf("hit %+v on a deeper session's card", h)
		}
	}
	for _, k := range []string{"1", "m", "i", "x"} {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		n := next.(Model)
		if cmd != nil || n.box.session != "" || n.confirm != "" || !strings.Contains(n.msg.text, "boss-a's: answer it there") {
			t.Errorf("%s: box %q, confirm %q, msg %q, want only where to answer it", k, n.box.session, n.confirm, n.msg.text)
		}
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Error("⏎: want a switch to the session")
	}
	if hint := ansi.Strip(m.bottom()[0]); !strings.Contains(hint, "boss-a's: answered there") || strings.Contains(hint, "x kill") {
		t.Errorf("hint = %q", hint)
	}
}

// A click on a deeper session's dialog option selects it and says where it is answered, without picking it; a
// reply box open on another card closes, keeping its draft.
func TestViaClick(t *testing.T) {
	m := viaBoard()
	m.open(m.snap.tiles[1])
	m.box.ed.set("draft for fix-login")
	b := m.board()
	i := slices.IndexFunc(b.hits, func(h hit) bool { return h.tile == 0 && h.kind == hitOption })
	if i < 0 {
		t.Fatal("no option hit on the deeper session's dialog")
	}
	top, _ := m.boardSpan(m.height - len(m.bottom()))
	h := b.hits[i]
	next, cmd := m.Update(tea.MouseMsg{X: h.x0, Y: top + h.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	n := next.(Model)
	if cmd != nil || n.box.session != "" || n.answering != "" || n.selected != "w-dialog" || !strings.Contains(n.msg.text, "boss-a's") {
		t.Errorf("click: box %q, answering %q, selected %q, msg %q", n.box.session, n.answering, n.selected, n.msg.text)
	}
	if d, ok := n.drafts["fix-login"]; !ok || d.ed.String() != "draft for fix-login" {
		t.Errorf("drafts = %+v, want fix-login's kept", n.drafts)
	}
}

// A deeper session's card leaves the board once its capture shows no dialog; one still showing it keeps its picker.
func TestRefreshViaDialogGone(t *testing.T) {
	rule := strings.Repeat("─", 40)
	gone := "  ⎿  Interrupted · What should Claude do instead?\n\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts\n"
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
		r("boss-a", "$3", "1", "1", "1", "%4", "/b", "", "sleep", "", "", "", "", "", "1", "herd", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
		r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "waiting", "claude", "", "", "", "", "boss-a", "permission_prompt", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	for _, screen := range []string{gone, dialogScreen} {
		state := "waiting"
		c := tmux.NewFake(func(args []string) (string, error) {
			switch args[0] {
			case "list-panes":
				return rows, nil
			case "list-clients":
				return r("/dev/ttys1", "herd", "5"), nil
			case "list-sessions":
				return "", nil
			case "display-message":
				return r("claude", state, "claude", "1", "permission_prompt", "50", "", ""), nil
			case "if-shell":
				state = "idle"
			}
			return screen, nil
		})
		m := Model{c: c, pane: "%2", look: &fakeLookups{}, width: 80, height: 20, clk: control.RealClock}
		msg := m.refresh()().(refreshMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		i := slices.IndexFunc(msg.snap.tiles, func(t tile) bool { return t.sess.Name == "w1" })
		switch {
		case screen == gone && i >= 0:
			t.Errorf("dialog gone: w1 still on the board: %+v", msg.snap.tiles[i])
		case screen != gone && (i < 0 || msg.snap.tiles[i].via != "boss-a" || msg.snap.tiles[i].pick == nil):
			t.Errorf("dialog open: tiles %+v, want w1 via boss-a with its picker", msg.snap.tiles)
		}
	}
}

// bossAsks keeps the bosses asked for, each with its open items on no one, a list not read with its error, and
// leaves out one with none.
func TestBossAsks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var read []string
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-a", "1") + tmux.FormatRow("boss-b", "1") + tmux.FormatRow("boss-x", "1") + tmux.FormatRow("boss-bad", "1"), nil
		case "display-message":
			name := strings.Trim(args[3], "=:")
			read = append(read, name)
			return tmux.FormatRow("/tmp/tmux-1/default", name, "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	now := time.Now()
	add := func(boss string, its ...todo.Item) todo.Store {
		s, err := todo.ForPane(c, tmux.SessionTarget(boss))
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range its {
			if _, err := s.Add(it, now); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	a := add("boss-a", todo.Item{Text: "Merge?", Options: []string{"Yes", "No"}}, todo.Item{Text: "done"}, todo.Item{Text: "on w1", On: "w1"})
	if err := a.Done([]int{2}, now); err != nil {
		t.Fatal(err)
	}
	add("boss-b", todo.Item{Text: "on w2", On: "w2"})
	add("boss-x", todo.Item{Text: "not ours"})
	add("boss-bad", todo.Item{Text: "x"})
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*boss-bad.json"))
	if err := os.WriteFile(matches[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	read = nil
	got, err := bossAsks(c, []string{"boss-a", "boss-b", "boss-bad"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Boss != "boss-a" || len(got[0].Items) != 1 || got[0].Items[0].Text != "Merge?" || got[1].Boss != "boss-bad" || got[1].Err == nil {
		t.Errorf("bossAsks = %+v, want boss-a's ask and boss-bad's error", got)
	}
	if slices.Contains(read, "boss-x") {
		t.Errorf("read %q: boss-x isn't asked for", read)
	}
}

// A to do item on a name only a deeper session has isn't on it: that session is its boss's.
func TestTodoOwnerSkipsVia(t *testing.T) {
	m := viaBoard()
	if o := m.todoOwner(todo.Shown{ID: 1, Text: "x", On: "w-dialog"}); o != nil {
		t.Errorf("owner = %+v, want none", o)
	}
	if o := m.todoOwner(todo.Shown{ID: 2, Text: "y", On: "fix-login"}); o == nil || o.name != "fix-login" {
		t.Errorf("owner = %+v, want fix-login", o)
	}
}

// The bosses' items waiting on the user show under each boss's header after the own list, an ask with its
// options; a list not read says why; folded, the headers only, ▸ first.
func TestAskRows(t *testing.T) {
	m := withTodo()
	m.snap.todo = todo.List{}
	m.snap.asks = []todo.BossList{
		{Boss: "boss-a", List: todo.List{Items: []todo.Shown{
			{ID: 7, Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}},
			{ID: 9, Text: "DM David"},
		}}},
		{Boss: "boss-b", Err: errors.New("bad")},
	}
	checkRows(t, m.todoRows(39), []string{
		" boss-a · 2 wait on you ──────────────────────────",
		" 7 ☐ Merge #148?                                  ",
		"     1 ★ Merge  2 Wait                            ",
		" 9 ☐ DM David                                     ",
		" boss-b ⚠ list not read: bad ─────────────────────",
	}, 50)
	m.width = 80
	if h := ansi.Strip(m.todoRows(39)[0]); !strings.HasSuffix(h, " dev todo answer boss-a <n> <option> ") {
		t.Errorf("wide header %q: want how to answer at the right", h)
	}
	m.todoFolded = true
	if rows := plain(m.todoRows(39)); len(rows) != 2 || !strings.HasPrefix(rows[0], " ▸ boss-a") {
		t.Errorf("folded: want the headers only:\n%s", strings.Join(rows, "\n"))
	}
	// Plain items only: no answer hint
	m.todoFolded, m.snap.asks = false, m.snap.asks[:1]
	m.snap.asks[0].Items = m.snap.asks[0].Items[1:]
	if h := ansi.Strip(m.todoRows(39)[0]); strings.Contains(h, "dev todo answer") {
		t.Errorf("header %q: no ask to answer", h)
	}
}

// A full own list leaves a row for each boss's header, and bosses out of rows are counted, not dropped; asks
// alone keep the pane open with the t hint.
func TestAskRowsRoom(t *testing.T) {
	m := withTodo()
	m.snap.todo.Items = append(m.snap.todo.Items, todo.Shown{ID: 6, Text: "six", Options: []string{"a", "b"}}, todo.Shown{ID: 7, Text: "seven"})
	ask := func(boss string) todo.BossList {
		return todo.BossList{Boss: boss, List: todo.List{Items: []todo.Shown{{ID: 1, Text: boss + "'s ask"}}}}
	}
	m.snap.asks = []todo.BossList{ask("boss-a"), ask("boss-b"), ask("boss-c"), ask("boss-d"), ask("boss-e")}
	rows := plain(m.todoRows(16))
	if len(rows) != 8 || !strings.HasPrefix(rows[len(rows)-2], " boss-c ·") || strings.TrimSpace(rows[len(rows)-1]) != "+2 more bosses wait on you" {
		t.Errorf("16 rows:\n%s", strings.Join(rows, "\n"))
	}
	m.snap = snapshot{asks: m.snap.asks[:1]}
	m.size, m.focused = collapsed, false
	m.c = tmux.NewFake(func(args []string) (string, error) { return "200\n", nil })
	m.fit()
	if m.size == collapsed || !strings.Contains(ansi.Strip(m.bottom()[0]), "t fold to do") {
		t.Errorf("asks alone: size %v, hint %q", m.size, ansi.Strip(m.bottom()[0]))
	}
}
