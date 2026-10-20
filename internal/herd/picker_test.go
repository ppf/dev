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
			if msg.msg != tt.msg || msg.took != strings.HasPrefix(tt.msg, "Answered") || !slices.Equal(*sent, tt.keys) {
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
	box := strings.Join(ansiStrip(m.render()[24-len(m.bottom()):]), "\n")
	want := strings.Join([]string{
		"─ ● a Claude · waiting · $1.20 ─────────",
		"▎ Color · Extras · Submit",
		"▎ Which color should the theme use?",
		"▎ ❯ 1. Red              Warm and loud",
		"▎   2. Green            Calm",
		"▎   3. Blue             Cool and safe",
		"▎   4. Type something.",
		"▎   5. Chat about this",
		" ↑↓ move · ⏎ pick · 1-9 · esc close",
	}, "\n")
	if box != want {
		t.Errorf("picker:\n%s\nwant:\n%s", box, want)
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
	next, _ := m.Update(answeredMsg{session: "a", msg: "Answered a", took: true})
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
	next, _ = m.Update(answeredMsg{session: "a", msg: "❌ a: nope"})
	if m = next.(Model); m.waiting(&p) || !strings.Contains(ansi.Strip(m.render()[23]), "❌ a: nope") {
		t.Errorf("failed answer: %+v", m.box)
	}
}

func TestPickerMulti(t *testing.T) {
	m := pickerModel(t, "claude-ask-q2-checked", colors)
	got := ansiStrip(m.render()[24-len(m.bottom()):])
	if got[3] != "▎ ❯ 1. ☑ Tests            Add unit tests" || got[4] != "▎   2. ☐ Docs             Write README" || got[len(got)-1] != " ↑↓ move · space toggle · ⏎ next · 1-9 …" {
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
	if row := ansi.Strip(m.render()[24-len(m.bottom())+6]); row != "▎ ❯ 4. ☐ ci " {
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
	got := ansiStrip(m.render()[24-len(m.bottom()):])
	want := []string{
		"─ ● a Claude · waiting · $1.20 ─────────────────────────────",
		"▎ Bash  mkdir -p build/out && touch build/out/x.txt",
		"▎ Create build/out directory and touch x.txt",
		"▎ Do you want to proceed?",
		"▎ ❯ 1. Yes",
		"▎   2. Yes, and don't ask again for mkdir -p build/out and …",
		"▎   3. Yes, and switch to auto mode · auto mode handles the…",
		"▎   4. No",
		" ↑↓ move · ⏎ pick · 1-9 · esc close",
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

// Behind an open reply box its tile is framed in its accent (header background, the rules beside and
// under it) and the others are dimmed; a selected tile without the box keeps the selection background.
func TestBoundTile(t *testing.T) {
	m := Model{width: 61, height: 14, selected: "b", snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "a", Agent: "Claude", State: "idle"}, body: []string{"\x1b[32mgreen\x1b[0m"}},
		{sess: sidebar.Session{Name: "b", Agent: "Claude", State: "waiting"}, body: []string{"\x1b[31mred\x1b[0m"}},
		{sess: sidebar.Session{Name: "c", Agent: "Claude", State: "working"}, body: []string{"x"}},
	}}}
	const redBG = "\x1b[48;2;228;104;118m"
	plain := m.render()
	if !strings.HasPrefix(plain[0], "\x1b[48;2;30;30;38m") || !strings.Contains(plain[0], "\x1b[48;2;42;42;55m") || strings.Contains(strings.Join(plain, ""), redBG) {
		t.Errorf("no box: a plain, b selected, no accent:\n%q", plain[0])
	}
	m.open(m.snap.tiles[1])
	got := m.render()
	// 3 tiles of 30 columns: a b on top, c under a
	if !strings.Contains(got[0], red+"│"+reset+redBG+ink+bold+" ● b Claude · waiting") {
		t.Errorf("b's header on its accent, the rule left of it red:\n%q", got[0])
	}
	if !strings.HasPrefix(got[0], headerBG+rule+" ● a") || !strings.Contains(got[1], rule+"green") {
		t.Errorf("a dimmed:\n%q\n%q", got[0], got[1])
	}
	gh := m.gridHeight()
	g := layout(3, 61, gh)
	div := got[g.tileH]
	if !strings.Contains(div, red+"┴"+red+strings.Repeat("─", 30)) || !strings.HasPrefix(div, rule+strings.Repeat("─", 30)) {
		t.Errorf("divider under b red, under a faint:\n%q", div)
	}
	if !strings.HasPrefix(got[gh], red+"─"+reset) {
		t.Errorf("box rule in b's accent: %q", got[gh])
	}
}

// A deciding tile's capture gives its picker, told apart by its @agent_question.
func TestRefreshPick(t *testing.T) {
	var q strings.Builder
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			r := tmux.FormatRow
			return r("herd", "$1", "1", "1", "1", "%1", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "") +
				r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "waiting", "claude", "", "", "", "", "$1", "permission_prompt", "", "", "", q.String(), ""), nil
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
}

func TestReplyEditing(t *testing.T) {
	m := twoTiles()
	m.width, m.height = 60, 20
	alt := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	m, _ = press(m, runes("i"), runes("one"), alt, runes("two"), tea.KeyMsg{Type: tea.KeyCtrlJ}, runes("x"), tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.box.ed.String() != "one\ntwo" {
		t.Fatalf("draft %q", m.box.ed.String())
	}
	if got := ansiStrip(m.bottom()); len(got) != 3 || got[1] != "▎ one" || got[2] != "▎ two " {
		t.Errorf("a two-line draft grows the box: %q", got)
	}
	m, _ = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	m.history = append(m.history, "older")
	m, _ = press(m, runes("i"))
	if got := ansi.Strip(m.bottom()[1]); got != "▎  ⏎ send · esc cancel · ⇧⏎ newline · ↑ recent" {
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

// A short pane scrolls the options to keep the highlighted one, dropping the head first.
func TestPickerScrolls(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	m.height = 10 // room for 3 rows
	m.box.hl = 4
	got := ansiStrip(m.bottom())
	if len(got) != 5 || got[1] != "▎   3. Blue             Cool and safe" || got[3] != "▎ ❯ 5. Chat about this" {
		t.Errorf("scrolled picker:\n%s", strings.Join(got, "\n"))
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
	next, _ := m.Update(answeredMsg{session: "b", msg: "❌ b: nope"})
	if m = next.(Model); m.box.sent == "" || m.msg != "❌ b: nope" {
		t.Errorf("b's outcome: sent %q msg %q", m.box.sent, m.msg)
	}
	// The screen still shows the answered dialog after staleRefreshes: it's back, and said so
	for range staleRefreshes + 1 {
		m.syncPick()
	}
	if m.box.sent != "" || !strings.Contains(m.msg, "the answer didn't take") {
		t.Errorf("stale: sent %q msg %q", m.box.sent, m.msg)
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
	if m2, cmd := press(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || !strings.Contains(m2.msg, "dialog is gone: nothing sent") {
		t.Errorf("enter without the dialog: msg %q", m2.msg)
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
	if len(sent) != 0 || !strings.Contains(msg.msg, "dialog changed: nothing sent") {
		t.Errorf("sent %q: %+v", sent, msg)
	}
}

// A pane shorter than the box keeps the box's bottom rows.
func TestBoxInShortPane(t *testing.T) {
	m := pickerModel(t, "claude-ask-q1", colors)
	for _, h := range []int{1, 2, 3} {
		m.height = h
		if got := m.render(); len(got) != h || !strings.Contains(ansi.Strip(got[h-1]), "esc close") {
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
