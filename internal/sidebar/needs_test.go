package sidebar

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

// needsModel has boss-dev's list (project dev) with two asks, a ticked one and one on a worker, a worker (deciding sets
// it waiting on a dialog, sorted first), and a recording answer func; boss-dev isn't marked a boss, so the sidebar
// shows the whole section.
// deciding is m with its worker waiting on a dialog.
func deciding(m Model) Model {
	m.snap.sessions = slices.Clone(m.snap.sessions)
	m.snap.sessions[1].Deciding = true
	return m
}

func needsModel(calls *[]answerCall) Model {
	return Model{
		c: tmux.NewFake(func([]string) (string, error) { return "", nil }),
		snap: snapshot{width: 40, height: 30, current: "boss-dev", focused: true, sessions: []Session{
			{Name: "boss-dev", Group: "dev", Project: "dev", ID: "$1", Agent: "Claude", State: "idle"},
			{Name: "w1", Group: "dev", Project: "dev", ID: "$2", StartedBy: "boss-dev", Depth: 1, Agent: "Claude", State: "waiting"},
		}},
		needs: []NeedList{{Boss: "boss-dev", Items: []NeedItem{
			{ID: 1, Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}, Status: StatusDecide},
			{ID: 2, Text: "Ticked", Done: true},
			{ID: 3, Text: "Staging", On: "Marek", Status: StatusWaiting, StatusOn: "Marek"},
			{ID: 4, Text: "Call Vince", Status: StatusYours},
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
	got := deciding(needsModel(&calls)).needRows()
	want := []NeedRow{
		{Kind: NeedDialog, Project: "dev", Who: "w1", Text: "w1", Session: "w1"},
		{Kind: NeedListAsk, Project: "dev", Who: "boss", Boss: "boss-dev", ID: 1, Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}, Session: "boss-dev"},
		{Kind: NeedPlain, Project: "dev", Who: "boss", Boss: "boss-dev", ID: 4, Text: "Call Vince", Session: "boss-dev"},
	}
	if !slices.EqualFunc(got, want, func(a, b NeedRow) bool {
		return a.key() == b.key() && a.Project == b.Project && a.Who == b.Who && slices.Equal(a.Options, b.Options) && a.Session == b.Session
	}) {
		t.Errorf("rows = %+v\nwant %+v", got, want)
	}
	// A list not read is a row saying why, ⏎ going to its boss
	m := deciding(needsModel(&calls))
	m.needs = append(m.needs, NeedList{Boss: "boss-web", Error: "list not read: bad JSON"})
	if r := m.needRows(); len(r) != 4 || r[3].Kind != NeedUnread || r[3].Text != "list not read: bad JSON" || r[3].Session != "boss-web" || !strings.Contains(m.needLine(r[3], 40, false), "⚠") {
		t.Errorf("rows with a list not read = %+v", r)
	}
	// A boss no session carries is shown by its name without "boss-"
	m = needsModel(&calls)
	m.snap.sessions = nil
	if r := m.needRows(); r[0].Project != "dev" {
		t.Errorf("project without a session = %q", r[0].Project)
	}
}

func TestNeedsRender(t *testing.T) {
	var calls []answerCall
	m := deciding(needsModel(&calls))
	f := m.render()
	plain := func(i int) string { return strings.TrimRight(ansi.Strip(f.lines[i]), " ") }
	for i, want := range []string{" TO DO  1 need you · 1 decide · 1 to do", "", " w1  ● w1", " dev  ? Merge #148?", " dev  ☐ Call Vince", "", " SESSIONS                           +"} {
		if got := plain(i); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	if f.header != 6 || len(f.needs) != 3 || f.needs[1] != (needHit{line: 3, idx: 1}) {
		t.Errorf("header %d, hits %v", f.header, f.needs)
	}
	if !strings.Contains(f.lines[0], bold+"TO DO") || !strings.Contains(f.lines[0], red+"1 need you") || !strings.Contains(f.lines[2], red+"●") {
		t.Errorf("colours: %q %q", f.lines[0], f.lines[2])
	}

	m = needsModel(&calls)
	m.needMode, m.needOpen, m.needIdx, m.needKey = true, true, 0, m.needRows()[0].key()
	f = m.render()
	for i, want := range map[int]string{3: "    1 ★ Merge", 4: "    2 Wait"} {
		if got := strings.TrimRight(ansi.Strip(f.lines[i]), " "); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	if !strings.HasPrefix(f.lines[2], selectedBG) || !strings.HasPrefix(f.lines[4], selectedBG) || f.header != 7 {
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
	var items []NeedItem
	for i := 1; i <= 30; i++ {
		items = append(items, NeedItem{ID: i, Text: "item", Status: StatusYours})
	}
	m.needs = []NeedList{{Boss: "boss-dev", Items: items}}
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
			t.Errorf("%s acted while in TO DO", k)
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
		t.Error("second esc stayed in TO DO")
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
	if m.needIdx != 1 {
		t.Errorf("k wraps: idx %d, want 1", m.needIdx)
	}
}

// ⏎ on a dialog row goes to its worker: tmux switches the client there.
func TestNeedsEnterDialog(t *testing.T) {
	var calls []answerCall
	var tmuxCalls []string
	m := deciding(needsModel(&calls))
	m.c = tmux.NewFake(func(args []string) (string, error) {
		tmuxCalls = append(tmuxCalls, strings.Join(args, " "))
		return "", nil
	})
	m, _ = key(m, "n")
	if m.needIdx != 0 || m.needRows()[0].Kind != NeedDialog {
		t.Fatalf("idx %d", m.needIdx)
	}
	if m, _ = key(m, "enter"); !m.needOpen || m.selected != "" {
		t.Fatalf("first ⏎ must expand: open %v selected %q", m.needOpen, m.selected)
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
	next, _ := m.Update(needsMsg{lists: []NeedList{{Boss: "boss-dev", Items: []NeedItem{{ID: 1, Text: "Release now?", Options: []string{"Now", "Later"}, Status: StatusDecide}}}}})
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
		t.Error("stayed in TO DO unfocused")
	}
	m, _ = key(needsModel(&calls), "n")
	m.needs, m.snap.sessions[1].Deciding = nil, false
	m.fixNeeds()
	if m.needMode {
		t.Error("stayed in an empty TO DO")
	}
}

func TestNeedsReadFailure(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	next, _ := m.Update(needsMsg{err: errors.New("dev todo --all: boom")})
	m = next.(Model)
	if len(m.needs) != 1 || m.needRows()[0].Text != "Merge #148?" {
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
	// A line of TO DO that isn't a row selects no session
	if m3, cmd := click(m, 3, 0); cmd != nil || m3.selected != "" {
		t.Errorf("click on the TO DO header: selected %q cmd %v", m3.selected, cmd)
	}
	if _, cmd := click(m, 36, f.header); cmd == nil {
		t.Error("+ on the SESSIONS header opened nothing")
	}
}

// A list item's option shows its label only; a session's choice keeps a " | " in it.
func TestOptionLabel(t *testing.T) {
	for _, tt := range []struct {
		list    bool
		in, out string
		rec     bool
	}{
		{true, "Merge (Recommended)", "Merge", true},
		{true, "Wait", "Wait", false},
		{true, "A (recommended) ", "A", true},
		{true, "(Recommended)", "", true},
		{true, "Merge | CI green (Recommended)", "Merge", true},
		{true, "Merge (Recommended) | CI green", "Merge", true},
		{false, "Run | staging (Recommended)", "Run | staging", true},
	} {
		if got, rec := optionLabel(tt.list, tt.in); got != tt.out || rec != tt.rec {
			t.Errorf("optionLabel(%v, %q) = %q %v, want %q %v", tt.list, tt.in, got, rec, tt.out, tt.rec)
		}
	}
}

// An item above the cursor leaves: the cursor stays on its own row, options as they were.
func TestNeedsCursorFollowsRow(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	m, _ = key(m, "j")
	want := m.needKey
	next, _ := m.Update(needsMsg{lists: []NeedList{{Boss: "boss-dev", Items: []NeedItem{{ID: 4, Text: "Call Vince", Status: StatusYours}}}}})
	m = next.(Model)
	if m.needIdx != 0 || m.needKey != want {
		t.Errorf("idx %d key %q, want 0 and %q", m.needIdx, m.needKey, want)
	}
}

func TestNeedsAnswered(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needsLoad = func() ([]NeedList, error) { return nil, nil }
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
	if got := ansi.Strip(m.render().lines[0]); !strings.Contains(got, "TO DO  1 decide · 1 to do (stale)") {
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

// A pane too short to show TO DO takes no keys for it.
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

// A boss under a manager keeps its own group, and TO DO rows show that project, not the manager's.
func TestNeedsLabelIsProject(t *testing.T) {
	sessions := Order([]Session{
		{Name: "manager", Group: "manager", ID: "$1", Boss: true},
		{Name: "boss-nocworx-core", Group: "nocworx-core", ID: "$2", Boss: true, StartedBy: "$1"},
	})
	if sessions[1].Group != "nocworx-core" || sessions[1].Project != "nocworx-core" || sessions[1].Root != "manager" {
		t.Fatalf("order: %+v", sessions[1])
	}
	lists := []NeedList{{Boss: "boss-nocworx-core", Items: []NeedItem{{ID: 1, Text: "Call", Status: StatusYours}}}}
	if r := NeedRows(sessions, lists, "manager"); len(r) != 1 || r[0].Project != "nocworx-core" || r[0].Who != "boss" {
		t.Errorf("rows = %+v", r)
	}
}

func dialogModel(q string) Model {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs = nil
	m.snap.sessions = []Session{{Name: "w1", Group: "dev", Project: "dev", Deciding: true, Question: q}}
	return m
}

func TestNeedsDialogText(t *testing.T) {
	for _, tc := range []struct {
		q, text string
		options []string
	}{
		{`{"tool":"Bash","command":"echo hi"}`, "Bash: echo hi", nil},
		{`{"tool":"Bash","command":"echo a\necho  b"}`, "Bash: echo a echo b", nil},
		{`{"tool":"Read","file":"/a"}`, "Read", nil},
		{`{"tool":"AskUserQuestion","questions":[{"question":"Which?","options":[{"label":"A (Recommended)"},{"label":"B"}]}]}`, "Which?", []string{"A (Recommended)", "B"}},
		{`{"tool":"AskUserQuestion"}`, "AskUserQuestion", nil},
		{"", "w1", nil},
		{"junk", "w1", nil},
	} {
		r := dialogModel(tc.q).needRows()
		if len(r) != 1 || r[0].Text != tc.text || r[0].label() != "w1" || !slices.Equal(r[0].Options, tc.options) {
			t.Errorf("%q → %+v, want text %q options %v", tc.q, r, tc.text, tc.options)
		}
	}
}

// ⏎ expands a plain item with its full text, then goes to its boss; a dialog's choices are shown, and a digit sends nothing.
func TestNeedsExpandPlainAndDialog(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs[0].Items = []NeedItem{{ID: 4, Text: "Be reachable Tuesday night and Wednesday morning for the rollout", Status: StatusYours}}
	m, _ = key(m, "n")
	m, _ = key(m, "enter")
	all := ansi.Strip(strings.Join(m.render().lines, "\n"))
	if !m.needOpen || !strings.Contains(all, "rollout") || strings.Contains(m.needsHint(), "1-9") {
		t.Fatalf("not expanded (open %v, hint %q):\n%s", m.needOpen, m.needsHint(), all)
	}
	if _, cmd := key(m, "enter"); cmd == nil {
		t.Error("second ⏎ must go to the boss")
	}

	m = dialogModel(`{"tool":"AskUserQuestion","questions":[{"question":"Which database should the new service use for storage?","options":[{"label":"Postgres (Recommended)"},{"label":"SQLite"}]}]}`)
	m, _ = key(m, "n")
	m, _ = key(m, "enter")
	all = ansi.Strip(strings.Join(m.render().lines, "\n"))
	for _, want := range []string{"storage?", "★ Postgres", "· SQLite"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	for _, l := range m.render().lines {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("%q wider than 40", l)
		}
	}
	m.needsAnswer = func(string, int, string, string) (string, error) {
		t.Error("a dialog's choice was answered from the sidebar")
		return "", nil
	}
	if m, cmd := key(m, "1"); cmd != nil || m.answering {
		t.Error("a digit on a dialog did something")
	}
}

// A list not read: ⏎ shows the whole error, then goes to the boss.
func TestNeedsBrokenRowExpands(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs = []NeedList{{Boss: "boss-web", Error: "list not read: a long reason that does not fit in one line"}}
	m.snap.sessions = nil
	m, _ = key(m, "n")
	m, cmd := key(m, "enter")
	if !m.needOpen || cmd != nil || !strings.Contains(ansi.Strip(strings.Join(m.render().lines, "\n")), "one line") {
		t.Fatalf("first ⏎: open %v cmd %v", m.needOpen, cmd)
	}
	if _, cmd := key(m, "enter"); cmd == nil {
		t.Error("second ⏎ must go to the boss")
	}
}

// Clicks follow ⏎: the first expands a plain row, the second on it goes to its boss.
func TestNeedsMouseSecondClickGoes(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	click := func(m Model) (Model, tea.Cmd) {
		next, cmd := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: m.render().needs[1].line})
		return next.(Model), cmd
	}
	m, cmd := click(m)
	if !m.needOpen || cmd != nil {
		t.Fatalf("first click: open %v cmd %v", m.needOpen, cmd)
	}
	if m, cmd = click(m); m.needMode || cmd == nil {
		t.Errorf("second click: mode %v cmd %v", m.needMode, cmd)
	}
}

// A second click on an open ask keeps its options open: it has answers to pick, not a session to go to.
func TestNeedsMouseOpenAskStaysOpen(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	for range 2 {
		next, cmd := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: m.render().needs[0].line})
		m = next.(Model)
		if cmd != nil || !m.needOpen {
			t.Fatalf("open %v cmd %v", m.needOpen, cmd)
		}
	}
}

// boardModel is a manager over boss-dev (its worker w1 on a dialog, its list with an ask) and boss-web with
// nothing under it, plus a boss of its own (boss-solo) whose list waits on the user.
func boardModel(calls *[]answerCall) Model {
	m := needsModel(calls)
	m.snap.current = "vitay"
	m.snap.sessions = Order([]Session{
		{Name: "manager", Group: "manager", ID: "$1", Boss: true},
		{Name: "boss-dev", Group: "dev", ID: "$2", Boss: true, StartedBy: "manager"},
		{Name: "w1", Group: "dev", ID: "$3", StartedBy: "boss-dev", Agent: "Claude", State: "waiting", Deciding: true},
		{Name: "boss-web", Group: "web", ID: "$4", Boss: true, StartedBy: "manager"},
		{Name: "boss-solo", Group: "solo", ID: "$5", Boss: true},
		{Name: "vitay", Group: "vitay", ID: "$6", Agent: "Claude", State: "waiting", Deciding: true},
	})
	m.needs = []NeedList{
		{Boss: "boss-dev", Items: []NeedItem{{ID: 1, Text: "Merge?", Options: []string{"Yes", "No"}, Status: StatusDecide}}},
		{Boss: "boss-solo", Items: []NeedItem{{ID: 1, Text: "Call Vince", Status: StatusYours}}},
	}
	return m
}

// A manager and a boss in no manager's group have boards; a boss under the manager doesn't.
func TestBoards(t *testing.T) {
	var calls []answerCall
	if got := Boards(boardModel(&calls).snap.sessions); !slices.Equal(got, []string{"manager", "boss-solo"}) {
		t.Errorf("boards %q", got)
	}
}

// With a boss on the server TO DO is one line, its counts the boards' blocks'; n focuses it, ⏎ goes to the first
// board with any and asks it to focus its block; a click does too.
func TestNeedsBoardLine(t *testing.T) {
	var calls []answerCall
	m := boardModel(&calls)
	m.snap.sessions[5].Deciding = false
	if r := m.needRows(); len(r) != 1 || r[0].Kind != needBoard || r[0].ID != 3 || r[0].Session != "manager" {
		t.Fatalf("rows = %+v", r)
	}
	f := m.render()
	if got := strings.TrimRight(ansi.Strip(f.lines[0]), " "); !strings.HasPrefix(got, " TO DO  1 need you · 1 decide · 1 to do") || f.header != 2 {
		t.Errorf("line %q header %d", got, f.header)
	}
	var ran []string
	m.c = tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		if args[0] == "list-panes" {
			return "%10\tcoordinator\n%11\tpreviews\n", nil
		}
		return "", nil
	})
	want := []string{
		"list-panes -s -t =manager: -F #{pane_id}\t#{@dev_boss_pane}",
	}
	m, _ = key(m, "n")
	if !m.needMode || !strings.Contains(m.needsHint(), "⏎ board") {
		t.Fatalf("n: mode %v hint %q", m.needMode, m.needsHint())
	}
	m, cmd := key(m, "enter")
	if m.needMode || cmd == nil {
		t.Fatalf("⏎: mode %v", m.needMode)
	}
	cmd()
	if len(ran) < 3 || !slices.Equal(ran[:1], want) || !slices.ContainsFunc(ran, func(c string) bool { return strings.HasPrefix(c, "switch-client") && strings.Contains(c, "=manager:") }) ||
		!slices.Contains(ran, "set-option -p -t %11 @dev_needs_focus 1 ; select-window -t %11 ; select-pane -t %11") {
		t.Errorf("ran %q", ran)
	}
	ran = nil
	next, cmd := m.Update(tea.MouseMsg{X: 3, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatal("click: no jump")
	}
	_ = next
	cmd()
	if len(ran) < 2 || !slices.Equal(ran[:1], want) {
		t.Errorf("click ran %q", ran)
	}
	// A session under no boss shows here, under the boards' row
	m.snap.sessions[5].Deciding = true
	f = m.render()
	for i, want := range []string{" TO DO  2 need you · 1 decide · 1 to do", "", " board  → 3 on the board", " vitay  ● vitay"} {
		if got := strings.TrimRight(ansi.Strip(f.lines[i]), " "); !strings.HasPrefix(got, want) {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	// Nothing on any board nor off them: no section
	m.needs, m.snap.sessions[2].Deciding, m.snap.sessions[5].Deciding = nil, false, false
	if f := m.render(); f.header != 0 {
		t.Errorf("no rows: header %d", f.header)
	}
}

// d ticks a plain item as dev todo done does, never an ask; a failure shows on the bottom row.
func TestNeedsTick(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	var ticked [][2]any
	m.needsDone = func(boss string, id int, text string) (string, error) {
		ticked = append(ticked, [2]any{boss, id})
		return "done 4", nil
	}
	m, _ = key(m, "n")
	if m, cmd := key(m, "d"); cmd != nil || m.answering {
		t.Error("d on an ask did something")
	}
	m, _ = key(m, "j")
	if hint := ansi.Strip(m.needsHint()); !strings.Contains(hint, "d tick (stays till ticked)") {
		t.Errorf("hint %q", hint)
	}
	m, cmd := key(m, "d")
	if cmd == nil || !m.answering {
		t.Fatal("d on a plain item sent nothing")
	}
	if _, again := key(m, "d"); again != nil {
		t.Error("a second d while the tick is out sent another")
	}
	next, _ := m.Update(cmd())
	if m = next.(Model); m.answering || m.answerErr != nil || len(ticked) != 1 || ticked[0] != [2]any{"boss-dev", 4} || len(calls) != 0 {
		t.Errorf("answering %v err %v ticked %v answers %v", m.answering, m.answerErr, ticked, calls)
	}
	if _, again := key(m, "d"); again != nil {
		t.Error("d took the ticked row before a read showed it gone")
	}
	gone := slices.Clone(m.needs)
	gone[0].Items = slices.Clone(gone[0].Items)
	gone[0].Items[3].Done = true
	next, _ = m.Update(needsMsg{lists: gone})
	if m = next.(Model); m.ticked != "" {
		t.Errorf("ticked %q after the row went", m.ticked)
	}

	m = needsModel(&calls)
	m.needsDone = func(string, int, string) (string, error) { return "", errors.New("timed out") }
	m, _ = key(m, "n")
	m, _ = key(m, "j")
	m, cmd = key(m, "d")
	next, _ = m.Update(cmd())
	if m = next.(Model); m.answerErr == nil || !strings.Contains(m.answerErr.Error(), "timed out") {
		t.Errorf("failure not kept: %v", m.answerErr)
	}
	if _, again := key(m, "d"); again == nil {
		t.Error("the row didn't take d again after a failed tick")
	}
}

// An open ask wraps a long option under its number instead of cutting it.
func TestNeedsOpenLongOption(t *testing.T) {
	var calls []answerCall
	r := NeedRow{Kind: NeedListAsk, Project: "dev", Text: "Pick?", Options: []string{"Keep the grouping, add the items under their boss", "Wait"}, Session: "boss-dev"}
	var got []string
	lines, _ := needsModel(&calls).openLines(r, 30)
	for _, l := range lines {
		if ansi.StringWidth(l) > 30 {
			t.Errorf("%q is wider than 30", l)
		}
		got = append(got, strings.TrimRight(ansi.Strip(l), " "))
	}
	text := strings.Join(got, "\n")
	for _, want := range []string{"1 Keep the grouping, add", "the items under their", "boss", "2 Wait"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing:\n%s", want, text)
		}
	}
}

// An open ask with long wrapped options is cut to the pane, so TO DO's header stays on screen.
func TestNeedsOpenLongOptionsFitPane(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	long := strings.Repeat("a long option ", 6)
	m.needs[0].Items[0].Options = []string{long, long, long, long}
	m.snap.height = 12
	m.needMode, m.needOpen, m.needIdx, m.needKey = true, true, 0, m.needRows()[0].key()
	lines, _ := m.renderNeeds(40, 12)
	if len(lines) > 12 || !strings.Contains(ansi.Strip(lines[0]), "TO DO") {
		t.Errorf("%d lines, first %q", len(lines), lines[0])
	}
}

// A row of a stale list shows its list's age at the right edge, unless the text would keep under MinTextAfterAge.
func TestNeedLineStaleAge(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.needs[0].Stale, m.needs[0].Updated = true, time.Now().Add(-18*time.Hour-30*time.Second).Unix()
	rows := m.needRows()
	line := ansi.Strip(m.needLine(rows[0], 40, false))
	if !strings.HasSuffix(line, " 18h") || ansi.StringWidth(line) != 39 {
		t.Errorf("line = %q (%d wide)", line, ansi.StringWidth(line))
	}
	if line := ansi.Strip(m.needLine(rows[0], 22, false)); strings.Contains(line, "18h") {
		t.Errorf("narrow line = %q", line)
	}
	openAsk, _ := m.openLines(rows[0], 40)
	if open := ansi.Strip(strings.Join(openAsk, "\n")); strings.Contains(open, "18h") {
		t.Errorf("open ask marked:\n%s", open)
	}
	m.needs[0].Error = "list not read: bad JSON"
	if r := m.needRows(); r[len(r)-1].Kind != NeedUnread || r[len(r)-1].Age(time.Now()) != "" {
		t.Errorf("unread row marked: %+v", r[len(r)-1])
	}
	m.needs[0].Error, m.needs[0].Stale = "", false
	if line := ansi.Strip(m.needLine(m.needRows()[0], 40, false)); strings.Contains(line, "18h") {
		t.Errorf("fresh list marked: %q", line)
	}
}

// A click on an open ask's option answers it as its key does; one on a closed ask's row only opens it.
func TestNeedsMouseOption(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	press := func(m Model, y int) (Model, tea.Cmd) {
		next, cmd := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 8, Y: y})
		return next.(Model), cmd
	}
	m, _ = press(m, m.render().needs[0].line)
	var y int
	for _, h := range m.render().needs {
		if h.opt == 2 {
			y = h.line
		}
	}
	if y == 0 {
		t.Fatalf("no target for option 2: %v", m.render().needs)
	}
	m, cmd := press(m, y)
	if cmd == nil || !m.answering {
		t.Fatalf("click on option 2: cmd %v answering %v", cmd, m.answering)
	}
	cmd()
	if len(calls) != 1 || calls[0].option != "Wait" {
		t.Errorf("answered %+v, want Wait", calls)
	}
}

// d ticks a closed boss's item by its list; ⏎ and 1-9 say how to reopen it.
func TestNeedsTickClosed(t *testing.T) {
	var calls []answerCall
	m := needsModel(&calls)
	m.snap.sessions = nil
	m.needs = []NeedList{{Boss: "boss-kite", Closed: true, Items: []NeedItem{{ID: 3, Text: "Ask Mara", Status: StatusBossClosed}}}}
	var ticked [][2]any
	m.needsDone = func(boss string, id int, text string) (string, error) {
		ticked = append(ticked, [2]any{boss, id})
		return "done 3", nil
	}
	m, _ = key(m, "n")
	for _, k := range []string{"enter", "1"} {
		if _, cmd := key(m, k); cmd != nil {
			t.Errorf("%s on a closed boss's row acted", k)
		}
	}
	m, cmd := key(m, "d")
	if cmd == nil {
		t.Fatal("d on a closed boss's item sent nothing")
	}
	cmd()
	if !slices.Equal(ticked, [][2]any{{"boss-kite", 3}}) {
		t.Errorf("ticked %v, want boss-kite #3", ticked)
	}
}

// Every status is in exactly one group; the groups go in the order of their first status in Statuses.
func TestGroupsCoverStatuses(t *testing.T) {
	for _, st := range Statuses {
		n := 0
		for _, g := range Groups {
			if slices.Contains(g.Statuses, st) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q in %d groups", st, n)
		}
	}
	for i := 1; i < len(Groups); i++ {
		if slices.Index(Statuses, Groups[i-1].Statuses[0]) > slices.Index(Statuses, Groups[i].Statuses[0]) {
			t.Errorf("%s before %s", Groups[i-1].Title, Groups[i].Title)
		}
	}
}

// space opens the list item under the cursor in its popup, with its text to check it by.
func TestNeedsOpenItem(t *testing.T) {
	var calls []answerCall
	m, _ := key(needsModel(&calls), "n")
	var opened []answerCall
	m.openItem = func(_, boss string, id int, text string) error {
		opened = append(opened, answerCall{boss, id, text, ""})
		return nil
	}
	_, cmd := key(m, " ")
	if cmd == nil {
		t.Fatal("space opened nothing")
	}
	cmd()
	if !slices.Equal(opened, []answerCall{{"boss-dev", 1, "Merge #148?", ""}}) {
		t.Errorf("opened %+v", opened)
	}
	// A session's row and an unread list's open nothing
	m.needs = append(m.needs, NeedList{Boss: "boss-x", Error: "list not read"})
	rows, n := m.needRows(), 0
	for i, r := range rows {
		if r.Boss != "" && r.Kind != NeedUnread {
			continue
		}
		n++
		m.needIdx, m.needKey = i, r.key()
		if _, cmd := key(m, " "); cmd != nil {
			t.Errorf("space on %+v ran a command", r)
		}
	}
	if n == 0 || len(opened) != 1 {
		t.Errorf("checked %d rows, opened %+v", n, opened)
	}
}
