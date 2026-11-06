package herd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

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
)

func twoTiles() Model {
	return Model{width: 40, height: 12, selected: "b", snap: snapshot{self: "$1", tiles: []tile{
		{sess: sidebar.Session{Name: "a", Agent: "Claude", State: "waiting", Cost: "$1.20"}, body: []string{"\x1b[31mred line that is far too long to fit\x1b[0m"}},
		{sess: sidebar.Session{Name: "b", Agent: "Codex", Branch: "fix", PR: sidebar.PR{Number: 3, State: "open"}}, body: []string{"x"}},
	}}}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
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
	// Tall enough for both tiles with the box open
	m.height = 14
	// Both in the Idle section, so folded under one header: a click answers in place: it selects the card and opens the reply
	// box for it, never switching
	m, cmd := click(m, 8, 1)
	if m.selected != "a" || m.box.session != "a" || m.box.pane != "%5" || cmd != nil || len(ran) != 0 {
		t.Errorf("click on tile a: selected %q reply %q pane %q, ran %q", m.selected, m.box.session, m.box.pane, ran)
	}
	if next, cmd := click(m, 8, 0); cmd != nil || next.box.session != "a" {
		t.Error("click on the section header does something")
	}
	m.box.ed.set("half")
	if m, _ = click(m, 8, 1); m.box.ed.String() != "half" {
		t.Errorf("click on the same tile drops the draft: %q", m.box.ed.String())
	}
	if m, _ = click(m, 8, 2); m.box.session != "b" || m.box.pane != "%6" || m.box.ed.String() != "" {
		t.Errorf("click on tile b: reply %q pane %q draft %q", m.box.session, m.box.pane, m.box.ed.String())
	}
	if len(ran) != 0 {
		t.Errorf("clicks ran %q", ran)
	}
}

// Focus moving to another pane closes the reply box, keeping a typed reply for when its tile's box opens
// again; focus coming back doesn't reopen it. A refresh listed before the click that focused the pane leaves
// the box the click opened, and the box stays while its answer goes out or is still on screen.
func TestBlurClosesBox(t *testing.T) {
	m := twoTiles()
	m.height = 14
	refresh := func(m Model, focused bool, inputs int) Model {
		snap := m.snap
		snap.focused = focused
		next, _ := m.Update(refreshMsg{snap: snap, inputs: inputs})
		return next.(Model)
	}
	click := func(m Model) Model {
		next, _ := m.Update(tea.MouseMsg{X: 8, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return next.(Model)
	}
	m = click(m)
	if m = refresh(m, false, m.inputs-1); m.box.session != "a" {
		t.Fatal("a refresh listed before the click closed its box")
	}
	// Away before any refresh saw the pane focused
	m.box.ed.set("half")
	if m = refresh(m, false, m.inputs); m.box.session != "" || m.selected != "a" {
		t.Fatalf("blur: box %q selected %q", m.box.session, m.selected)
	}
	if got := ansi.Strip(strings.Join(m.render(), "\n")); strings.Contains(got, "esc cancel") {
		t.Errorf("blur leaves the box drawn:\n%s", got)
	}
	if m = refresh(m, true, m.inputs); m.box.session != "" {
		t.Error("focus back reopens the box")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m = next.(Model); m.box.session != "a" || m.box.ed.String() != "half" {
		t.Errorf("i after blur: box %q draft %q", m.box.session, m.box.ed.String())
	}
	m.answering = "a"
	if m = refresh(m, false, m.inputs); m.box.session != "a" {
		t.Error("blur closed the box while its answer goes out")
	}
	m.answering, m.box.sent = "b", "key"
	if m = refresh(m, false, m.inputs); m.box.session != "a" {
		t.Error("blur closed the box while its answer is on screen")
	}
	m.box.sent = ""
	if m = refresh(m, false, m.inputs); m.box.session != "" {
		t.Error("another worker's answer keeps the box open")
	}
}

func TestKill(t *testing.T) {
	var ran []string
	m := twoTiles()
	m.c = tmux.NewFake(func(args []string) (string, error) { ran = append(ran, strings.Join(args, " ")); return "", nil })
	press := func(m Model, k string) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model), cmd
	}
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
	// sidebar.Close (TestHerdKillForgets runs it whole): the fake lists no sessions, so there's nothing to close
	if len(ran) != 1 || !strings.HasPrefix(ran[0], "list-sessions ") {
		t.Errorf("y ran %q", ran)
	}
}

type fakeLookups struct {
	prs   int
	args  []string // the last PR call's
	tails map[string]desktop.Tail
	read  []string   // the transcripts Tail read
	cis   []string   // the PRs CI read, each with its gen
	pr    sidebar.PR // what PR returns; #1 open when zero
}

func (f *fakeLookups) Branch(path string) string { return "br-" + path }
func (f *fakeLookups) PR(pane, stored, gen, path, branch string) sidebar.PR {
	f.prs++
	f.args = []string{pane, stored, gen, path, branch}
	if f.pr.Number != 0 {
		return f.pr
	}
	return sidebar.PR{Number: 1, State: "open"}
}
func (f *fakeLookups) OpenPR(string) error { return errors.New("no browser") }
func (f *fakeLookups) CI(url, gen string) ci {
	f.cis = append(f.cis, url+" "+gen)
	return ci{state: ciRunning, names: []string{"test"}}
}
func (f *fakeLookups) Tail(path string) (desktop.Tail, error) {
	f.read = append(f.read, path)
	t, ok := f.tails[path]
	if !ok {
		return desktop.Tail{}, errors.New("no transcript " + path)
	}
	return t, nil
}

// One refresh: list-panes, list-clients, then one capture per tile only while the herd window is shown.
func TestRefresh(t *testing.T) {
	rows := func(active string) string {
		r := tmux.FormatRow
		return r("herd", "$1", "1", active, "0", "%1", "/h", "", "claude", "idle", "claude", "", "", "", "1", "", "", "", "", "", "", "", "", "", "") +
			r("herd", "$1", "1", active, active, "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "") +
			r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "working", "claude", "0.5", "app", "", "", "$1", "", "", "stored", "2", "", "", "", "", "") +
			r("plain", "$3", "1", "1", "1", "%4", "/p", "", "zsh", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
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
		if len(s.tiles) != 1 || s.tiles[0].sess.Name != "w1" || s.self != "$1" || s.client != "/dev/ttys1" || s.focused != (active == "1") {
			t.Fatalf("active=%s snapshot %+v", active, s)
		}
		if active == "1" {
			if want := []string{"%3", "stored", "2", "/w1", "br-/w1"}; !slices.Equal(look.args, want) {
				t.Errorf("PR(%q), want the agent pane's (%q)", look.args, want)
			}
			if !slices.Equal(ran, []string{"list-panes", "list-clients", "capture-pane"}) || look.prs != 1 ||
				!slices.Equal(s.tiles[0].body, []string{"\x1b[32mgot: hi\x1b[0m"}) || s.tiles[0].sess.Cost != "$0.50" {
				t.Errorf("visible: ran %q, prs %d, tile %+v", ran, look.prs, s.tiles[0])
			}
		} else if !slices.Equal(ran, []string{"list-panes", "list-clients"}) || look.prs != 0 {
			t.Errorf("hidden window: ran %q, prs %d", ran, look.prs)
		}
	}
	// Focus is own pane's, not its window's
	for _, active := range []bool{true, false} {
		rows := []row{{Session: "herd", SessionID: "$1", WindowActive: true, Active: !active, ID: "%1"},
			{Session: "herd", SessionID: "$1", WindowActive: true, Active: active, ID: "%2"}}
		if s, err := buildSnapshot(rows, nil, "%2", "", &fakeLookups{}); err != nil || s.focused != active {
			t.Errorf("pane_active %v: focused %v, %v", active, s.focused, err)
		}
	}
}

// A deciding tile whose screen shows Claude's input box back (its dialog answered No or Esc, no hook) is
// settled idle and cropped like any idle tile; one still showing its dialog gets a picker and no extra reads.
func TestRefreshDialogGone(t *testing.T) {
	rule := strings.Repeat("─", 40)
	gone := "  ⎿  Interrupted · What should Claude do instead?\n\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts\n"
	open := rule + "\n Bash command\n\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "") +
		r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "waiting", "claude", "", "", "", "", "$1", "permission_prompt", "", "", "", "", "", "", "", "")
	for _, screen := range []string{gone, open} {
		var ran []string
		state := "waiting"
		c := tmux.NewFake(func(args []string) (string, error) {
			ran = append(ran, args[0])
			switch args[0] {
			case "list-panes":
				return rows, nil
			case "list-clients":
				return r("/dev/ttys1", "herd", "5"), nil
			case "display-message":
				return r("claude", state, "claude", "1", "permission_prompt", "50", "", ""), nil
			case "if-shell":
				state = "idle"
			}
			return screen, nil
		})
		m := Model{c: c, pane: "%2", look: &fakeLookups{}, width: 80, height: 20}
		msg := m.refresh()().(refreshMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		tl := msg.snap.tiles[0]
		body := strings.Join(tl.body, "\n")
		if screen == gone {
			if tl.deciding || tl.sess.State != "idle" || strings.Contains(body, "❯") || !slices.Contains(ran, "if-shell") {
				t.Errorf("dialog gone: deciding %v, state %q, ran %q, body:\n%s", tl.deciding, tl.sess.State, ran, body)
			}
		} else if !tl.deciding || tl.pick == nil || slices.Contains(ran, "display-message") {
			t.Errorf("dialog open: deciding %v, pick %v, ran %q", tl.deciding, tl.pick != nil, ran)
		}
	}
}

func TestReply(t *testing.T) {
	var ran []string
	status := tmux.FormatRow("claude", "idle", "claude", "3", "", "", "", "")
	m := twoTiles()
	m.snap.tiles[0].pane, m.snap.tiles[1].pane = "%5", "%6"
	paneCmd := "claude" // what runs in b's agent pane
	m.c = tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("b", "1", "%6", "/p", "t", paneCmd, "idle", "claude", "", "", "", "$2", "", "", "", ""), nil
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
	if got := m.render(); ansi.Strip(got[10]) != "─ ● b Codex #3 open fix "+strings.Repeat("─", 16) || ansi.Strip(got[11]) != "▎ fix it " {
		t.Errorf("reply box %q", got[10:])
	}
	// Keys go to the draft while typing: j doesn't move the selection
	if m, _ = key(m, runes("j")); m.selected != "b" || m.box.ed.String() != "fix itj" {
		t.Errorf("typing: selected %q draft %q", m.selected, m.box.ed.String())
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.box.session != "" || cmd == nil || !slices.Equal(m.history, []string{"fix it"}) {
		t.Fatalf("enter: reply %q, cmd %v, history %q", m.box.session, cmd, m.history)
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
	if m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEsc}); m.box.session != "" || cmd != nil || len(ran) != 0 {
		t.Errorf("esc: reply %q, ran %q", m.box.session, ran)
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
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one\r\nline two\x1b[31m"), Paste: true})
	got := m.render()
	// The box grows a row per line
	if len(got) != m.height || ansi.Strip(got[10]) != "▎ line one" || ansi.Strip(got[11]) != "▎ line two⏎[31m " {
		t.Errorf("pasted reply: %d lines, rows %q", len(got), got[9:])
	}
	if m.box.ed.String() != "line one\nline two\x1b[31m" {
		t.Errorf("draft keeps the paste as typed: %q", m.box.ed.String())
	}
	if _, cmd := key(m, tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || cmd() != tea.Quit() {
		t.Error("ctrl+c while typing doesn't quit")
	}
}

func TestTmuxQuote(t *testing.T) {
	if got, want := tmuxQuote(`'/a b/#x"$y\z' '$3'`), `"'/a b/##x\"\$y\\z' '\$3'"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// The previews take 40% of the window, at least 60 columns, at most half.
func TestFitWidth(t *testing.T) {
	for window, want := range map[string]string{"100": "50", "140": "60", "200": "80"} {
		var resized string
		c := tmux.NewFake(func(args []string) (string, error) {
			if args[0] == "resize-pane" {
				resized = args[len(args)-1]
			}
			return window + "\n", nil
		})
		if err := fitWidth(c, "%2"); err != nil || resized != want {
			t.Errorf("window %s: resized to %q (%v), want %s", window, resized, err, want)
		}
	}
}
