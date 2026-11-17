package sidebar

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/tmux"
)

type answerCall struct {
	boss     string
	id       int
	question string
	option   string
}

// needsModel has boss-dev (project dev) with two asks, a ticked one and one on a worker, a worker waiting on a dialog, and
// a recording answer func.
func needsModel(calls *[]answerCall) Model {
	return Model{
		c: tmux.NewFake(func([]string) (string, error) { return "", nil }),
		snap: snapshot{width: 40, height: 30, current: "boss-dev", focused: true, sessions: []Session{
			{Name: "boss-dev", Group: "dev", Boss: true, ID: "$1", Agent: "Claude", State: "idle"},
			{Name: "w1", Group: "dev", ID: "$2", StartedBy: "boss-dev", Depth: 1, Agent: "Claude", State: "waiting", Deciding: true},
		}},
		needs: []needList{{Boss: "boss-dev", Items: []needItem{
			{ID: 1, Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}},
			{ID: 2, Text: "Ticked", Done: true},
			{ID: 3, Text: "Staging", On: "Marek"},
			{ID: 4, Text: "Call Vince"},
		}}},
		needsAnswer: func(boss string, id int, question, option string) (string, error) {
			*calls = append(*calls, answerCall{boss, id, question, option})
			return "", nil
		},
	}
}

func key(m Model, s string) (Model, tea.Cmd) {
	var k tea.KeyMsg
	switch s {
	case "enter":
		k = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		k = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	next, cmd := m.handleKey(k)
	return next.(Model), cmd
}

func TestNeedsRows(t *testing.T) {
	var calls []answerCall
	got := needsModel(&calls).needRows()
	want := []needRow{
		{project: "dev", boss: "boss-dev", id: 1, text: "Merge #148?", options: []string{"Merge (Recommended)", "Wait"}, session: "boss-dev"},
		{project: "dev", boss: "boss-dev", id: 4, text: "Call Vince", session: "boss-dev"},
		{project: "dev", text: "w1", session: "w1", dialog: true},
	}
	if !slices.EqualFunc(got, want, func(a, b needRow) bool {
		return a.key() == b.key() && a.project == b.project && slices.Equal(a.options, b.options) && a.session == b.session
	}) {
		t.Errorf("rows = %+v\nwant %+v", got, want)
	}
	// A boss no session carries is shown by its own name
	m := needsModel(&calls)
	m.snap.sessions = nil
	if r := m.needRows(); r[0].project != "boss-dev" {
		t.Errorf("project without a session = %q", r[0].project)
	}
}

func TestNeedsRender(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	f := m.render()
	plain := func(i int) string { return strings.TrimRight(ansi.Strip(f.lines[i]), " ") }
	for i, want := range []string{" NEEDS YOU 3 · n", "", " dev  ☐ Merge #148?", " dev  ☐ Call Vince", " dev  ● w1", "", " SESSIONS                           +"} {
		if got := plain(i); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	if f.header != 6 || len(f.needs) != 3 || f.needs[1] != (needHit{line: 3, idx: 1}) {
		t.Errorf("header %d, hits %v", f.header, f.needs)
	}
	if !strings.Contains(f.lines[0], red+bold+"NEEDS YOU") || !strings.Contains(f.lines[4], red+"●") {
		t.Errorf("colours: %q %q", f.lines[0], f.lines[4])
	}

	m.needMode, m.needOpen, m.needIdx, m.needKey = true, true, 0, m.needRows()[0].key()
	f = m.render()
	for i, want := range map[int]string{3: "    1 ★ Merge", 4: "    2 Wait"} {
		if got := strings.TrimRight(ansi.Strip(f.lines[i]), " "); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	if !strings.HasPrefix(f.lines[2], selectedBG) || !strings.HasPrefix(f.lines[4], selectedBG) || f.header != 8 {
		t.Errorf("selected ask: %q %q header %d", f.lines[2], f.lines[3], f.header)
	}
	for i, l := range f.lines {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("line %d is %d wide", i, ansi.StringWidth(l))
		}
	}
}

func TestNeedsRenderNothing(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs, m.snap.sessions[1].Deciding = nil, false
	f := m.render()
	if f.header != 0 || len(f.needs) != 0 || !strings.Contains(f.lines[0], "SESSIONS") {
		t.Errorf("no rows: header %d, %v, %q", f.header, f.needs, f.lines[0])
	}
}

// Many rows leave sessions their room and keep the cursor on screen.
func TestNeedsRenderBudget(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	var items []needItem
	for i := 1; i <= 30; i++ {
		items = append(items, needItem{ID: i, Text: "item"})
	}
	m.needs = []needList{{Boss: "boss-dev", Items: items}}
	m.snap.height = 20
	f := m.render()
	if f.header > needsBudget(20) || len(f.lines) != 20 {
		t.Errorf("header at %d of %d lines, budget %d", f.header, len(f.lines), needsBudget(20))
	}
	m.needMode, m.needIdx = true, 25
	m.needKey = m.needRows()[25].key()
	f = m.render()
	if !slices.ContainsFunc(f.needs, func(h needHit) bool { return h.idx == 25 }) || f.header > needsBudget(20) {
		t.Errorf("cursor row 25 not drawn: %v, header %d", f.needs, f.header)
	}
	m.needOpen = true
	f = m.render()
	if !slices.ContainsFunc(f.needs, func(h needHit) bool { return h.idx == 25 }) {
		t.Errorf("open cursor row dropped: %v", f.needs)
	}
}

func TestNeedsKeys(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m, _ = key(m, "n")
	if !m.needMode || m.needIdx != 0 {
		t.Fatalf("n: mode %v idx %d", m.needMode, m.needIdx)
	}
	// Not shown yet: a digit answers nothing
	if _, cmd := key(m, "1"); cmd != nil {
		t.Error("digit answered before ⏎ showed the options")
	}
	m, _ = key(m, "enter")
	if !m.needOpen {
		t.Fatal("⏎ didn't show the options")
	}
	if _, cmd := key(m, "3"); cmd != nil {
		t.Error("option 3 of 2 answered")
	}
	// x, r, o, d don't reach the session under the cursor
	for _, k := range []string{"x", "r", "o", "d"} {
		if _, cmd := key(m, k); cmd != nil {
			t.Errorf("%s acted while in NEEDS YOU", k)
		}
	}
	_, cmd := key(m, "1")
	if cmd == nil {
		t.Fatal("1 didn't answer")
	}
	msg := cmd().(answeredMsg)
	if msg.err != nil || !slices.Equal(calls, []answerCall{{"boss-dev", 1, "Merge #148?", "Merge (Recommended)"}}) {
		t.Errorf("answer = %+v, calls %+v", msg, calls)
	}
	m, _ = key(m, "esc")
	if !m.needMode || m.needOpen {
		t.Errorf("first esc: mode %v open %v", m.needMode, m.needOpen)
	}
	m, _ = key(m, "esc")
	if m.needMode {
		t.Error("second esc stayed in NEEDS YOU")
	}
}

func TestNeedsKeysMove(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "enter")
	m, _ = key(m, "j")
	if m.needIdx != 1 || m.needOpen {
		t.Errorf("j: idx %d open %v, want 1 shut", m.needIdx, m.needOpen)
	}
	// A plain item has no options: no answer keys
	if _, cmd := key(m, "1"); cmd != nil {
		t.Error("plain item answered")
	}
	m, _ = key(m, "k")
	m, _ = key(m, "k")
	if m.needIdx != 2 {
		t.Errorf("k wraps: idx %d, want 2", m.needIdx)
	}
}

// ⏎ on a dialog row goes to its worker: tmux switches the client there.
func TestNeedsEnterDialog(t *testing.T) {
	var calls []answerCall
	var tmuxCalls []string
	m := needsModel(&calls)
	m.c = tmux.NewFake(func(args []string) (string, error) {
		tmuxCalls = append(tmuxCalls, strings.Join(args, " "))
		return "", nil
	})
	m, _ = key(m, "n")
	m, _ = key(m, "k")
	if m.needIdx != 2 {
		t.Fatalf("idx %d", m.needIdx)
	}
	m, cmd := key(m, "enter")
	if m.needMode || m.selected != "w1" || cmd == nil {
		t.Fatalf("mode %v selected %q cmd %v", m.needMode, m.selected, cmd)
	}
	cmd()
	if !slices.ContainsFunc(tmuxCalls, func(c string) bool { return strings.Contains(c, "switch-client") && strings.Contains(c, "w1") }) {
		t.Errorf("no switch to w1: %q", tmuxCalls)
	}
}

// The list changed under the cursor: the options shut and a digit answers nothing, so an answer is only ever to the ask shown.
func TestNeedsStaleRow(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "enter")
	// The boss's list emptied and #1 is a newer ask
	next, _ := m.Update(needsMsg{lists: []needList{{Boss: "boss-dev", Items: []needItem{{ID: 1, Text: "Release now?", Options: []string{"Now", "Later"}}}}}})
	m = next.(Model)
	if m.needOpen {
		t.Error("options stayed open on a different ask")
	}
	if _, cmd := key(m, "1"); cmd != nil {
		t.Error("answered a newer ask from a stale view")
	}
	if rows := m.needRows(); m.needKey != rows[m.needIdx].key() {
		t.Errorf("cursor key %q, row %q", m.needKey, rows[m.needIdx].key())
	}
}

func TestNeedsLeaveWhenUnfocusedOrEmpty(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m.snap.focused = false
	m.fixNeeds()
	if m.needMode {
		t.Error("stayed in NEEDS YOU unfocused")
	}
	m, _ = key(needsModel(&calls), "n")
	m.needs, m.snap.sessions[1].Deciding = nil, false
	m.fixNeeds()
	if m.needMode {
		t.Error("stayed in an empty NEEDS YOU")
	}
}

func TestNeedsReadFailure(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	next, _ := m.Update(needsMsg{err: errors.New("dev todo --all: boom")})
	m = next.(Model)
	if len(m.needs) != 1 || m.needRows()[0].text != "Merge #148?" {
		t.Error("a failed read dropped the lists")
	}
	if got := ansi.Strip(m.bottomRow()); !strings.Contains(got, "boom") {
		t.Errorf("bottom row = %q", got)
	}
	if got := len(m.render().lines); got != 30 {
		t.Errorf("%d lines", got)
	}
}

func TestNeedsAnswerError(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needMode, m.needOpen = true, true
	var shown []string
	m.c = tmux.NewFake(func(args []string) (string, error) {
		shown = append(shown, strings.Join(args, " "))
		return "", nil
	})
	next, _ := m.Update(answeredMsg{err: errors.New("item 1 is \"Release now?\" now"), client: "/dev/x"})
	m = next.(Model)
	if m.needOpen || len(shown) != 1 || !strings.Contains(shown[0], "❌") {
		t.Errorf("open %v, shown %q", m.needOpen, shown)
	}
}

// A click on a row acts like ⏎ on it; the + button stays on the SESSIONS header, wherever it is.
func TestNeedsMouse(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	f := m.render()
	click := func(m Model, x, y int) (Model, tea.Cmd) {
		next, cmd := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
		return next.(Model), cmd
	}
	m2, _ := click(m, 3, f.needs[0].line)
	if !m2.needMode || !m2.needOpen || m2.needIdx != 0 {
		t.Errorf("click on the ask: mode %v open %v idx %d", m2.needMode, m2.needOpen, m2.needIdx)
	}
	// A line of NEEDS YOU that isn't a row selects no session
	if m3, cmd := click(m, 3, 0); cmd != nil || m3.selected != "" {
		t.Errorf("click on the NEEDS YOU header: selected %q cmd %v", m3.selected, cmd)
	}
	if _, cmd := click(m, 36, f.header); cmd == nil {
		t.Error("+ on the SESSIONS header opened nothing")
	}
}

func TestOptionLabel(t *testing.T) {
	for in, want := range map[string]string{"Merge (Recommended)": "Merge", "Wait": "Wait", "A (recommended) ": "A (recommended) ", "(Recommended)": ""} {
		if got, _ := optionLabel(in); got != want {
			t.Errorf("optionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// An item above the cursor leaves: the cursor stays on its own row, options as they were.
func TestNeedsCursorFollowsRow(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "j")
	want := m.needKey
	next, _ := m.Update(needsMsg{lists: []needList{{Boss: "boss-dev", Items: []needItem{{ID: 4, Text: "Call Vince"}}}}})
	m = next.(Model)
	if m.needIdx != 0 || m.needKey != want {
		t.Errorf("idx %d key %q, want 0 and %q", m.needIdx, m.needKey, want)
	}
}

func TestNeedsAnswered(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needsLoad = func() ([]needList, error) { return nil, nil }
	m.needMode, m.needOpen = true, true
	next, cmd := m.Update(answeredMsg{out: "#1 answered, sent to boss-dev"})
	m = next.(Model)
	if m.needOpen || m.answerErr != nil || cmd == nil {
		t.Errorf("open %v err %v cmd %v: want options shut and the lists read again", m.needOpen, m.answerErr, cmd)
	}
}

// A failed answer stays on the bottom row until the next key, past the flash.
func TestNeedsAnswerErrorStays(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	next, _ := m.Update(answeredMsg{err: errors.New("ticked, not sent")})
	m = next.(Model)
	if got := ansi.Strip(m.bottomRow()); !strings.Contains(got, "not sent") {
		t.Errorf("bottom row = %q", got)
	}
	m, _ = key(m, "j")
	if m.answerErr != nil {
		t.Error("error stayed after a key")
	}
}

func TestNeedsStaleHeader(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needsErr = errors.New("boom")
	if got := ansi.Strip(m.render().lines[0]); !strings.Contains(got, "NEEDS YOU 3 (stale)") {
		t.Errorf("header = %q", got)
	}
}

// Only the polling chain's own read schedules the next one: an answer's extra read must not start a second chain.
func TestNeedsOneChain(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	if _, cmd := m.Update(needsMsg{chain: true}); cmd == nil {
		t.Error("the chain's read didn't schedule the next")
	}
	if _, cmd := m.Update(needsMsg{}); cmd != nil {
		t.Error("an extra read scheduled another tick")
	}
}

// A second digit while the first answer runs sends nothing.
func TestNeedsAnswerOnce(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "enter")
	m, cmd := key(m, "1")
	if cmd == nil || !m.answering {
		t.Fatal("1 didn't start an answer")
	}
	if _, cmd := key(m, "2"); cmd != nil {
		t.Error("a second answer started while the first ran")
	}
	next, _ := m.Update(answeredMsg{})
	if next.(Model).answering {
		t.Error("answering stayed set after the answer")
	}
}

func TestNeedsShortPane(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.snap.height = 9
	if f := m.render(); f.header != 0 || len(f.needs) != 0 {
		t.Errorf("short pane: header %d, rows %v", f.header, f.needs)
	}
}

// A long question shows whole once open, and every option has its own line: none hides behind another.
func TestNeedsOpenLong(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs[0].Items[0].Text = "Deploy after the maintenance window or right now while traffic is low?"
	m.needs[0].Items[0].Options = []string{"Deploy after the maintenance window (Recommended)", "Now"}
	m, _ = key(m, "n")
	m, _ = key(m, "enter")
	f := m.render()
	var plain []string
	for _, l := range f.lines[:f.header] {
		plain = append(plain, ansi.Strip(l))
		if ansi.StringWidth(l) > 40 {
			t.Errorf("%q is wider than the pane", l)
		}
	}
	all := strings.Join(plain, "\n")
	for _, want := range []string{"traffic is low?", "1 ★ Deploy after", "2 Now"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
}

// A pane too short to show NEEDS YOU takes no keys for it.
func TestNeedsHiddenTakesNoKeys(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "enter")
	m.snap.height = 9
	m.fixNeeds()
	if m.needMode || m.needOpen {
		t.Error("mode stayed on in a pane that hides the section")
	}
	if m, _ = key(m, "n"); m.needMode {
		t.Error("n entered a hidden section")
	}
}
