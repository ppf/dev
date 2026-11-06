package herd

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// colors is the question claude-ask-q1 and claude-ask-q2-* show, as its hook reports it.
var colors = agent.Question{Tool: agent.AskUserQuestion, Questions: []agent.Ask{
	{Question: "Which color should the theme use?", Header: "Color", Options: []agent.Choice{{Label: "Red", Description: "Warm and loud"}, {Label: "Green", Description: "Calm"}, {Label: "Blue", Description: "Cool and safe"}}},
	{Question: "Which extras do you want?", Header: "Extras", MultiSelect: true, Options: []agent.Choice{{Label: "Tests", Description: "Add unit tests"}, {Label: "Docs", Description: "Write README"}, {Label: "Lint", Description: "Run linter"}}},
}}

func goldenPick(t *testing.T, file string, q agent.Question) pick {
	t.Helper()
	capture := golden(t, file)
	d, ok := parseDialog(capture)
	if !ok {
		t.Fatalf("%s: no dialog", file)
	}
	return newPick(d, q, ansi.Strip(capture))
}

func TestNewPick(t *testing.T) {
	p := goldenPick(t, "claude-ask-q2-checked", colors)
	if !slices.Equal(p.tabs, []string{"Color", "Extras", "Submit"}) || p.cur != 1 || p.other != 3 || !p.d.multi {
		t.Errorf("second question: %+v", p)
	}
	p = goldenPick(t, "claude-ask-review", agent.Question{Tool: agent.AskUserQuestion, Questions: colors.Questions})
	if p.cur != 2 || len(p.tabs) != 3 {
		t.Errorf("review: tabs %q cur %d", p.tabs, p.cur)
	}
	// Another question's hook: the screen alone
	p = goldenPick(t, "claude-ask-q1", agent.Question{Tool: agent.AskUserQuestion, Questions: []agent.Ask{{Question: "Other?", Header: "X"}}})
	if !slices.Equal(p.tabs, []string{"Color", "Extras", "Submit"}) || p.cur != -1 || p.other != 3 {
		t.Errorf("unmatched hook: %+v", p)
	}
	bash := agent.Question{Tool: "Bash", Command: "mkdir -p build/out && touch build/out/x.txt", Description: "Create build/out directory and touch x.txt"}
	p = goldenPick(t, "claude-permission-bash", bash)
	if p.tool != "Bash" || p.detail != bash.Command || p.about != bash.Description || p.other != -1 {
		t.Errorf("permission: %+v", p)
	}
	// A stale hook from an earlier prompt (answered No, which runs no hook) isn't shown
	if p = goldenPick(t, "claude-permission-bash", agent.Question{Tool: "Bash", Command: "rm -rf build"}); p.tool != "" {
		t.Errorf("stale permission shown: %+v", p)
	}
	if p = goldenPick(t, "claude-permission-write", agent.Question{Tool: "Write", File: "/tmp/proj/notes.txt"}); p.tool != "Write" || p.detail != "/tmp/proj/notes.txt" {
		t.Errorf("write permission: %+v", p)
	}
}

// screens fakes a pane whose captures go through the given screens (the last one repeats), recording the keys.
func screens(t *testing.T, files ...string) (*tmux.Client, *[]string) {
	var sent []string
	n := 0
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			f := files[min(n, len(files)-1)]
			n++
			if f == "" {
				return "\n", nil
			}
			return golden(t, f), nil
		}
		sent = append(sent, strings.Join(args[3:], " "))
		return "", nil
	})
	return c, &sent
}

func fakeClock() control.Clock {
	now := time.Unix(0, 0)
	return control.Clock{Now: func() time.Time { return now }, Sleep: func(d time.Duration) { now = now.Add(d) }}
}

func TestSend(t *testing.T) {
	tests := []struct {
		name   string
		files  []string // what the pane shows: when sending, then after the keys
		shown  string   // the screen the picker showed
		answer func(p pick) answer
		keys   []string
		msg    string
	}{
		{"choose", []string{"claude-ask-q1", "claude-ask-q2-multi"}, "claude-ask-q1", func(pick) answer { return choose(1) }, []string{"-l 2"}, "Answered w"},
		{"toggle", []string{"claude-ask-q2-multi", "claude-ask-q2-checked"}, "claude-ask-q2-multi", func(pick) answer { return toggle(0) }, []string{"-l 1"}, "Answered w"},
		{"submit moves to the Submit row", []string{"claude-ask-q2-checked", "claude-ask-review"}, "claude-ask-q2-checked", func(pick) answer { return submit() },
			[]string{"Down", "Down", "Down", "Down", "Enter"}, "Answered w"},
		{"write in", []string{"claude-ask-single", ""}, "claude-ask-single", func(p pick) answer { return writeIn(p.other, "medium please") },
			[]string{"-l 3", "-l -- medium please", "Enter"}, "Answered w"},
		{"write in a multi-select", []string{"claude-ask-multi-single", "claude-ask-multi-other-typed"}, "claude-ask-multi-single",
			func(p pick) answer { return writeInMulti(p.other, "a fish") }, []string{"Down", "Down", "-l -- a fish"}, "Answered w"},
		{"permission", []string{"claude-permission-bash", ""}, "claude-permission-bash", func(pick) answer { return choose(3) }, []string{"-l 4"}, "Answered w"},
		{"another dialog since: nothing sent", []string{"claude-ask-q2-multi"}, "claude-ask-q1", func(pick) answer { return choose(0) }, nil,
			"⚠ w's dialog changed: nothing sent, showing it again"},
		{"checked since: nothing sent", []string{"claude-ask-q2-checked"}, "claude-ask-q2-multi", func(pick) answer { return toggle(0) }, nil,
			"⚠ w's dialog changed: nothing sent, showing it again"},
		{"keys that don't take", []string{"claude-ask-q1"}, "claude-ask-q1", func(pick) answer { return choose(0) }, []string{"-l 1"},
			"❌ w: keys sent, but the dialog didn't change: check the worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, sent := screens(t, tt.files...)
			p := goldenPick(t, tt.shown, agent.Question{})
			m := Model{c: c, clk: fakeClock()}
			msg := m.send("w", "%5", p.d, tt.answer(p))().(answeredMsg)
			if msg.msg.text != tt.msg || msg.took != strings.HasPrefix(tt.msg, "Answered") || !slices.Equal(*sent, tt.keys) {
				t.Errorf("sent %q: %+v, want %q %q", *sent, msg, tt.keys, tt.msg)
			}
		})
	}
}

// pickerModel is twoTiles with tile a waiting on the claude-ask-q1 question, its reply box open.
func pickerModel(t *testing.T, file string, q agent.Question) Model {
	m := twoTiles()
	m.height = 24
	m.clk = fakeClock()
	m.snap.tiles[0].pane = "%5"
	m.snap.tiles[0].deciding = true
	p := goldenPick(t, file, q)
	m.snap.tiles[0].pick = &p
	m.open(m.snap.tiles[0])
	return m
}

func press(m Model, keys ...tea.KeyMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var next tea.Model
		next, cmd = m.Update(k)
		m = next.(Model)
	}
	return m, cmd
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestPickerKeys(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	if m.box.hl != 0 || m.activePick() == nil {
		t.Fatalf("picker: hl %d", m.box.hl)
	}
	// The card shows the dialog, its options side by side, the highlighted one bracketed; the box under the
	// board its keys
	got := ansiStrip(m.render())
	want := []string{
		"┃│    Color · Extras · Submit          │",
		"┃│    Which color should the theme use?│",
		"┃│    [1 Red]  2 Green   3 Blue        │",
		"┃│     4 Type something.               │",
		"┃│     5 Chat about this               │",
		"┃│    Red: Warm and loud               │",
		"┃│    Green: Calm                      │",
		"┃│    Blue: Cool and safe              │",
	}
	if !slices.Equal(got[3:11], want) || got[22] != " ╭─ Answer ● a Claude · waiting · $1… ─╮" || got[23] != " ╰─ ←→ move · ⏎ pick · 1-9 · esc clo… ─╯" {
		t.Errorf("picker:\n%s", strings.Join(got, "\n"))
	}
	if m, _ := press(m, tea.KeyMsg{Type: tea.KeyRight}); !strings.Contains(ansi.Strip(m.render()[5]), "[2 Green]") {
		t.Errorf("→ doesn't move to Green: %q", ansi.Strip(m.render()[5]))
	}
	// ↓ then ⏎ picks Green; keys wait while it's in flight and until the screen moves on
	m, cmd := press(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.answering != "a" || m.box.hl != 1 {
		t.Fatalf("enter: answering %q hl %d", m.answering, m.box.hl)
	}
	if next, cmd := press(m, runes("3")); cmd != nil || next.box.hl != 1 {
		t.Error("a key while answering does something")
	}
	if !strings.Contains(ansi.Strip(m.render()[23]), "⏳ answering a") {
		t.Errorf("waiting hint %q", ansi.Strip(m.render()[23]))
	}
	m, _ = press(m)
	next, _ := m.Update(answeredMsg{session: "a", msg: info("Answered a"), took: true})
	m = next.(Model)
	if m.answering != "" || m.box.sent == "" {
		t.Errorf("answered: answering %q sent %q", m.answering, m.box.sent)
	}
	// The next refresh shows the next question: the picker follows it, on its cursor
	p := goldenPick(t, "claude-ask-q2-checked", colors)
	m.snap.tiles[0].pick = &p
	m.syncPick()
	if m.box.sent != "" || m.box.hl != 0 || m.box.hlSig != p.d.sig() {
		t.Errorf("next question: %+v", m.box)
	}
	// A failed answer can be retried at once
	m.answering, m.box.sent = "a", p.d.key()
	next, _ = m.Update(answeredMsg{session: "a", msg: alert("❌ a: nope")})
	if m = next.(Model); m.waiting(&p) || !strings.Contains(ansi.Strip(m.render()[23]), "❌ a: nope") {
		t.Errorf("failed answer: %+v", m.box)
	}
}

func TestPickerMulti(t *testing.T) {
	m := pickerModel(t, "claude-ask-q2-checked", colors)
	got := ansiStrip(m.render())
	if got[5] != "┃│    [1 ☑ Tests]  2 ☐ Docs   3 ☑ Lint │" || got[7] != "┃│     5 Chat about this   Submit ⏎    │" || got[23] != " ╰─ ←→ move · space toggle · ⏎ next … ─╯" {
		t.Errorf("multi picker:\n%s", strings.Join(got, "\n"))
	}
	// space and digits toggle, ⏎ goes on; "Chat about this" answers at once
	for _, tt := range []struct {
		key  tea.KeyMsg
		want []string
	}{
		{tea.KeyMsg{Type: tea.KeySpace}, []string{"-l 1"}},
		{runes("2"), []string{"-l 2"}},
		{tea.KeyMsg{Type: tea.KeyEnter}, []string{"Down", "Down", "Down", "Down", "Enter"}},
		{runes("5"), []string{"-l 5"}},
	} {
		c, sent := screens(t, "claude-ask-q2-checked", "")
		m.c = c
		_, cmd := press(m, tt.key)
		if cmd == nil {
			t.Fatalf("%v: no answer", tt.key)
		}
		cmd()
		if !slices.Equal(*sent, tt.want) {
			t.Errorf("%v sent %q, want %q", tt.key, *sent, tt.want)
		}
	}
	// The free-text option opens for typing; esc goes back to the options, esc again closes
	m, cmd := press(m, runes("4"))
	if cmd != nil || !m.box.other {
		t.Fatal("4 doesn't open the free text")
	}
	m, _ = press(m, runes("ci"))
	if row := ansi.Strip(m.render()[22]); row != " │ Type something: ci                  │" {
		t.Errorf("typing row %q", row)
	}
	c, sent := screens(t, "claude-ask-q2-checked", "")
	m.c = c
	m2, cmd := press(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	if m2.box.other || !slices.Equal(*sent, []string{"Down", "Down", "Down", "-l -- ci"}) {
		t.Errorf("write in sent %q", *sent)
	}
	// ⏎ on the written-in row goes on, as on any checkbox
	m2.box.hl = 3
	c, sent = screens(t, "claude-ask-q2-checked", "")
	m2.c = c
	m2.answering, m2.box.sent = "", ""
	if _, cmd = press(m2, tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("⏎ on the free-text row: no answer")
	}
	if cmd(); !slices.Equal(*sent, []string{"Down", "Down", "Down", "Down", "Enter"}) {
		t.Errorf("⏎ on the free-text row sent %q", *sent)
	}
	if m, _ = press(m, tea.KeyMsg{Type: tea.KeyEsc}); m.box.other || m.box.session != "a" {
		t.Error("esc in the free text closes the box")
	}
	if m, _ = press(m, tea.KeyMsg{Type: tea.KeyEsc}); m.box.session != "" {
		t.Error("esc doesn't close the picker")
	}
}

func TestPickerPermission(t *testing.T) {
	m := pickerModel(t, "claude-permission-bash", agent.Question{Tool: "Bash", Command: "mkdir -p build/out && touch build/out/x.txt", Description: "Create build/out directory and touch x.txt"})
	m.width = 60
	got := ansiStrip(m.render())[3:10]
	want := []string{
		"┃│    Bash  mkdir -p build/out && touch build/out/x.txt    │",
		"┃│    Create build/out directory and touch x.txt           │",
		"┃│    Do you want to proceed?                              │",
		"┃│    [1 Yes]                                              │",
		"┃│     2 Yes, and don't ask again for mkdir -p build/out a…│",
		"┃│     3 Yes, and switch to auto mode · auto mode handles …│",
		"┃│     4 No                                                │",
	}
	if !slices.Equal(got, want) {
		t.Errorf("permission picker:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A draft typed before the dialog came stays a draft; text mode ⏎ goes to dev agent prompt as before.
func TestPickerKeepsDraft(t *testing.T) {
	m := twoTiles()
	m.snap.tiles[0].deciding = true
	m.selected = "a"
	m, _ = press(m, runes("i"), runes("half"))
	p := goldenPick(t, "claude-ask-q1", colors)
	m.snap.tiles[0].pick = &p
	if m.activePick() != nil {
		t.Error("a draft turned into the picker")
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.activePick() == nil {
		t.Error("an empty draft doesn't show the picker")
	}
}

func ansiStrip(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// The reply box's card is framed in its state's accent, as the selected card is, and so is the box's frame.
func TestBoundTile(t *testing.T) {
	m := Model{width: 61, height: 30, selected: "a", snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "b", Agent: "Claude", State: "waiting"}, deciding: true, body: []string{"\x1b[31mred\x1b[0m"}},
		{sess: sidebar.Session{Name: "c", Agent: "Claude", State: "working"}, body: []string{"x"}},
		{sess: sidebar.Session{Name: "a", Agent: "Claude", State: "idle"}, note: "failed: boom", body: []string{"\x1b[32mgreen\x1b[0m"}},
	}}}
	frame := func(lines []string, name string) string {
		for i, l := range lines {
			if strings.Contains(ansi.Strip(l), "● "+name+" ") {
				return lines[i-1]
			}
		}
		return ""
	}
	plain := m.render()
	if f := frame(plain, "a"); !strings.Contains(f, "\x1b[38;2;152;187;108m╭") {
		t.Errorf("selected a framed in green:\n%q", f)
	}
	if f := frame(plain, "b"); !strings.Contains(f, rule+"╭") {
		t.Errorf("b framed faint:\n%q", f)
	}
	m.open(m.snap.tiles[0])
	got := m.render()
	if f := frame(got, "b"); !strings.Contains(f, red+"╭") {
		t.Errorf("b's card in its accent:\n%q", f)
	}
	if f := frame(got, "a"); !strings.Contains(f, rule+"╭") {
		t.Errorf("a no longer selected:\n%q", f)
	}
	if gh := m.boardHeight(); !strings.HasPrefix(got[gh], " "+red+"╭─"+reset) {
		t.Errorf("box frame in b's accent: %q", got[gh])
	}
}

// A deciding tile's capture gives its picker, told apart by its @agent_question.
func TestRefreshPick(t *testing.T) {
	var q strings.Builder
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			r := tmux.FormatRow
			return r("herd", "$1", "1", "1", "1", "%1", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "") +
				r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "waiting", "claude", "", "", "", "", "$1", "permission_prompt", "", "", "", q.String(), "", "", "", ""), nil
		case "list-clients":
			return tmux.FormatRow("/dev/ttys1", "herd", "5"), nil
		}
		return golden(t, "claude-ask-q1"), nil
	})
	rc := tmux.NewFake(func(args []string) (string, error) { q.WriteString(args[5]); return "", nil })
	if err := agent.RecordQuestion(rc, "%3", colors); err != nil {
		t.Fatal(err)
	}
	m := Model{c: c, pane: "%1", look: &fakeLookups{}, width: 80, height: 40}
	msg := m.refresh()().(refreshMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	tl := msg.snap.tiles[0]
	if !tl.deciding || tl.pick == nil || tl.pick.cur != 0 || tl.pick.d.title != "Which color should the theme use?" {
		t.Fatalf("tile %+v", tl)
	}
	// Its card shows the dialog: its screen stops above it
	body := strings.Join(ansiStrip(tl.body), "\n")
	if len(tl.body) == 0 || strings.Contains(body, "1. Red") || strings.Contains(body, "Chat about this") || strings.Contains(body, "Extras  ✔") {
		t.Errorf("screen not cut above the dialog:\n%s", body)
	}
}

func TestReplyEditing(t *testing.T) {
	m := twoTiles()
	m.width, m.height = 60, 20
	alt := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	m, _ = press(m, runes("i"), runes("one"), alt, runes("two"), tea.KeyMsg{Type: tea.KeyCtrlJ}, runes("x"), tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.box.ed.String() != "one\ntwo" {
		t.Fatalf("draft %q", m.box.ed.String())
	}
	if got := ansiStrip(m.bottom()); len(got) != 4 || got[1] != " │ one"+strings.Repeat(" ", 53)+"│" || got[2] != " │ two "+strings.Repeat(" ", 52)+"│" {
		t.Errorf("a two-line draft grows the box: %q", got)
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	m.history = append(m.history, "older")
	m, _ = press(m, runes("i"))
	if got := ansi.Strip(m.bottom()[1]); got != " │  type a message"+strings.Repeat(" ", 41)+"│" {
		t.Errorf("placeholder %q", got)
	}
	// ↑ recalls the newest, then older; ↓ comes back, then to an empty draft
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.box.ed.String() != "older" {
		t.Errorf("↑ recalled %q", m.box.ed.String())
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.box.ed.String() != "one\ntwo" {
		t.Errorf("↑↑ recalled %q", m.box.ed.String())
	}
	// Within a recalled multi-line reply ↑ moves up a line first
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyUp}, runes("A"))
	if m.box.ed.String() != "oneA\ntwo" || m.box.hist != -1 {
		t.Errorf("edit after ↑: %q hist %d", m.box.ed.String(), m.box.hist)
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyCtrlU}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnd}, tea.KeyMsg{Type: tea.KeyCtrlU}, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.box.ed.String() != "" {
		t.Fatalf("cleared %q", m.box.ed.String())
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyDown})
	if m.box.ed.String() != "" || m.box.hist != -1 {
		t.Errorf("↓ past the newest: %q hist %d", m.box.ed.String(), m.box.hist)
	}
	// A typed draft isn't replaced by ↑
	m, _ = press(m, runes("keep"), tea.KeyMsg{Type: tea.KeyUp})
	if m.box.ed.String() != "keep" {
		t.Errorf("↑ over a draft: %q", m.box.ed.String())
	}
}

// A narrow card wraps its options, each chip cut to the card.
func TestPickerWraps(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m.width = 24
	got := ansiStrip(m.render())
	if got[5] != "┃│    [1 Red]  2 Green │" || got[6] != "┃│     3 Blue          │" || got[7] != "┃│     4 Type somethin…│" {
		t.Errorf("wrapped options:\n%s", strings.Join(got[:12], "\n"))
	}
}

// cellOf is where text shows on m's screen: its column and row.
func cellOf(t *testing.T, m Model, text string) (int, int) {
	t.Helper()
	for y, l := range m.render() {
		if i := strings.Index(ansi.Strip(l), text); i >= 0 {
			return ansi.StringWidth(ansi.Strip(l)[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, strings.Join(ansiStrip(m.render()), "\n"))
	return 0, 0
}

func clickAt(m Model, x, y int) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return next.(Model), cmd
}

// A click on an option answers through the picker, as its key does, opening the card's box first; on a
// multi-select's option it toggles it, on Submit it goes on. A click while an answer is out, or over a typed
// reply, sends nothing.
func TestCardAnswerClicks(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m.box = replyBox{}
	c, sent := screens(t, "claude-ask-q1", "")
	m.c = c
	x, y := cellOf(t, m, "2 Green")
	m, cmd := clickAt(m, x+2, y)
	if cmd == nil || m.box.session != "a" || m.box.hl != 1 || m.answering != "a" {
		t.Fatalf("click on Green: box %+v answering %q", m.box, m.answering)
	}
	if cmd(); !slices.Equal(*sent, []string{"-l 2"}) {
		t.Errorf("click on Green sent %q", *sent)
	}
	if _, cmd := clickAt(m, x+2, y); cmd != nil {
		t.Error("a click while the answer is out sends again")
	}

	m = pickerModel(t, "claude-ask-q2-checked", colors)
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"2 ☐ Docs", []string{"-l 2"}},
		{"Submit ⏎", []string{"Down", "Down", "Down", "Down", "Enter"}},
	} {
		c, sent := screens(t, "claude-ask-q2-checked", "")
		m.c = c
		x, y := cellOf(t, m, tt.text)
		_, cmd := clickAt(m, x+1, y)
		if cmd == nil {
			t.Fatalf("click on %s: no answer", tt.text)
		}
		if cmd(); !slices.Equal(*sent, tt.want) {
			t.Errorf("click on %s sent %q, want %q", tt.text, *sent, tt.want)
		}
	}

	m = pickerModel(t, "claude-ask-q1", colors)
	m.box.ed.set("half")
	x, y = cellOf(t, m, "3 Blue")
	if m, cmd = clickAt(m, x+1, y); cmd != nil || !strings.Contains(m.msg.text, "typed reply") || m.box.ed.String() != "half" {
		t.Errorf("click over a draft: msg %q draft %q", m.msg.text, m.box.ed.String())
	}
}

// A card's buttons: Open switches to the worker, Message… opens its box, its PR opens it, × asks to close it
// (closing the box, so the y isn't typed into it).
func TestCardButtons(t *testing.T) {
	m := board4()
	var ran []string
	m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
	look := &fakeLookups{}
	m.look = look
	m.snap.tiles[1].sess.PR.URL = "https://example.invalid/95"
	row := func(m Model, name string) int {
		_, y := cellOf(t, m, "● "+name)
		for ; y < m.height; y++ {
			if strings.Contains(ansi.Strip(m.render()[y]), "Message…") {
				return y
			}
		}
		t.Fatalf("no buttons under %s", name)
		return 0
	}
	y := row(m, "docs-api")
	at := func(text string) int {
		i := strings.Index(ansi.Strip(m.render()[y]), text)
		return ansi.StringWidth(ansi.Strip(m.render()[y])[:i])
	}
	if _, cmd := clickAt(m, at("Open")+1, y); cmd == nil {
		t.Error("Open: no switch")
	} else if cmd(); len(ran) != 1 || !strings.HasPrefix(ran[0], "switch-client -t =docs-api:") {
		t.Errorf("Open ran %q", ran)
	}
	if got, _ := clickAt(m, at("Message…")+1, y); got.box.session != "docs-api" {
		t.Errorf("Message… opened %q", got.box.session)
	}
	if _, cmd := clickAt(m, at("PR #95")+1, y); cmd == nil {
		t.Error("PR: nothing opened")
	} else if msg := cmd().(actionMsg); !strings.Contains(msg.msg.text, "no browser") {
		t.Errorf("PR: %q", msg.msg.text)
	}
	m.open(m.snap.tiles[1])
	m.box.ed.set("draft")
	y = row(m, "docs-api")
	got, _ := clickAt(m, at("×"), y)
	if got.confirm != "docs-api" || got.box.session != "" {
		t.Errorf("×: confirm %q box %q", got.confirm, got.box.session)
	}
}

// An answer taken for done while the screen still shows its dialog lets the picker back after a few refreshes.
func TestPickerSelfHeals(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	p := m.snap.tiles[0].pick
	m.box.sent = p.d.key()
	for range staleRefreshes {
		m.syncPick()
	}
	if !m.waiting(p) {
		t.Fatal("the picker stopped waiting too soon")
	}
	if m.syncPick(); m.waiting(p) {
		t.Error("the picker still waits")
	}
}

// The worker's cursor on an option under the Submit row ("Chat about this") is a row of its own.
func TestCursorUnderSubmit(t *testing.T) {
	capture := strings.Replace(strings.Replace(golden(t, "claude-ask-q2-checked"), "❯ 1. [✔] Tests", "  1. [✔] Tests", 1), "  5. Chat about this", "❯ 5. Chat about this", 1)
	d, ok := parseDialog(capture)
	if !ok || d.cursor != 5 || d.option(d.cursor) != 4 || d.option(d.submitAt) != -1 || d.row(3) != 3 || d.row(4) != 5 {
		t.Fatalf("cursor %d submitAt %d", d.cursor, d.submitAt)
	}
	for _, tt := range []struct {
		a    answer
		want []string
	}{
		{submit(), []string{"up", "enter"}},
		{writeInMulti(3, "ci"), []string{"up", "up", ""}},
	} {
		keys, err := tt.a.keys(d)
		var got []string
		for _, k := range keys {
			got = append(got, k.key)
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("keys %q %v, want %q", got, err, tt.want)
		}
	}
	m := pickerModel(t, "claude-ask-q1", colors)
	p := newPick(d, colors, "")
	m.snap.tiles[0].pick = &p
	if m.syncPick(); m.box.hl != 4 {
		t.Errorf("picker starts on the worker's cursor: hl %d", m.box.hl)
	}
}

// A dialog that changed under a draft gets its own highlight: emptied, the draft shows its picker at once.
func TestPickerUnderDraft(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyUp}) // hl 4, Chat about this
	m.snap.tiles[0].pick = nil
	m.syncPick()
	m, _ = press(m, runes("x"))
	p := goldenPick(t, "claude-permission-write", agent.Question{})
	m.snap.tiles[0].pick = &p
	m.syncPick()
	m.c, _ = screens(t, "claude-permission-write", "")
	m, cmd := press(m, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.box.hl != 0 || cmd == nil {
		t.Errorf("hl %d, cmd %v", m.box.hl, cmd != nil)
	}
}

// One answer at a time; another worker's late outcome doesn't touch this box's wait.
func TestAnswerOutcomes(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m.answering = "b"
	if _, cmd := press(m, runes("1")); cmd != nil {
		t.Error("answered while another answer's keys go out")
	}
	m.answering, m.box.sent = "", m.snap.tiles[0].pick.d.key()
	next, _ := m.Update(answeredMsg{session: "b", msg: alert("❌ b: nope")})
	if m = next.(Model); m.box.sent == "" || m.msg.text != "❌ b: nope" {
		t.Errorf("b's outcome: sent %q msg %q", m.box.sent, m.msg.text)
	}
	// The screen still shows the answered dialog after staleRefreshes: it's back, and said so
	for range staleRefreshes + 1 {
		m.syncPick()
	}
	if m.box.sent != "" || !strings.Contains(m.msg.text, "the answer didn't take") {
		t.Errorf("stale: sent %q msg %q", m.box.sent, m.msg.text)
	}
}

// Free text being typed survives a capture that missed the dialog; with the dialog gone, nothing is sent.
func TestWriteInSurvivesMissedCapture(t *testing.T) {
	m := pickerModel(t, "claude-ask-single", agent.Question{})
	m, _ = press(m, runes("3"), runes("teal"))
	p := m.snap.tiles[0].pick
	m.snap.tiles[0].pick = nil
	m.syncPick()
	if !m.box.other || m.box.ed.String() != "teal" {
		t.Fatalf("missed capture: other %v text %q", m.box.other, m.box.ed.String())
	}
	if m2, cmd := press(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || !strings.Contains(m2.msg.text, "dialog is gone: nothing sent") {
		t.Errorf("enter without the dialog: msg %q", m2.msg.text)
	}
	m.snap.tiles[0].pick = p
	m.syncPick()
	m.c, _ = screens(t, "claude-ask-single", "")
	if _, cmd := press(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Error("the dialog back: no answer")
	}
	// Another question drops the text typed for this one
	q := goldenPick(t, "claude-ask-q1", agent.Question{})
	m.snap.tiles[0].pick = &q
	if m.syncPick(); m.box.other || m.box.ed.String() != "" {
		t.Errorf("another question: other %v text %q", m.box.other, m.box.ed.String())
	}
}

func TestDigitPastNine(t *testing.T) {
	if _, err := digit(option{num: 10}); err == nil {
		t.Error("option 10 got a key")
	}
}

// A permission's hook data shows only for the command on screen, all of it, not just its start.
func TestPermissionNeedle(t *testing.T) {
	screen := "Bash command\n  cd /Users/me/project/very/long/path && rm -rf\n  build\nDo you want to proceed?\n"
	d := dialog{title: "Do you want to proceed?", opts: []option{{num: 1, label: "Yes"}}}
	for _, tt := range []struct {
		q    agent.Question
		want bool
	}{
		{agent.Question{Tool: "Bash", Command: "cd /Users/me/project/very/long/path && rm -rf build"}, true},
		{agent.Question{Tool: "Bash", Command: "cd /Users/me/project/very/long/path && ls"}, false},
		{agent.Question{Tool: "WebSearch"}, false},
		{agent.Question{Tool: "Bash"}, true},
	} {
		if got := newPick(d, tt.q, screen).tool != ""; got != tt.want {
			t.Errorf("%+v shown %v", tt.q, got)
		}
	}
}

// Two permission prompts alike but for their command are two dialogs: an answer drawn for one sends
// nothing to the other.
func TestSendRefusesAnotherCommand(t *testing.T) {
	shown := goldenPick(t, "claude-permission-bash", agent.Question{})
	other := strings.ReplaceAll(golden(t, "claude-permission-bash"), "&& touch build/out/x.txt", "&& touch build/out/y.txt")
	var sent []string
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			return other, nil
		}
		sent = append(sent, strings.Join(args, " "))
		return "", nil
	})
	msg := Model{c: c, clk: fakeClock()}.send("w", "%5", shown.d, choose(0))().(answeredMsg)
	if len(sent) != 0 || !strings.Contains(msg.msg.text, "dialog changed: nothing sent") {
		t.Errorf("sent %q: %+v", sent, msg)
	}
}

// A pane shorter than the box keeps the box's bottom rows.
func TestBoxInShortPane(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	for _, h := range []int{1, 2, 3} {
		m.height = h
		if got := m.render(); len(got) != h || !strings.HasPrefix(ansi.Strip(got[h-1]), " ╰─ ←→ move") {
			t.Errorf("height %d: %q", h, got)
		}
	}
}

// 1-9 on a waiting tile go through the picker; space and pasted digits never answer by surprise.
func TestPickerGuards(t *testing.T) {
	m := twoTiles()
	m.height, m.clk, m.selected = 24, fakeClock(), "a"
	m.snap.tiles[0].pane, m.snap.tiles[0].deciding = "%5", true
	p := goldenPick(t, "claude-ask-q1", colors)
	m.snap.tiles[0].pick = &p
	m.c, _ = screens(t, "claude-ask-q1", "")
	m, cmd := press(m, runes("2"))
	if cmd == nil || m.box.session != "a" || m.answering != "a" {
		t.Errorf("2 on a waiting tile: box %q answering %q", m.box.session, m.answering)
	}
	m = pickerModel(t, "claude-ask-q2-checked", colors)
	m.box.hl = 4 // Chat about this
	if _, cmd := press(m, tea.KeyMsg{Type: tea.KeySpace}); cmd != nil {
		t.Error("space on Chat about this answers")
	}
	if _, cmd := press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2"), Paste: true}); cmd != nil {
		t.Error("a pasted digit answers")
	}
}

// A click on another card's option goes to that card's worker, its box moving there and keeping the reply
// typed in the box it left; a click while an answer is out moves nothing and says so; × keeps a typed reply
// too.
func TestCardClicksAcrossCards(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m.height = 40
	b := goldenPick(t, "claude-permission-bash", agent.Question{})
	m.snap.tiles[1].deciding, m.snap.tiles[1].pick, m.snap.tiles[1].pane = true, &b, "%6"
	m.snap.tiles[1].sess.State = agent.Waiting
	sortBySection(m.snap.tiles)
	m.box.ed.set("half")
	var sent []string
	m.c = tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			return golden(t, "claude-permission-bash"), nil
		}
		sent = append(sent, strings.Join(args, " "))
		return "", nil
	})
	x, y := cellOf(t, m, "4 No")
	m, cmd := clickAt(m, x+1, y)
	if cmd == nil || m.box.session != "b" || m.drafts["a"].ed.String() != "half" {
		t.Fatalf("click on b's No: box %q drafts %v", m.box.session, m.drafts)
	}
	if cmd(); len(sent) != 1 || !strings.Contains(sent[0], "-t %6 ") || !strings.HasSuffix(sent[0], "-l 4") {
		t.Errorf("sent %q, want 4 to b's pane %%6", sent)
	}
	x, y = cellOf(t, m, "2 Green")
	if got, cmd := clickAt(m, x+1, y); cmd != nil || got.box.session != "b" || !strings.Contains(got.msg.text, "answering b") {
		t.Errorf("click while b's answer is out: box %q msg %q", got.box.session, got.msg.text)
	}
	m.answering, m.box.sent = "", ""
	m.box.ed.set("later")
	x, y = cellOf(t, m, "×")
	if got, _ := clickAt(m, x, y); got.confirm == "" || got.box.session != "" || got.drafts["b"].ed.String() != "later" {
		t.Errorf("×: confirm %q box %q drafts %v", got.confirm, got.box.session, got.drafts)
	}
}

// Without a box a card brackets its dialog's own cursor; while any answer is out every card's options dim.
func TestCardDialogCursor(t *testing.T) {
	m := pickerModel(t, "claude-ask-multi-other-typed", agent.Question{})
	m.box = replyBox{}
	if x, y := cellOf(t, m, "[3 ☑ a fish]"); x == 0 || y == 0 {
		t.Error("the dialog's cursor isn't bracketed")
	}
	m.answering = "b"
	if strings.Contains(ansi.Strip(strings.Join(m.render(), "\n")), "[") {
		t.Error("options not dimmed while an answer is out")
	}
}

// A click on the free-text option opens it for typing in the box; one on it again keeps the text, one on
// another option drops it and picks that one.
func TestCardFreeTextClick(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	x, y := cellOf(t, m, "4 Type something.")
	m, cmd := clickAt(m, x+1, y)
	if cmd != nil || !m.box.other {
		t.Fatal("click on Type something doesn't open it")
	}
	m, _ = press(m, runes("teal"))
	if m, cmd = clickAt(m, x+1, y); cmd != nil || m.box.ed.String() != "teal" {
		t.Errorf("click on it again: text %q", m.box.ed.String())
	}
	c, sent := screens(t, "claude-ask-q1", "")
	m.c = c
	x, y = cellOf(t, m, "3 Blue")
	m, cmd = clickAt(m, x+1, y)
	if cmd == nil || m.box.other || m.box.ed.String() != "" {
		t.Fatalf("click on Blue: other %v text %q", m.box.other, m.box.ed.String())
	}
	if cmd(); !slices.Equal(*sent, []string{"-l 3"}) {
		t.Errorf("sent %q", *sent)
	}
}

// In a short pane the board scrolls to keep the box's highlighted option in view.
func TestPickerScrolls(t *testing.T) {
	m := pickerModel(t, "claude-permission-bash", agent.Question{})
	m.width, m.height = 60, 10
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyRight})
	if got := strings.Join(ansiStrip(m.render()), "\n"); !strings.Contains(got, "[4 No]") {
		t.Errorf("the highlighted No is out of view:\n%s", got)
	}
}

// A working tile whose AskUserQuestion hook recorded the question on screen is a decision before its
// Notification makes it one; a recorded question not on screen (answered, its turn going on) isn't; no case
// reads its state again.
func TestRefreshAsking(t *testing.T) {
	other := agent.Question{Tool: agent.AskUserQuestion, Questions: []agent.Ask{{Question: "Other?", Header: "X"}}}
	tests := []struct {
		name   string
		q      agent.Question
		screen string
		asks   bool
	}{
		{"its question on screen", colors, golden(t, "claude-ask-q1"), true},
		{"its second question on screen", colors, golden(t, "claude-ask-q2-checked"), true},
		{"its answers' review on screen", colors, golden(t, "claude-ask-review"), true},
		{"another question on screen", other, golden(t, "claude-ask-q1"), false},
		{"no dialog", colors, "⏺ Picked Red.\n\n✻ Working…\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var q strings.Builder
			rc := tmux.NewFake(func(args []string) (string, error) { q.WriteString(args[5]); return "", nil })
			if err := agent.RecordQuestion(rc, "%3", tt.q); err != nil {
				t.Fatal(err)
			}
			var ran []string
			c := tmux.NewFake(func(args []string) (string, error) {
				ran = append(ran, args[0])
				switch args[0] {
				case "list-panes":
					r := tmux.FormatRow
					return r("herd", "$1", "1", "1", "1", "%1", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "") +
						r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "working", "claude", "", "", "", "", "$1", "", "", "", "", q.String(), "", "", "", ""), nil
				case "list-clients":
					return tmux.FormatRow("/dev/ttys1", "herd", "5"), nil
				}
				return tt.screen, nil
			})
			m := Model{c: c, pane: "%1", look: &fakeLookups{}, width: 80, height: 40}
			msg := m.refresh()().(refreshMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			tl := msg.snap.tiles[0]
			if tl.deciding != tt.asks || (tl.pick != nil) != tt.asks || (tl.section() == needsYouSection) != tt.asks {
				t.Errorf("deciding %v, pick %v, section %d", tl.deciding, tl.pick != nil, tl.section())
			}
			if want := map[bool]string{true: agent.Waiting, false: agent.Working}[tt.asks]; tl.sess.State != want {
				t.Errorf("state %q, want %q", tl.sess.State, want)
			}
			if slices.Contains(ran, "display-message") || slices.Contains(ran, "if-shell") {
				t.Errorf("state read again: %q", ran)
			}
		})
	}
}
