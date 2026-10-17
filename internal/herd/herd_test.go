package herd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func TestLayout(t *testing.T) {
	tests := []struct {
		n, w, h int
		want    grid
	}{
		{0, 100, 40, grid{}},
		{1, 100, 40, grid{cols: 1, rows: 1, tileW: 100, tileH: 40, perPage: 1}},
		{2, 100, 40, grid{cols: 2, rows: 1, tileW: 49, tileH: 40, perPage: 2}},
		{2, 60, 40, grid{cols: 1, rows: 2, tileW: 60, tileH: 19, perPage: 2}},
		{4, 100, 40, grid{cols: 2, rows: 2, tileW: 49, tileH: 19, perPage: 4}},
		{5, 100, 40, grid{cols: 3, rows: 2, tileW: 32, tileH: 19, perPage: 5}},
		// Too short for 4 rows of 5: paged
		{8, 40, 17, grid{cols: 1, rows: 3, tileW: 40, tileH: 5, perPage: 3}},
		// Capped at maxTiles
		{20, 400, 100, grid{cols: 6, rows: 2, tileW: 65, tileH: 49, perPage: 12}},
	}
	for _, tt := range tests {
		if got := layout(tt.n, tt.w, tt.h); got != tt.want {
			t.Errorf("layout(%d, %d, %d) = %+v, want %+v", tt.n, tt.w, tt.h, got, tt.want)
		}
	}
	g := grid{perPage: 3}
	for i, want := range [][2]int{{0, 3}, {0, 3}, {0, 3}, {3, 6}, {6, 9}} {
		if from, to := g.page([]int{0, 1, 2, 3, 7}[i]); from != want[0] || to != want[1] {
			t.Errorf("page %d = %d,%d, want %v", i, from, to, want)
		}
	}
}

func TestLastLines(t *testing.T) {
	capture := "one\n\x1b]8;;http://x\x1b\\two\x1b]8;;\x1b\\\nthree\n  \n\x1b[0m\n"
	if got := lastLines(capture, 2); !slices.Equal(got, []string{"two", "three"}) {
		t.Errorf("lastLines = %q", got)
	}
	if got := lastLines(capture, 0); len(got) != 0 {
		t.Errorf("lastLines 0 = %q", got)
	}
}

const (
	red   = "\x1b[38;2;228;104;118m"
	muted = "\x1b[38;2;114;113;105m"
	reset = "\x1b[0m"
)

func twoTiles() Model {
	return Model{width: 40, height: 12, selected: "b", snap: snapshot{self: "$1", tiles: []tile{
		{sess: sidebar.Session{Name: "a", Agent: "Claude", State: "waiting", Cost: "$1.20"}, body: []string{"\x1b[31mred line that is far too long to fit\x1b[0m"}},
		{sess: sidebar.Session{Name: "b", Agent: "Codex", Branch: "fix", PR: sidebar.PR{Number: 3, State: "open"}}, body: []string{"x"}},
	}}}
}

// Golden: two stacked 40×5 tiles; the waiting one in red, the selected one on the selection background,
// a body line cropped with its colour reset.
func TestRenderGolden(t *testing.T) {
	want := []string{
		" " + red + "●" + reset + " \x1b[1m" + red + "a" + reset + " " + red + "Claude · waiting · $1.20" + reset + "           ",
		reset + "\x1b[31mred line that is far too long to fit\x1b[0m" + reset + "    ",
		strings.Repeat(" ", 40),
		strings.Repeat(" ", 40),
		strings.Repeat(" ", 40),
		"",
		"\x1b[48;2;42;42;55m " + muted + "●" + reset + "\x1b[48;2;42;42;55m \x1b[1m" + muted + "b" + reset + "\x1b[48;2;42;42;55m " + muted + "Codex" + reset + "\x1b[48;2;42;42;55m \x1b[38;2;152;187;108m#3 open" + reset + "\x1b[48;2;42;42;55m \x1b[38;2;84;84;109mfix" + reset + "\x1b[48;2;42;42;55m                  " + reset,
		reset + "x" + reset + strings.Repeat(" ", 39),
		strings.Repeat(" ", 40),
		strings.Repeat(" ", 40),
		strings.Repeat(" ", 40),
		" \x1b[38;2;84;84;109mj/k ⏎ switch  i reply  o PR  x kill" + reset,
	}
	got := twoTiles().render()
	for i := range max(len(got), len(want)) {
		if i >= len(got) || i >= len(want) || got[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, at(got, i), at(want, i))
		}
	}
	for i, l := range got[:11] {
		if l != "" && ansi.StringWidth(l) != 40 {
			t.Errorf("line %d is %d wide", i, ansi.StringWidth(l))
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}

func TestRenderSideBySideAndPaging(t *testing.T) {
	m := twoTiles()
	m.width = 80
	got := m.render()
	if !strings.Contains(got[0], "a") || !strings.Contains(ansi.Strip(got[0]), " b ") || ansi.StringWidth(got[0]) != 79 {
		t.Errorf("side by side header %q", ansi.Strip(got[0]))
	}
	m = twoTiles()
	m.width, m.height = 40, 7 // room for one tile: paged
	got = m.render()
	if s := ansi.Strip(got[0]); !strings.HasPrefix(s, " ● b ") {
		t.Errorf("page of the selection shows %q", s)
	}
	if s := ansi.Strip(got[6]); !strings.HasPrefix(s, " 2/2  ") {
		t.Errorf("bottom row %q", s)
	}
	m.snap.tiles = nil
	if s := ansi.Strip(m.render()[0]); !strings.Contains(s, "No worker sessions") {
		t.Errorf("empty state %q", s)
	}
}

func TestSelectionAndClicks(t *testing.T) {
	m := twoTiles()
	key := func(m Model, k string) Model {
		var msg tea.KeyMsg
		switch k {
		case "down", "up", "enter":
			msg = tea.KeyMsg{Type: map[string]tea.KeyType{"down": tea.KeyDown, "up": tea.KeyUp, "enter": tea.KeyEnter}[k]}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.Update(msg)
		return next.(Model)
	}
	if m = key(m, "j"); m.selected != "a" {
		t.Errorf("j wraps to a, got %q", m.selected)
	}
	if m = key(m, "up"); m.selected != "b" {
		t.Errorf("up wraps to b, got %q", m.selected)
	}
	click := func(m Model, x, y int) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return next.(Model), cmd
	}
	var ran []string
	m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
	m.snap.tiles[0].pane, m.snap.tiles[1].pane = "%5", "%6"
	// A click answers in place: it selects the tile and opens the reply box for it, never switching
	m, cmd := click(m, 3, 1)
	if m.selected != "a" || m.reply != "a" || m.replyPane != "%5" || cmd != nil || len(ran) != 0 {
		t.Errorf("click on tile a: selected %q reply %q pane %q, ran %q", m.selected, m.reply, m.replyPane, ran)
	}
	if _, cmd := click(m, 3, 5); cmd != nil {
		t.Error("click on the gap row does something")
	}
	m.draft = []rune("half")
	if m, _ = click(m, 3, 1); string(m.draft) != "half" {
		t.Errorf("click on the same tile drops the draft: %q", string(m.draft))
	}
	if m, _ = click(m, 3, 7); m.reply != "b" || m.replyPane != "%6" || len(m.draft) != 0 {
		t.Errorf("click on tile b: reply %q pane %q draft %q", m.reply, m.replyPane, string(m.draft))
	}
	if len(ran) != 0 {
		t.Errorf("clicks ran %q", ran)
	}
}

func TestKillGuard(t *testing.T) {
	var ran []string
	m := twoTiles()
	m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
	press := func(m Model, k string) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model), cmd
	}
	m, _ = press(m, "x")
	if m.confirm != "" || !strings.Contains(m.msg, "wasn't started from this boss") {
		t.Errorf("x on a foreign worker: confirm %q msg %q", m.confirm, m.msg)
	}
	m.snap.name = "boss"
	m.snap.tiles[1].startedBy = "boss-web"
	if m, _ = press(m, "x"); m.confirm != "" {
		t.Error("x on another boss's worker asks to kill it")
	}
	m.snap.tiles[1].startedBy = "boss"
	if m, _ = press(m, "x"); m.confirm != "b" {
		t.Errorf("x on a worker naming this boss: confirm %q", m.confirm)
	}
	m.confirm = ""
	m.snap.tiles[1].startedBy = "$1" // as older versions wrote
	m, _ = press(m, "x")
	if m.confirm != "b" || !strings.Contains(ansi.Strip(m.render()[11]), "Kill b? y/n") {
		t.Fatalf("x asks: confirm %q", m.confirm)
	}
	m, cmd := press(m, "n")
	if m.confirm != "" || cmd != nil {
		t.Error("n doesn't cancel")
	}
	m, _ = press(m, "x")
	_, cmd = press(m, "y")
	cmd()
	if !slices.Equal(ran, []string{"kill-session -t =b:"}) {
		t.Errorf("y ran %q", ran)
	}
}

type fakeLookups struct{ prs int }

func (f *fakeLookups) Branch(path string) string { return "br-" + path }
func (f *fakeLookups) PR(string, string) sidebar.PR {
	f.prs++
	return sidebar.PR{Number: 1, State: "open"}
}
func (f *fakeLookups) OpenPR(string) error { return errors.New("no browser") }

// One refresh: list-panes, list-clients, then one capture per tile only while the herd window is shown.
func TestRefresh(t *testing.T) {
	rows := func(active string) string {
		r := tmux.FormatRow
		return r("herd", "$1", "1", active, "%1", "/h", "", "claude", "idle", "claude", "", "", "", "1", "", "") +
			r("herd", "$1", "1", active, "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "") +
			r("w1", "$2", "1", "1", "%3", "/w1", "", "claude", "working", "claude", "0.5", "app", "", "", "$1", "") +
			r("plain", "$3", "1", "1", "%4", "/p", "", "zsh", "", "", "", "", "", "", "", "")
	}
	for _, active := range []string{"1", "0"} {
		var ran []string
		look := &fakeLookups{}
		c := tmux.NewFake(func(args []string) (string, error) {
			ran = append(ran, args[0])
			switch args[0] {
			case "list-panes":
				return rows(active), nil
			case "list-clients":
				return tmux.FormatRow("/dev/ttys1", "herd", "5"), nil
			}
			return "\x1b[32mgot: hi\x1b[0m\n\n", nil
		})
		m := Model{c: c, pane: "%2", look: look, width: 80, height: 20}
		msg := m.refresh()().(refreshMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		s := msg.snap
		if len(s.tiles) != 1 || s.tiles[0].sess.Name != "w1" || s.tiles[0].startedBy != "$1" || s.self != "$1" || s.client != "/dev/ttys1" {
			t.Fatalf("active=%s snapshot %+v", active, s)
		}
		if active == "1" {
			if !slices.Equal(ran, []string{"list-panes", "list-clients", "capture-pane"}) || look.prs != 1 ||
				!slices.Equal(s.tiles[0].body, []string{"\x1b[32mgot: hi\x1b[0m"}) || s.tiles[0].sess.Cost != "$0.50" {
				t.Errorf("visible: ran %q, prs %d, tile %+v", ran, look.prs, s.tiles[0])
			}
		} else if !slices.Equal(ran, []string{"list-panes", "list-clients"}) || look.prs != 0 {
			t.Errorf("hidden window: ran %q, prs %d", ran, look.prs)
		}
	}
}

// The last page, partly filled, captures only the tiles on it.
func TestRefreshLastPage(t *testing.T) {
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "%1", "/h", "", "dev", "", "", "", "", "", "1", "", "")
	for _, w := range []string{"w1", "w2", "w3"} {
		rows += r(w, "$"+w, "1", "1", "%"+w, "/"+w, "", "codex", "", "", "", "", "", "", "", "")
	}
	var captured []string
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return rows, nil
		case "list-clients":
			return r("/dev/ttys1", "herd", "5"), nil
		}
		captured = append(captured, args[len(args)-1])
		return "x\n", nil
	})
	m := Model{c: c, pane: "%1", look: &fakeLookups{}, width: 40, height: 13, selected: "w3"}
	if msg := m.refresh()().(refreshMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	if !slices.Equal(captured, []string{"%w3"}) {
		t.Errorf("captured %q", captured)
	}
}

func TestReply(t *testing.T) {
	var ran []string
	status := tmux.FormatRow("claude", "idle", "claude", "3", "")
	m := twoTiles()
	m.snap.tiles[0].pane, m.snap.tiles[1].pane = "%5", "%6"
	paneCmd := "claude" // what runs in b's agent pane
	m.c = tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("b", "1", "%6", "/p", "t", paneCmd, "idle", "claude", "", "", "", "$2", "", ""), nil
		case "has-session":
			return "", nil
		}
		ran = append(ran, args[0]+" "+args[len(args)-1])
		if args[0] == "display-message" {
			return status, nil
		}
		return "", nil
	})
	key := func(m Model, k tea.KeyMsg) (Model, tea.Cmd) {
		next, cmd := m.Update(k)
		return next.(Model), cmd
	}
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	m, _ = key(m, runes("i"))
	m, _ = key(m, runes("fix"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = key(m, runes("itx"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if row := ansi.Strip(m.render()[11]); !strings.HasPrefix(row, " b ❯ fix it") {
		t.Errorf("reply row %q", row)
	}
	// Keys go to the draft while typing: j doesn't move the selection
	if m, _ = key(m, runes("j")); m.selected != "b" || string(m.draft) != "fix itj" {
		t.Errorf("typing: selected %q draft %q", m.selected, string(m.draft))
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.reply != "" || cmd == nil {
		t.Fatalf("enter: reply %q, cmd %v", m.reply, cmd)
	}
	if msg := cmd().(actionMsg); msg.msg != "Sent to b" {
		t.Errorf("send: %q", msg.msg)
	}
	if want := []string{"display-message " + tmux.Format[control.Status](), "paste-buffer %6", "send-keys Enter"}; !slices.Equal(slices.DeleteFunc(slices.Clone(ran), func(s string) bool { return strings.HasPrefix(s, "load-buffer") }), want) {
		t.Errorf("sent with %q", ran)
	}

	// Claude exited while the reply was typed: its pane's shell must not get the text
	ran, paneCmd = nil, "zsh"
	m, _ = key(m, runes("i"))
	m, _ = key(m, runes("rm -rf x"))
	_, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if msg := cmd().(actionMsg); !strings.Contains(msg.msg, "agent is gone") || len(ran) != 0 {
		t.Errorf("gone agent: %q, ran %q", msg.msg, ran)
	}
	paneCmd = "claude"

	ran = nil
	m, _ = key(m, runes("i"))
	m, _ = key(m, runes("never"))
	if m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEsc}); m.reply != "" || cmd != nil || len(ran) != 0 {
		t.Errorf("esc: reply %q, ran %q", m.reply, ran)
	}
}

func TestDialogKeys(t *testing.T) {
	var ran []string
	m := twoTiles()
	m.snap.tiles[0].pane = "%5"
	m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
	press := func(m Model, k string) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model), cmd
	}
	// b is selected and not waiting
	if m, cmd := press(m, "1"); cmd != nil || !strings.Contains(m.msg, "isn't waiting") {
		t.Errorf("1 on a worker that isn't waiting: msg %q", m.msg)
	}
	m.selected = "a"
	if !strings.Contains(ansi.Strip(m.render()[11]), "1-9 answer") {
		t.Errorf("waiting tile hint %q", ansi.Strip(m.render()[11]))
	}
	_, cmd := press(m, "2")
	cmd()
	if !slices.Equal(ran, []string{"send-keys -t %5 -l 2"}) {
		t.Errorf("2 on the waiting worker ran %q", ran)
	}
}

func TestReplyPasteAndQuit(t *testing.T) {
	m := twoTiles()
	m.snap.tiles[1].pane = "%6"
	key := func(m Model, k tea.KeyMsg) (Model, tea.Cmd) {
		next, cmd := m.Update(k)
		return next.(Model), cmd
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one\nline two\x1b[31m"), Paste: true})
	got := m.render()
	if len(got) != m.height || !strings.Contains(ansi.Strip(got[11]), "line one⏎line two⏎[31m") {
		t.Errorf("pasted reply: %d lines, row %q", len(got), ansi.Strip(got[11]))
	}
	if string(m.draft) != "line one\nline two\x1b[31m" {
		t.Errorf("draft keeps the paste as typed: %q", string(m.draft))
	}
	if _, cmd := key(m, tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || cmd() != tea.Quit() {
		t.Error("ctrl+c while typing doesn't quit")
	}
}
