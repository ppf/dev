package herd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

const vpnAsk = "Reply **Done** once it's connected, or **Skip** to leave the lw check for later."

type listAnswer struct {
	boss     string
	id       int
	question string
	option   string
}

// needsSnap is the manager's board over managerRows: noc-triage on a Bash dialog (screen), boss-dev's turn
// ended on a question with options, boss-noc's list ask and plain item, and the manager's own plain item.
func needsSnap(t *testing.T, screen string) snapshot {
	t.Helper()
	rows := managerRows()
	for i, r := range rows {
		switch {
		case strings.HasPrefix(r.Session, "noc") || r.Session == "boss-noc":
			rows[i].Project = "noc"
		case strings.HasPrefix(r.Session, "dev") || r.Session == "boss-dev":
			rows[i].Project = "dev"
		}
	}
	rows[1].Note = inbox.Note{Kind: inbox.Asks, Text: vpnAsk}.Summary()
	s, err := buildSnapshot(rows, nil, "%1", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.asks = managerAsks
	s.todo = todo.List{Items: []todo.Shown{{ID: 4, Text: "Call Vince"}}}
	i := index(s.tiles, "noc-triage")
	q := agent.Question{Tool: "Bash", Command: "rm -rf build"}
	d, ok := parseDialog(screen)
	if !ok {
		t.Fatal("no dialog on screen")
	}
	p := newPick(d, q, screen)
	s.tiles[i].pick, s.tiles[i].question = &p, q
	dev := index(s.tiles, "boss-dev")
	s.tiles[dev].tail = desktop.Tail{Lines: []string{"Merged.", vpnAsk}}
	s.rollUp()
	return s
}

// needsBoard is the manager's board on needsSnap, its list answers recorded in answered.
func needsBoard(t *testing.T, answered *[]listAnswer) Model {
	m := Model{width: 80, height: 40, clk: fakeClock(), snap: needsSnap(t, dialogScreen), focused: true, size: shared}
	m.answerList = func(boss string, id int, question, option string) (string, error) {
		*answered = append(*answered, listAnswer{boss, id, question, option})
		return "#1 answered, sent to " + boss, nil
	}
	m.selected = m.validSelection()
	return m
}

func keys(m Model, ks ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range ks {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	return m, cmd
}

// The manager's block lists every descendant's dialog, then turn-ending questions, then list asks, then plain
// items (its own list's first, as "to do"), each labelled "project › who", with the options its keys send.
func TestNeedsRows(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	type want struct {
		kind    sidebar.NeedKind
		label   string
		text    string
		options []string
	}
	var got []want
	for _, r := range m.needRows() {
		got = append(got, want{r.Kind, r.label, r.Text, r.Options})
	}
	if w := []want{
		{sidebar.NeedDialog, "noc › triage", "allow Bash: rm -rf build", []string{"Yes", "No"}},
		{sidebar.NeedAsk, "dev › boss", vpnAskPlain, []string{"Done", "Skip"}},
		{sidebar.NeedListAsk, "noc › boss", "Release now?", []string{"Now", "Later"}},
		{sidebar.NeedPlain, "to do", "Call Vince", nil},
		{sidebar.NeedPlain, "noc › boss", "DM David", nil},
	}; !slices.EqualFunc(got, w, func(a, b want) bool {
		return a.kind == b.kind && a.label == b.label && a.text == b.text && slices.Equal(a.options, b.options)
	}) {
		t.Errorf("rows:\n%#v\nwant:\n%#v", got, w)
	}
	// The sidebar counts the same rows from what it reads
	all := sidebar.Order(m.snap.all)
	if n := len(sidebar.NeedRows(all, m.snap.needLists(), "manager")); n != 5 {
		t.Errorf("sidebar count %d, want 5", n)
	}
}

const vpnAskPlain = "Reply Done once it's connected, or Skip to leave the lw check for later."

// A boss's own board scopes the block to its workers and list, labelled by who, its own question left out.
func TestNeedsBossBoard(t *testing.T) {
	rows := managerRows()
	rows[5].Note = inbox.Note{Kind: inbox.Asks, Text: "Connect the VPN?"}.Summary()
	s, err := buildSnapshot(rows, nil, "%6", "", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	s.todo = todo.List{Items: []todo.Shown{{ID: 1, Text: "Release now?", Options: []string{"Now", "Later"}}}}
	s.rollUp()
	m := Model{width: 80, height: 40, snap: s}
	var got []string
	for _, r := range m.needRows() {
		got = append(got, r.label+": "+r.Text)
	}
	if want := []string{"noc-triage: Bash: echo hi", "to do: Release now?"}; !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
}

// The block sits above the cards: header, a row per item with its options under it; its cards fold to a row
// pointing at it; n focuses it, wrapping the selected row's text.
func TestNeedsRender(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	lines := plain(m.render())
	for i, want := range []string{
		" NEEDS YOU 5 · n",
		"",
		" ● noc › triage  allow Bash: rm -rf build",
		"     1 Yes   2 No",
		" ? dev › boss    Reply Done once it's connected, or Skip to leave the lw check…",
	} {
		if got := strings.TrimRight(lines[i], " "); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"     1 Done   2 Skip   m reply", " ? noc › boss    Release now?", "     1 Now   2 Later", " ☐ to do         Call Vince",
		"noc-triage  via boss-noc  Bash prompt · in the block ↑", "n needs"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	for i, l := range m.render() {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	m, _ = keys(m, "n", "j")
	lines = plain(m.render())
	if !strings.HasPrefix(lines[0], " NEEDS YOU 5 ") || strings.Contains(lines[0], "· n") || !strings.HasSuffix(strings.TrimSpace(lines[5]), "for later.") {
		t.Errorf("focused, ask selected:\n%s", strings.Join(lines[:8], "\n"))
	}
	if hint := lines[len(lines)-1]; !strings.Contains(hint, "1-9 answer  m reply  g/⏎ go  esc cards") {
		t.Errorf("hint = %q", hint)
	}
}

// 1-9 on a list ask answers it through dev todo answer, once at a time; on a plain item nothing.
func TestNeedsAnswerList(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	m, _ = keys(m, "n", "j", "j", "j")
	if m, cmd := keys(m, "1"); cmd != nil || len(answered) != 0 || !strings.Contains(m.msg.text, "Nothing to answer") {
		t.Errorf("plain item: cmd %v, msg %q", cmd != nil, m.msg.text)
	}
	m, _ = keys(m, "k")
	m, cmd := keys(m, "2")
	if cmd == nil || m.answering != "boss-noc" {
		t.Fatalf("2 on the list ask: answering %q", m.answering)
	}
	if again, cmd := keys(m, "1"); cmd != nil || !strings.Contains(again.msg.text, "answered: press again") {
		t.Errorf("a second answer while one is out: %q", again.msg.text)
	}
	next, _ := m.Update(cmd())
	if m = next.(Model); m.answering != "" || !strings.Contains(m.msg.text, "answered") || !m.needMode {
		t.Errorf("after: answering %q msg %q mode %v", m.answering, m.msg.text, m.needMode)
	}
	if want := []listAnswer{{"boss-noc", 1, "Release now?", "Later"}}; !slices.Equal(answered, want) {
		t.Errorf("answered %+v, want %+v", answered, want)
	}
	// While the answered row is still shown, keys wait; once it goes, the cursor takes the row now under it, and
	// its keys act at once
	next, _ = m.Update(refreshMsg{snap: needsSnap(t, dialogScreen), inputs: m.inputs})
	if m, cmd := keys(next.(Model), "1"); cmd != nil || !strings.Contains(m.msg.text, "answered: press again") {
		t.Errorf("answered row still shown: msg %q", m.msg.text)
	}
	s := needsSnap(t, dialogScreen)
	s.asks = []todo.BossList{{Boss: "boss-noc", List: todo.List{Items: []todo.Shown{{ID: 2, Text: "DM David"}}}}}
	next, _ = m.Update(refreshMsg{snap: s, inputs: m.inputs})
	m = next.(Model)
	if m, _ := keys(m, "1"); !strings.Contains(m.msg.text, "Nothing to answer") {
		t.Errorf("next row after an answer: msg %q", m.msg.text)
	}
}

// 1-9 on a turn-ending question sends the option as a typed reply; m opens the reply box on it.
func TestNeedsAnswerAsk(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	var ran []string
	m.c = tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "", nil
	})
	m, _ = keys(m, "n", "j")
	if _, cmd := keys(m, "2"); cmd == nil {
		t.Error("2 on the question: no reply sent")
	}
	if m, _ := keys(m, "m"); m.box.session != "boss-dev" || m.activePick() != nil {
		t.Errorf("m: box %q", m.box.session)
	}
}

// dialogClient is a pane whose screens come in turn (the last repeating), recording the keys sent.
func dialogClient(screens ...string) (*tmux.Client, *[]string) {
	var sent []string
	n := 0
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "capture-pane":
			s := screens[min(n, len(screens)-1)]
			n++
			return s, nil
		case "send-keys":
			sent = append(sent, strings.Join(args[1:], " "))
		}
		return "", nil
	})
	return c, &sent
}

// The bash dialog asking for another command.
var otherDialog = strings.Replace(dialogScreen, "rm -rf build", "rm -rf /", 1)

// 1-9 on a dialog answers it through its picker: keys only while the pane still shows the dialog the row was
// built from; one that changed or closed since gets nothing, and the block takes its keys again.
func TestNeedsAnswerDialog(t *testing.T) {
	closed := "❯ \n"
	for _, tc := range []struct {
		name    string
		screens []string
		sent    bool
		msg     string
	}{
		{"same", []string{dialogScreen, closed}, true, "Answered noc-triage"},
		{"changed", []string{otherDialog}, false, "dialog changed: nothing sent"},
		{"closed", []string{closed}, false, "dialog changed: nothing sent"},
	} {
		var answered []listAnswer
		m := needsBoard(t, &answered)
		c, sent := dialogClient(tc.screens...)
		m.c = c
		m, cmd := keys(m, "n", "1")
		if cmd == nil || m.answering != "noc-triage" || !m.box.block {
			t.Fatalf("%s: answering %q block %v", tc.name, m.answering, m.box.block)
		}
		next, _ := m.Update(cmd())
		m = next.(Model)
		if got := len(*sent) > 0; got != tc.sent || tc.sent && !strings.HasSuffix((*sent)[0], "%8 -l 1") {
			t.Errorf("%s: sent %q", tc.name, *sent)
		}
		if !strings.Contains(m.msg.text, tc.msg) || m.box.session != "" || !m.needMode {
			t.Errorf("%s: msg %q box %q mode %v", tc.name, m.msg.text, m.box.session, m.needMode)
		}
	}
}

// A dialog that changed between the frame and the key isn't answered: the key says so and the next one acts on
// the row now shown; a row that moved keeps the cursor; one that went leaves it, refusing the next answer.
func TestNeedsRowChanged(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	c, sent := dialogClient(otherDialog, "❯ \n")
	m.c = c
	m, _ = keys(m, "n")
	next, _ := m.Update(refreshMsg{snap: needsSnap(t, otherDialog), inputs: m.inputs})
	m = next.(Model)
	m, cmd := keys(m, "1")
	if cmd != nil || m.box.session != "" || !strings.Contains(m.msg.text, "changed: nothing sent") {
		t.Fatalf("changed dialog: cmd %v box %q msg %q", cmd != nil, m.box.session, m.msg.text)
	}
	if m, cmd = keys(m, "1"); cmd == nil || m.answering != "noc-triage" {
		t.Fatalf("again: answering %q", m.answering)
	}
	if cmd(); len(*sent) != 1 {
		t.Errorf("sent %q, want one key to the dialog shown", *sent)
	}

	// The dialog goes: the list ask under the cursor moves up and keeps it
	m = needsBoard(t, &answered)
	m, _ = keys(m, "n", "j", "j")
	s := needsSnap(t, dialogScreen)
	s.tiles = slices.DeleteFunc(s.tiles, func(t tile) bool { return t.sess.Name == "noc-triage" })
	s.all = slices.DeleteFunc(s.all, func(a sidebar.Session) bool { return a.Name == "noc-triage" })
	next, _ = m.Update(refreshMsg{snap: s, inputs: m.inputs})
	if m = next.(Model); m.needIdx != 1 || m.needRows()[1].Kind != sidebar.NeedListAsk {
		t.Fatalf("idx %d", m.needIdx)
	}
	if _, cmd := keys(m, "1"); cmd == nil {
		t.Error("the list ask moved: 1 must still answer it")
	}

	// The selected list ask goes: the cursor stays, the next answer key is refused
	m = needsBoard(t, &answered)
	m, _ = keys(m, "n", "j", "j")
	s = needsSnap(t, dialogScreen)
	s.asks = []todo.BossList{{Boss: "boss-noc", List: todo.List{Items: []todo.Shown{{ID: 2, Text: "DM David"}}}}}
	next, _ = m.Update(refreshMsg{snap: s, inputs: m.inputs})
	m = next.(Model)
	if m, cmd := keys(m, "1"); cmd != nil || !strings.Contains(m.msg.text, "changed") {
		t.Errorf("gone row: cmd %v msg %q", cmd != nil, m.msg.text)
	}
}

// esc and n leave the block; g/⏎ go to the row's session; the sidebar's request focuses it, clearing it.
func TestNeedsFocusAndLeave(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	var ran []string
	m.c = tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "", nil
	})
	for _, k := range []string{"esc", "n"} {
		if m, _ := keys(m, "n", k); m.needMode {
			t.Errorf("%s: still in the block", k)
		}
	}
	m, cmd := keys(m, "n", "enter")
	if cmd == nil {
		t.Fatal("⏎: no switch")
	}
	cmd()
	if len(ran) != 1 || !strings.Contains(ran[0], "switch-client") || !strings.HasSuffix(ran[0], tmux.SessionTarget("noc-triage")) {
		t.Errorf("ran %q", ran)
	}
	m, _ = keys(m, "esc")
	s := needsSnap(t, dialogScreen)
	s.needsFocus = true
	next, _ := m.Update(refreshMsg{snap: s, inputs: m.inputs})
	if m = next.(Model); !m.needMode || m.needIdx != 0 {
		t.Errorf("focus request: mode %v idx %d", m.needMode, m.needIdx)
	}
}

// A multi-select answered from the block keeps its picker for the next toggle and Submit, closing once the
// dialog is gone.
func TestNeedsMultiSelect(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	i := index(m.snap.tiles, "noc-triage")
	p := goldenPick(t, "claude-ask-q2-checked", agent.Question{})
	m.snap.tiles[i].pick = &p
	m.c, _ = screens(t, "claude-ask-q2-checked", "claude-ask-q2-multi")
	m, cmd := keys(m, "n", "2")
	if cmd == nil || !m.box.block {
		t.Fatalf("2: no toggle sent, block %v", m.box.block)
	}
	next, _ := m.Update(answeredMsg{session: "noc-triage", msg: info("Answered noc-triage"), took: true})
	if m = next.(Model); m.box.session != "noc-triage" {
		t.Fatalf("after a toggle: box %q, want its picker kept", m.box.session)
	}
	s := needsSnap(t, dialogScreen)
	s.tiles[index(s.tiles, "noc-triage")].pick = nil
	next, _ = m.Update(refreshMsg{snap: s, inputs: m.inputs})
	if m = next.(Model); m.box.session != "" || !m.needMode {
		t.Errorf("dialog gone: box %q mode %v", m.box.session, m.needMode)
	}
}

// A click on a row of the block selects it there.
func TestNeedsClick(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	lines := plain(m.render())
	y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Release now?") })
	next, _ := m.Update(tea.MouseMsg{X: 5, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = next.(Model); !m.needMode || m.needRows()[m.needIdx].Kind != sidebar.NeedListAsk {
		t.Errorf("click: mode %v idx %d", m.needMode, m.needIdx)
	}
}

// The refresh reads the sidebar's request off its own pane and clears it.
func TestRefreshNeedsFocus(t *testing.T) {
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "1")
	var ran []string
	c := tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		switch args[0] {
		case "list-panes":
			return rows, nil
		case "list-clients":
			return r("/dev/ttys1", "herd", "5"), nil
		}
		return "", nil
	})
	m := Model{c: c, pane: "%2", look: &fakeLookups{}, width: 80, height: 20, clk: fakeClock()}
	msg := m.refresh()().(refreshMsg)
	if msg.err != nil || !msg.snap.needsFocus {
		t.Fatalf("err %v focus %v", msg.err, msg.snap.needsFocus)
	}
	if !slices.Contains(ran, "set-option -pu -t %2 "+sidebar.OptNeedsFocus) {
		t.Errorf("ran %q: want the request cleared", ran)
	}
}

// selectKind focuses the block on its first row of kind k.
func selectKind(t *testing.T, m Model, k sidebar.NeedKind) Model {
	t.Helper()
	m, _ = keys(m, "n")
	for range m.needRows() {
		if m.needRows()[m.needIdx].Kind == k {
			return m
		}
		m, _ = keys(m, "j")
	}
	t.Fatalf("no row of kind %d", k)
	return m
}

// 1-9 on a question sends that option's text as the reply, to its session.
func TestNeedsAskSendsOption(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	m = selectKind(t, m, sidebar.NeedAsk)
	m, cmd := keys(m, "2")
	if cmd == nil || len(m.history) == 0 || m.history[len(m.history)-1] != "Skip" {
		t.Errorf("2: history %q, want Skip sent", m.history)
	}
}

// Rows that changed under the cursor refuse the key: a question asked anew on the same session, a list item
// renumbered with the same id.
func TestNeedsRowChangedInPlace(t *testing.T) {
	var answered []listAnswer
	m := selectKind(t, needsBoard(t, &answered), sidebar.NeedAsk)
	s := needsSnap(t, dialogScreen)
	s.tiles[index(s.tiles, "boss-dev")].tail = desktop.Tail{Lines: []string{"Merged.", "Reply **Ship** to release, or **Hold** to wait."}}
	next, _ := m.Update(refreshMsg{snap: s, inputs: m.inputs})
	if m, cmd := keys(next.(Model), "1"); cmd != nil || !strings.Contains(m.msg.text, "changed: nothing sent") {
		t.Errorf("question asked anew: cmd %v msg %q", cmd != nil, m.msg.text)
	}

	m = selectKind(t, needsBoard(t, &answered), sidebar.NeedListAsk)
	s = needsSnap(t, dialogScreen)
	s.asks = []todo.BossList{{Boss: "boss-noc", List: todo.List{Items: []todo.Shown{{ID: 1, Text: "Rollback now?", Options: []string{"Now", "Later"}}}}}}
	next, _ = m.Update(refreshMsg{snap: s, inputs: m.inputs})
	if m, cmd := keys(next.(Model), "1"); cmd != nil || len(answered) != 0 || !strings.Contains(m.msg.text, "changed: nothing sent") {
		t.Errorf("same id, new text: cmd %v msg %q", cmd != nil, m.msg.text)
	}
}

// A dialog row with nothing on the board to answer it through (no tile, or no dialog on screen) numbers no options
// and sends nothing; nor does another tile answer for it.
func TestNeedsDialogNotAnswerable(t *testing.T) {
	var answered []listAnswer
	for _, name := range []string{"no pick", "no tile"} {
		m := needsBoard(t, &answered)
		i := index(m.snap.tiles, "noc-triage")
		switch name {
		case "no pick":
			m.snap.tiles[i].pick = nil
		case "no tile":
			m.snap.tiles = slices.Delete(m.snap.tiles, i, i+1)
		}
		var ran []string
		m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
		m = selectKind(t, m, sidebar.NeedDialog)
		for _, k := range []string{"1", "m"} {
			if got, cmd := keys(m, k); cmd != nil || got.box.session != "" || len(ran) != 0 {
				t.Errorf("%s, %s: cmd %v box %q ran %q", name, k, cmd != nil, got.box.session, ran)
			}
		}
		if strings.Contains(strings.Join(plain(m.render()), "\n"), "1 Yes") {
			t.Errorf("%s: options numbered:\n%s", name, strings.Join(plain(m.render()), "\n"))
		}
	}
}

// A list answer that fails leaves its row taking keys again at once.
func TestNeedsAnswerFails(t *testing.T) {
	var answered []listAnswer
	m := selectKind(t, needsBoard(t, &answered), sidebar.NeedListAsk)
	m.answerList = func(string, int, string, string) (string, error) { return "", errors.New("timed out") }
	m, cmd := keys(m, "1")
	next, _ := m.Update(cmd())
	if m = next.(Model); !strings.Contains(m.msg.text, "timed out") {
		t.Fatalf("msg %q", m.msg.text)
	}
	if _, cmd := keys(m, "1"); cmd == nil {
		t.Error("the row didn't take keys again")
	}
}

// Answering a dialog from the block keeps a reply typed to its worker; a click on a card leaves the block.
func TestNeedsKeepsDraftAndClickLeaves(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	draft := replyBox{session: "noc-triage", pane: "%8", hist: -1}
	draft.ed.set("half a reply")
	m.drafts = map[string]replyBox{"noc-triage": draft}
	m.c, _ = dialogClient(dialogScreen, "❯ \n")
	m, cmd := keys(m, "n", "1")
	next, _ := m.Update(cmd())
	if m = next.(Model); m.drafts["noc-triage"].ed.String() != "half a reply" || m.box.session != "" {
		t.Errorf("drafts %+v box %q", m.drafts, m.box.session)
	}
	m, _ = keys(m, "n")
	lines := plain(m.render())
	y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "boss-dev  needs you") })
	next, _ = m.Update(tea.MouseMsg{X: 20, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = next.(Model); m.needMode || m.selected != "boss-dev" {
		t.Errorf("click on a card: mode %v selected %q", m.needMode, m.selected)
	}
}

// A selected row wrapping past the pane is cut to it: the block never outgrows the rows it has.
func TestNeedsBlockFits(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	m.snap.todo.Items[0].Text = strings.Repeat("a very long item ", 60)
	m = selectKind(t, m, sidebar.NeedPlain)
	for _, h := range []int{8, 10, 14} {
		m.height = h
		if got := m.render(); len(got) != h {
			t.Errorf("height %d: %d lines", h, len(got))
		}
		if lines, _ := m.needBlock(h - 1); len(lines) > h-1 {
			t.Errorf("height %d: block of %d lines", h, len(lines))
		}
	}
}

// A pane too short for the block opens the cards it would ask for; a failed reply or another session's failed
// answer leave the cursor as they should.
func TestNeedsShortPaneAndFailures(t *testing.T) {
	var answered []listAnswer
	m := needsBoard(t, &answered)
	m.height = 6
	if rows, _ := m.card(m.snap.tiles[index(m.snap.tiles, "noc-triage")], 80); len(rows) == 1 {
		t.Error("short pane: the dialog's card folded with no block to show it")
	}
	m = selectKind(t, needsBoard(t, &answered), sidebar.NeedAsk)
	sig := m.needRows()[m.needIdx].sig
	m, _ = keys(m, "1")
	next, _ := m.Update(actionMsg{alert("❌ boss-dev: its agent is gone, reply not sent")})
	if m = next.(Model); m.needKey != sig || m.needDone != "" {
		t.Errorf("failed reply: key %q done %q", m.needKey, m.needDone)
	}
	m = selectKind(t, needsBoard(t, &answered), sidebar.NeedListAsk)
	m, _ = keys(m, "1")
	next, _ = m.Update(answeredMsg{session: "other", msg: alert("❌ other: boom")})
	if m = next.(Model); m.needDone == "" {
		t.Error("another session's failure dropped the answered row")
	}
}
