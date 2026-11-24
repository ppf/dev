package herd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/skill"
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
	return Model{width: 40, height: 12, selected: "b", size: shared, snap: snapshot{self: "$1", tiles: []tile{
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
	// Both in the Idle section, so folded under one header: a click only selects the card, for the keys
	m, cmd := click(m, 8, 1)
	if m.selected != "a" || m.box.session != "" || cmd != nil || len(ran) != 0 {
		t.Errorf("click on tile a: selected %q reply %q, ran %q", m.selected, m.box.session, ran)
	}
	if next, cmd := click(m, 8, 0); cmd != nil || next.selected != "a" {
		t.Error("click on the section header does something")
	}
	if m = key(m, "m"); m.box.session != "a" || m.box.pane != "%5" {
		t.Errorf("m on tile a: reply %q pane %q", m.box.session, m.box.pane)
	}
	// A click closes the box, so the keys reach the card it selects; its draft waits, and an answer going out
	// keeps it
	m.box.ed.set("half")
	if m, _ = click(m, 8, 1); m.box.session != "" || m.drafts["a"].ed.String() != "half" {
		t.Errorf("click on the box's tile: reply %q drafts %v", m.box.session, m.drafts)
	}
	m = key(m, "m")
	m.box.sent = "key"
	if m, _ = click(m, 8, 1); m.box.session != "a" {
		t.Error("click on the box's tile closed it while its answer is on screen")
	}
	if m, _ = click(m, 8, 2); m.selected != "b" || m.box.session != "" || m.drafts["a"].ed.String() != "half" {
		t.Errorf("click on tile b: selected %q reply %q drafts %v", m.selected, m.box.session, m.drafts)
	}
	if len(ran) != 0 {
		t.Errorf("clicks ran %q", ran)
	}
	// g and ⏎ go to the selected worker's session
	for _, k := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("g")}, {Type: tea.KeyEnter}} {
		ran = nil
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		cmd()
		if len(ran) != 1 || !strings.Contains(ran[0], "switch-client") || !strings.HasSuffix(ran[0], tmux.SessionTarget("b")) {
			t.Errorf("%s ran %q", k, ran)
		}
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
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = next.(Model)
	if m = refresh(m, false, m.inputs-1); m.box.session != "a" {
		t.Fatal("a refresh listed before the key closed its box")
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
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
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
	prs    int
	args   []string // the last PR call's
	tails  map[string]desktop.Tail
	read   []string              // the transcripts Tail read
	cis    []string              // the PRs CI read, each with its pane and gen
	pr     sidebar.PR            // what PR returns; #1 open when zero
	byPath map[string]sidebar.PR // what PR returns for a path, over pr
}

func (f *fakeLookups) Branch(path string) string { return "br-" + path }
func (f *fakeLookups) PR(pane, stored, gen, path, branch string) sidebar.PR {
	f.prs++
	f.args = []string{pane, stored, gen, path, branch}
	if pr, ok := f.byPath[path]; ok {
		return pr
	}
	if f.pr.Number != 0 {
		return f.pr
	}
	return sidebar.PR{Number: 1, State: "open"}
}
func (f *fakeLookups) OpenPR(string) error { return errors.New("no browser") }
func (f *fakeLookups) CI(url, pane, gen string) ci {
	f.cis = append(f.cis, url+" "+pane+" "+gen)
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
		return r("herd", "$1", "1", active, "0", "%1", "/h", "", "claude", "idle", "claude", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
			r("herd", "$1", "1", active, active, "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
			r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "working", "claude", "0.5", "app", "", "", "$1", "", "", "stored", "2", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
			r("plain", "$3", "1", "1", "1", "%4", "/p", "", "zsh", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
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
	rows := r("herd", "$1", "1", "1", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") +
		r("w1", "$2", "1", "1", "1", "%3", "/w1", "", "claude", "waiting", "claude", "", "", "", "", "$1", "permission_prompt", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
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
			return tmux.FormatRow("b", "1", "%6", "/p", "t", paneCmd, "idle", "claude", "", "", "", "$2", "", "", "", "", "", "", "", "", "", "", "", "", ""), nil
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
	if got := m.render(); ansi.Strip(got[9]) != " ╭─ Message ● b Codex #3 open fix ─────╮" || ansi.Strip(got[10]) != " │ fix it                              │" {
		t.Errorf("reply box %q", got[9:])
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
	if msg := selfreload.Done(cmd()).(actionMsg); msg.msg.text != "Sent to b" {
		t.Errorf("send: %q", msg.msg.text)
	}
	if want := []string{"display-message " + tmux.Format[control.Status](), "paste-buffer %6", "send-keys Enter"}; !slices.Equal(slices.DeleteFunc(slices.Clone(ran), func(s string) bool { return strings.HasPrefix(s, "load-buffer") }), want) {
		t.Errorf("sent with %q", ran)
	}

	// Claude exited while the reply was typed: its pane's shell must not get the text
	ran, paneCmd = nil, "zsh"
	m, _ = key(m, runes("i"))
	m, _ = key(m, runes("rm -rf x"))
	_, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if msg := selfreload.Done(cmd()).(actionMsg); !strings.Contains(msg.msg.text, "agent is gone") || len(ran) != 0 {
		t.Errorf("gone agent: %q, ran %q", msg.msg.text, ran)
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
	if m, cmd := press(m, "1"); cmd != nil || !strings.Contains(m.msg.text, "b has neither") {
		t.Errorf("1 on a worker that isn't waiting: msg %q", m.msg.text)
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

// 1-9 on an asks card whose reply read options send that option as its reply, as m does; the hint says so.
func TestAskOptionKeys(t *testing.T) {
	var ran, pasted []string
	m := twoTiles()
	m.width = 60
	m.snap.tiles[1] = tile{sess: sidebar.Session{Name: "b", Agent: "Claude", State: agent.Idle}, pane: "%6",
		note: "asks: Which DB?", tail: desktop.Tail{Lines: []string{"Intro.", "Which **DB**?", "- `Postgres`", "- SQLite", "3. Both"}}}
	kept := replyBox{session: "b", pane: "%6"}
	kept.ed.set("kept")
	m.drafts = map[string]replyBox{"b": kept}
	m.c = tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("b", "1", "%6", "/p", "t", "claude", "idle", "claude", "", "", "", "$2", "", "", "", "", "", "", "", "", "", "", "", "", ""), nil
		case "has-session":
			return "", nil
		case "display-message":
			return tmux.FormatRow("claude", "idle", "claude", "3", "", "", "", ""), nil
		case "load-buffer":
			b, err := os.ReadFile(args[len(args)-1])
			pasted = append(pasted, string(b))
			return "", err
		}
		ran = append(ran, args[0]+" "+args[len(args)-1])
		return "", nil
	})
	press := func(m Model, k string) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model), cmd
	}
	if got := strings.TrimSpace(ansi.Strip(m.bottom()[0])); !strings.HasPrefix(got, "1-9 answer  j/k select") {
		t.Errorf("asks card hint %q", got)
	}
	if m, cmd := press(m, "4"); cmd != nil || m.msg.text != "b's question has 3 options" {
		t.Errorf("4 of 3 options: msg %q", m.msg.text)
	}
	m, cmd := press(m, "1")
	if cmd == nil || m.box.session != "" || m.drafts["b"].ed.String() != "kept" {
		t.Fatalf("1: cmd %v, box %q, draft %q", cmd != nil, m.box.session, m.drafts["b"].ed.String())
	}
	if msg := selfreload.Done(cmd()).(actionMsg); msg.msg.text != "Sent to b" || !slices.Equal(pasted, []string{"Postgres"}) || !slices.Contains(ran, "paste-buffer %6") {
		t.Errorf("1 sent %q, pasted %q, ran %q", msg.msg.text, pasted, ran)
	}
	pasted = nil
	_, cmd = press(m, "3")
	if cmd(); !slices.Equal(pasted, []string{"Both"}) {
		t.Errorf("3 pasted %q", pasted)
	}
}

// 1-9 on an asks card whose turn ended telling how to reply send the options its line names; on one ending on
// list items that are questions they send nothing: those take a typed reply (m).
func TestAskReplyOptionKeys(t *testing.T) {
	var pasted []string
	m := twoTiles()
	m.width = 60
	m.c = tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("b", "1", "%6", "/p", "t", "claude", "idle", "claude", "", "", "", "$2", "", "", "", "", "", "", "", "", "", "", "", "", ""), nil
		case "display-message":
			return tmux.FormatRow("claude", "idle", "claude", "3", "", "", "", ""), nil
		case "load-buffer":
			b, err := os.ReadFile(args[len(args)-1])
			pasted = append(pasted, string(b))
			return "", err
		}
		return "", nil
	})
	press := func(m Model, k string) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model), cmd
	}
	m.snap.tiles[1] = tile{sess: sidebar.Session{Name: "b", Agent: "Claude", State: agent.Idle}, pane: "%6",
		note: "asks: Reply **Done** once it's connected, or **Skip** to leave the lw check for later.",
		tail: desktop.Tail{Lines: []string{"**Step 2 of 4: connect the VPN.** It still times out.",
			"Reply **Done** once it's connected, or **Skip** to leave the lw check for later."}}}
	if got := strings.TrimSpace(ansi.Strip(m.bottom()[0])); !strings.HasPrefix(got, "1-9 answer") {
		t.Errorf("reply card hint %q", got)
	}
	if _, cmd := press(m, "2"); cmd == nil {
		t.Fatal("2 sent nothing")
	} else if cmd(); !slices.Equal(pasted, []string{"Skip"}) {
		t.Errorf("2 pasted %q", pasted)
	}
	m.snap.tiles[1].note = "asks: - Post it? I recommend **Yes**. - Ping Carl? I recommend **Yes**."
	m.snap.tiles[1].tail.Lines = []string{"Two questions:", "- Post it? I recommend **Yes**.", "- Ping Carl? I recommend **Yes**. He plans to."}
	if got := m.snap.tiles[1].section(); got != needsYouSection {
		t.Errorf("questions card in %v, want needs you", got)
	}
	if got := strings.TrimSpace(ansi.Strip(m.bottom()[0])); strings.HasPrefix(got, "1-9") {
		t.Errorf("questions card hint %q, want no 1-9", got)
	}
	if m, cmd := press(m, "1"); cmd != nil || !strings.Contains(m.msg.text, "(m to reply)") {
		t.Errorf("1 on the questions card: cmd %v, msg %q", cmd != nil, m.msg.text)
	}
	if got := statusText(m.snap.tiles[1], needsYouSection); !strings.HasPrefix(got, "Two questions: - Post it?") {
		t.Errorf("questions card status %q", got)
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
	if len(got) != m.height || ansi.Strip(got[9]) != " │ line one                            │" || ansi.Strip(got[10]) != " │ line two⏎[31m                       │" {
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

// fitWidth gives the previews 40% of the window, at least 60 columns, at most half.
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

// The pane collapses while it has nothing to show and takes its share of the window otherwise: sized once the
// first refresh is in, again only when that changes or a resize widens it collapsed, never while zoomed; a
// focused collapsed pane hands the focus to the coordinator.
func TestFit(t *testing.T) {
	var ran []string
	c := tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "200\n", nil
	})
	workers := snapshot{tiles: twoTiles().snap.tiles}
	const fits, shrinks = "display-message -p -t %2 #{window_width}|resize-pane -t %2 -x 80", "resize-pane -t %2 -x 1"
	const handsFocus = shrinks + " ; select-pane -t %2 -L"
	type step struct {
		name string
		msg  tea.Msg
		want string
	}
	for name, steps := range map[string][]step{
		"empty boss": {
			{"resize before a refresh", tea.WindowSizeMsg{Width: 80, Height: 20}, ""},
			{"first refresh", refreshMsg{}, shrinks},
			{"no change", refreshMsg{}, ""},
			{"refresh error shows", refreshMsg{err: errors.New("gone")}, fits},
			{"error gone, focused", refreshMsg{snap: snapshot{focused: true}}, handsFocus},
			{"resize widens it", tea.WindowSizeMsg{Width: 47, Height: 20}, shrinks},
			{"own collapse's resize", tea.WindowSizeMsg{Width: 1, Height: 20}, ""},
			{"focus moved back in", refreshMsg{snap: snapshot{focused: true}}, handsFocus},
			{"first worker, zoomed", refreshMsg{snap: snapshot{tiles: workers.tiles, zoomed: true}}, ""},
			{"unzoomed", refreshMsg{snap: workers}, fits},
			{"resize with workers", tea.WindowSizeMsg{Width: 47, Height: 20}, ""},
			{"same workers", refreshMsg{snap: workers}, ""},
			{"last worker gone", refreshMsg{}, shrinks},
		},
		"restored with workers": {{"first refresh", refreshMsg{snap: workers}, fits}},
	} {
		m := Model{c: c, pane: "%2"}
		for _, st := range steps {
			ran = nil
			next, _ := m.Update(st.msg)
			if m = next.(Model); m.msg.alert {
				t.Fatalf("%s, %s: %s", name, st.name, m.msg.text)
			}
			if got := strings.Join(ran, "|"); got != st.want {
				t.Errorf("%s, %s: ran %q, want %q", name, st.name, got, st.want)
			}
		}
	}
}

// The pane stays at its share while it has an open reply box or a kill to confirm, though its last worker went.
func TestFitKeepsPrompts(t *testing.T) {
	for name, m := range map[string]Model{"reply box": {box: replyBox{session: "a"}}, "confirm": {confirm: "a"}} {
		var ran []string
		m.c, m.pane, m.size = tmux.NewFake(func(args []string) (string, error) {
			ran = append(ran, args[0])
			return "200\n", nil
		}), "%2", shared
		// Typing in the box keeps the pane focused
		m.Update(refreshMsg{snap: snapshot{focused: true}})
		if len(ran) != 0 {
			t.Errorf("%s: ran %q, want nothing", name, ran)
		}
	}
}

// A failed fit shows why, at the pane's share so the alert reads, and is tried again on the next message.
func TestFitFails(t *testing.T) {
	fail := true
	c := tmux.NewFake(func(args []string) (string, error) {
		if fail && args[0] == "resize-pane" {
			return "", errors.New("no pane")
		}
		return "200\n", nil
	})
	next, _ := Model{c: c, pane: "%2", size: unsized}.Update(refreshMsg{})
	m := next.(Model)
	if m.size != unsized || !strings.HasSuffix(m.msg.text, ": no pane") || !m.msg.alert {
		t.Fatalf("after a failed collapse: size %v, msg %+v", m.size, m.msg)
	}
	fail = false
	if next, _ = m.Update(tickMsg{}); next.(Model).size != shared {
		t.Errorf("next message: size %v, want its share for the alert", next.(Model).size)
	}
}

// A collapsed pane, one column wide, renders without panicking, with or without workers and an alert.
func TestRenderOneColumn(t *testing.T) {
	for _, m := range []Model{{}, twoTiles()} {
		m.width, m.height, m.msg = 1, 3, alert("❌ x")
		if got := m.render(); len(got) != 3 {
			t.Errorf("%d tiles: %d rows, want 3", len(m.snap.tiles), len(got))
		}
	}
}

// The reply box is boxed like the card it writes to, in its colour: its target on the top border, its keys on
// the bottom one; the card's Message… lights up.
func TestReplyBoxLooks(t *testing.T) {
	m := twoTiles()
	m.width, m.height, m.folds = 60, 16, map[string]bool{"b": false}
	m.history = []string{"older"}
	m.open(m.snap.tiles[1])
	got := m.render()
	// go test -run TestReplyBoxLooks -v prints the board with its box
	t.Log("\n" + strings.Join(ansiStrip(got), "\n"))
	want := []string{
		" ╭─ Message ● b Codex #3 open fix ─────────────────────────╮",
		" │  type a message                                         │",
		" ╰─────────── ⏎ send · esc cancel · ⇧⏎ newline · ↑ recent ─╯",
	}
	if box := ansiStrip(got[13:]); !slices.Equal(box, want) {
		t.Errorf("box:\n%s", strings.Join(box, "\n"))
	}
	accent := sidebar.StateColour("Codex", "")
	if !strings.HasPrefix(got[13], " "+accent+"╭─") || !strings.Contains(got[15], accent+"─╯") {
		t.Errorf("box frame not in the card's colour: %q", got[13:])
	}
	if card := strings.Join(got[:13], "\n"); !strings.Contains(card, accent+bold+"Message…") {
		t.Errorf("Message… not lit:\n%q", card)
	}
	m, _ = press(m, runes("fix it"))
	if got := ansiStrip(m.render()); got[14] != " │ fix it "+strings.Repeat(" ", 49)+"│" || got[15] != want[2] {
		t.Errorf("draft keeps its keys under it: %q", got[13:])
	}
	// Only the bound card's Message… lights up
	m.folds["a"] = false
	if n := strings.Count(strings.Join(m.render(), "\n"), bold+"Message…"); n != 1 {
		t.Errorf("%d cards' Message… lit", n)
	}
	// A message takes the keys' place; an alert goes under the box, ❌ or not
	m.msg = info("⏳ answering b…")
	if got := ansiStrip(m.render()); !strings.HasSuffix(got[15], "─ ⏳ answering b… ─╯") {
		t.Errorf("message: %q", got[13:])
	}
	m.msg = alert("gh: not logged in")
	if got := ansiStrip(m.render()); got[14] != want[2] || got[15] != " gh: not logged in" {
		t.Errorf("alert: %q", got[12:])
	}
}

// The keys offer o PR only while the selected worker has a PR, or a lookup error to say.
func TestHintPR(t *testing.T) {
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	for i, name := range []string{"has", "none", "failed"} {
		rows += r(name, fmt.Sprint("$", i+3), "1", "1", "1", fmt.Sprint("%", i+3), "/"+name, "", "claude", "idle", "claude",
			"", "", "", "", "$1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	}
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "list-panes" {
			return rows, nil
		}
		if args[0] == "list-clients" {
			return r("/dev/ttys1", "herd", "5"), nil
		}
		return "", nil
	})
	look := &fakeLookups{byPath: map[string]sidebar.PR{"/none": {}, "/failed": {Err: "gh: not logged in"}}}
	m := Model{c: c, pane: "%2", look: look, width: 80, height: 30, size: shared}
	next, _ := m.Update(m.refresh()())
	m = next.(Model)
	want := map[string]bool{"has": true, "none": false, "failed": true}
	seen := map[string]bool{}
	for range want {
		seen[m.selected] = true
		if got := strings.Contains(ansi.Strip(m.bottom()[0]), "o PR"); got != want[m.selected] {
			t.Errorf("%s selected: o PR shown %v, want %v: %q", m.selected, got, want[m.selected], ansi.Strip(m.bottom()[0]))
		}
		m, _ = press(m, runes("j"))
	}
	if len(seen) != len(want) {
		t.Errorf("selected only %v", seen)
	}
}

// With the reply box closed, a message shows faint and an alert red.
func TestBottomMessage(t *testing.T) {
	m := twoTiles()
	m.msg = info("Sent to b")
	if got := m.bottom(); len(got) != 1 || got[0] != sidebar.HintRow("Sent to b", m.width) {
		t.Errorf("message: %q", got)
	}
	m.msg = alert("gh: not logged in")
	if got := m.bottom(); len(got) != 1 || got[0] != sidebar.AlertRow("gh: not logged in", m.width) {
		t.Errorf("alert: %q", got)
	}
}

// Every coordinator runs dev state on a compaction's SessionStart, which the user's hooks don't match; the manager has
// its claude.ai connectors off.
func TestCoordinatorSettings(t *testing.T) {
	hook := `"hooks":{"SessionStart":[{"hooks":[{"command":"'/my dev' 'state' 'idle'","timeout":5,"type":"command"}],"matcher":"compact"}]}`
	if got, want := coordinatorSettings("/my dev", false), "{"+hook+"}"; got != want {
		t.Errorf("boss: %s, want %s", got, want)
	}
	if got, want := coordinatorSettings("/my dev", true), `{"disableClaudeAiConnectors":true,`+hook+"}"; got != want {
		t.Errorf("manager: %s, want %s", got, want)
	}
}

func TestCoordinatorArgv(t *testing.T) {
	start := agent.Start{Model: "opus", Effort: "medium"}
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"new", nil, []string{"claude"}},
		{"new with a model and effort", start.Args(false), []string{"claude", "--model", "opus", "--effort", "medium"}},
		{"resumed, the effort again", append([]string{"--resume", "b1"}, start.Args(true)...), []string{"claude", "--resume", "b1", "--effort", "medium"}},
	} {
		want := append(tt.want, "--settings", coordinatorSettings("/bin/dev", false), "--append-system-prompt", coordinatorPrompt, "--remote-control=boss-p")
		// A boss never gets the manager's window
		if got := coordinatorArgv("boss-p", "/bin/dev", 300000, tt.args...); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", tt.name, got[:len(got)-5], tt.want)
		}
	}
}

// Settling is the default for the boss and the manager: the line and both example lists in each prompt, the boss
// recording it with --decided, the manager settling a boss's ask (unmarked with --why) and saying it in one line.
func TestPromptSettleRule(t *testing.T) {
	lists := "Settle: rebuild a local dev server, which test key a demo container uses, a workspace or a single file for tests, keep a draft for tomorrow, add the missing test in this PR, order of agreed work. " +
		"Ask: a merge into someone else's repo, a force-push, a send to a person, a settings or secrets change, new scope, taste (looks, wording to people), real money or a large share of the usage limit, anything hard to undo. When unsure which side: ask."
	for _, tt := range []struct {
		name, prompt string
		want         []string
	}{
		{"coordinatorPrompt", coordinatorPrompt, []string{
			"Settle, don't ask, what is cheap, reversible and inside what the user already agreed (a picked contract, an earlier pick, a standing rule): take your recommended option, record it at once with `dev todo ask '<question?>' --option '<pick> | <effect> (Recommended)' --option '<other> | <effect>' --decided '<why it is easy>'` (ticked, under Done as \"decided for you\" with Overrule), never only as a line in your pane, and report it in one line (a worker's question with one clear answer needs only that report); permission prompts and dialogs, and the manager's hand-on gate, stay as they are. " + lists,
			"note answers an ask the manager settled, marked obvious or not, as theirs",
		}},
		{"managerPrompt", managerPrompt, []string{
			"Settle, don't ask, a boss's ask that is cheap, reversible and inside what the user already agreed (a picked contract, an earlier pick, a standing rule), when you see no trade-off the boss missed: `dev todo answer <boss> <n> \"<recommended option>\" --recommended --question \"<item text>\" --via manager`, plus --why '<one line>' when its boss didn't mark it obvious (\"obvious: <why>\" in dev todo --all). " + lists,
			`Say each in your reply to the user in one line: "<boss> #<n>: took <option> over <other>; overrule on the board".`,
			"dev refuses it with no single recommended option, once answered or changed, unmarked without --why, and for a hand-on question; permission prompts and dialogs, and the hand-on gate, stay as they are.",
			"dev todo tally counts per list over 7 days (--days) the asks answered by the user, settled by a boss and by you, and overruled (not dev agent handoffs, the hand-on tally): a boss that settles much and is overruled much is the signal to ask more.",
		}},
	} {
		for _, want := range tt.want {
			if !strings.Contains(tt.prompt, want) {
				t.Errorf("%s: missing %q", tt.name, want)
			}
		}
		for _, old := range []string{"An easy decision is yours", "Ask the user only for what stays theirs", "asks-stay", "settle no item on a boss's list", "(obvious: <why>)\""} {
			if strings.Contains(tt.prompt, old) {
				t.Errorf("%s: still has the old wording %q", tt.name, old)
			}
		}
	}
}

// The prompts carry the skill's ask rules, and the manager's boss briefs use the skill's boss line verbatim.
func TestPromptAskRules(t *testing.T) {
	if !strings.Contains(skill.DevAgents, "> "+bossBriefAsk+"\n") {
		t.Error("bossBriefAsk isn't the dev-agents skill's boss brief line")
	}
	// The manager pastes it into a double-quoted shell argument.
	if strings.Contains(bossBriefAsk, "`") {
		t.Error("bossBriefAsk has a backtick: the shell would run it")
	}
	if strings.Contains(coordinatorPrompt, "AskUserQuestion before every use of ultracode") {
		t.Error("coordinatorPrompt: ultracode is asked with AskUserQuestion, not dev todo ask")
	}
	for _, tt := range []struct {
		name, prompt string
		want         []string
	}{
		{"coordinatorPrompt", coordinatorPrompt, []string{"--decided '<why it is easy>'", "gets --obvious '<why>' (exactly one option recommended)", "settled by the manager with your recommended option", "start without a further ask unless their cost or scope changed since the pick", "probe it yourself with bounded retries", "File a run's outward asks",
			"never ask your own decisions with AskUserQuestion or only as chat text",
			"relay them through your AskUserQuestion", "never let you answer a permission prompt",
			"Checks a plugin or hook asks for after your answer count as lookups too",
			"dev todo note decided|said|risk",
			"write a five-line contract file", "An item you do yourself or park is --on boss", "--waiting-for <who>, never a free-text owner", "state: your standing state off the board", `ending "leave it to the manager" is the manager's`, "(dev agent send-keys refuses you until it is answered)",
			"dev todo add refuses one the user does without, --replaces keeping the old ones; --no-details only for one that needs none", `in a "via manager (...)" note only the answer after the colon is`, "stop it after the first step and ask before the rest", `open -a "Google Chrome" <file>`,
			`answer it in the turn it reaches you`, "never act on it, never submit or clear it", "dev agent get --json gives the box as input: {text, suggestion}, dev agent read marks a suggestion's row [suggestion]", "dev todo ask --for <n>",
			"one question of at most 100 characters ending in \"?\"", "go in a file whose path is the last", "--context '<finding>'", `each option is "<label> | <detail>"`, `put the texts in single quotes, or keep "$"`,
			"run bare, in your own calls and in every brief whose worker runs them", "use && (never ;) and read the item back before you announce it",
			`A saved draft is "requested", never "replaced" or "sent": re-read the thread in the same turn, save none while a pick on that item is open`,
			"one item per list, its text bumped per release", "a Codex review (unless the project's rules or the user's pick require it before any merge) that hits a usage limit doesn't wait (its brief says so): it merges with every other condition met and owes the review", `an item "Codex review owed: PR #<n>" on your list until it ran; a release and its ask name those still open`,
			"optional Approaches section: 3-5 clearly different approaches, one inverting an assumption, each with its strongest side and main risk"}},
		{"managerPrompt", managerPrompt, []string{bossBriefAsk, "to them yourself with AskUserQuestion", "Up to 4 asks per call, in order, one per question",
			"only independent ones (no pick changes another's options or whether it is needed; else one per call)",
			"a hand-on question stays alone in its call, as does an item offering \"Hand it on\"", "Relay every pick of a call before the next call",
			"Right before the dialog mark every item it asks:",
			"right before each call with `dev todo show --print <boss> <n> --question \"<item text>\"` (one call; refused when gone or reading otherwise) and leave it out when ☑ (closed: answered or withdrawn), boss's own or boss closed", `name the item's number, mark and who it waits on as dev had them when the note went, "#<n> ☐ on you: …": relay a waits on you as written, its state as of then, re-checked before you ask it; an ask answered drops its question`, "--question \"<item text>\" --via manager", "one left unanswered, or \"Leave it open\", leaves the item open",
			"Each question starts \"<project> #<n>: \" and holds the item text word for word", "each option's detail (after \" | \") in its description", `the item's context as "- " lines`,
			"Plain to do items (not asks) aren't questions", "only the user's own pick (typed under Other too) answers it", "a message they typed here answers no item",
			"their latest pick also with typed messages after it (relay it, don't ask again; words that withdraw or change it need a fresh pick)", "dev refuses any other Claude's keys until it is answered", "with --kind work (their latest pick counts also with typed messages after it; words that withdraw or change it mean ask again)",
			"answer a permission prompt or dialog without the user's pick", "do anything destructive",
			"project research and project decisions stay with that project's boss", "answer with dev agent message manager",
			"for you the source is the boss", `is not opened again`, "plus \"contract first\"; don't scope it yourself", "Checks a plugin or hook asks for: run those that read only dev's own state",
			"Before handing on the user's own new idea or request", "Before every hand-on of new work, not only on a conflict", "holding the assessment and the brief you would send",
			"one AskUserQuestion of your own", "Drop it or skipped, it isn't handed on", "you decide nothing for a project but the asks you settle (below)", "when you see no trade-off the boss missed", "dev todo answer <boss> <n> \"<recommended option>\" --recommended --question", "you decide yourself and report in one line",
			"follows your assessment of that very work in your previous reply and names or plainly answers it (\"Tak ruszaj\" to \"start it today?\")",
			"--kind work --go '<their words, quoted>'", "words about something else, a conditional or a question are no go: ask with the hand-on question",
			"refused without that pick or a typed go",
			"you decide nothing for a project", "text typed under Other is a correction: revise and ask again", "Not gated: relaying the user's pick on an item", "your own easy decisions (simple or obvious; new work never is one)", "the brief you would send (the assessment and its cons in it)", "send-keys that worker directly", "your own task --on boss (the word itself", "one a person owes --waiting-for <who>",
			"brief's first line stands word for word on a line of its own", "or dev agent message --kind question", `reads "per boss-<project>" or "per <list> at HH:MM", the time from date run in that turn, never your guess`, "prompt, message or reply you send a boss carries --kind: work (new work, refused without that pick or a typed go), question", "dev agent handoffs",
			"With an advisor tool in your session, call your advisor only before you assess the user's new idea or request", "before a boss is cleared or closed", "when a boss's report contradicts what its list or dev's state shows", "before you show the user a text meant for other people", "when you are unsure whether something is yours to settle or the user's", "manager advisor <why, 3-6 words>", "never on plain relays, state polls or list updates", "start that reply with [advisor]", "While the trial runs (from the restart, one week)", "~/.local/state/dev/design/trial/tally.log", "manager user-caught <what>", "manager bad-relay <what>", "No advisor tool: skip this",
			`after a compaction, read the re-injected list and notes before answering, and first do what its "[dev] Owed from before the compaction" lines name`}},
	} {
		for _, want := range tt.want {
			if !strings.Contains(tt.prompt, want) {
				t.Errorf("%s: missing %q", tt.name, want)
			}
		}
	}
}

// The manager's coordinator gets managerPrompt, its claude.ai connectors off and its auto-compact window (none for 0), so
// a resurrect's Adopt restores it as the manager; a new one runs on fable · medium where its start leaves them unset.
func TestManagerArgvAndStart(t *testing.T) {
	want := []string{"claude", "--settings", coordinatorSettings("/bin/dev", true), "--autocompact", "300000",
		"--append-system-prompt", managerPrompt, "--remote-control=manager"}
	if got := coordinatorArgv(Manager, "/bin/dev", 300000); !slices.Equal(got, want) {
		t.Errorf("manager argv %q, want %q", got, want)
	}
	off := slices.Delete(slices.Clone(want), 3, 5)
	if got := coordinatorArgv(Manager, "/bin/dev", 0); !slices.Equal(got, off) {
		t.Errorf("manager argv, window 0: %q, want %q", got, off)
	}
	for _, tt := range []struct{ start, want agent.Start }{
		{agent.Start{}, agent.Start{Model: "fable", Effort: "medium"}},
		{agent.Start{Model: "opus"}, agent.Start{Model: "opus", Effort: "medium"}},
		{agent.Start{Effort: "high"}, agent.Start{Model: "fable", Effort: "high"}},
	} {
		if got := managerStart(tt.start); got != tt.want {
			t.Errorf("managerStart(%+v) = %+v, want %+v", tt.start, got, tt.want)
		}
	}
}

// The coordinator runs through an interactive login shell, so claude resolves on the user's PATH like a worker's.
func TestViaLoginShell(t *testing.T) {
	got := viaLoginShell("/bin/zsh", []string{"/x/dev", "pty-wrap", "--", "claude", "--append-system-prompt", "it's"})
	want := []string{"/bin/zsh", "-l", "-i", "-c", `exec '/x/dev' 'pty-wrap' '--' 'claude' '--append-system-prompt' 'it'"'"'s'`}
	if !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
}

// A default-shell that isn't POSIX fails clearly: the coordinator command is quoted for sh.
func TestCoordinatorCmdRefusesNonPosixShell(t *testing.T) {
	for shell, ok := range map[string]bool{"/bin/zsh": true, "/usr/bin/bash": true, "/opt/homebrew/bin/fish": false, "": false} {
		c := tmux.NewFake(func([]string) (string, error) { return shell + "\n", nil })
		cmd, err := coordinatorCmd(c, "boss-p")
		if ok && (err != nil || cmd[0] != shell) || !ok && (err == nil || !strings.Contains(err.Error(), "not a POSIX shell")) {
			t.Errorf("shell %q: %q, %v", shell, cmd, err)
		}
	}
}

// Adopt respawns the coordinator through the login shell, and refuses before touching the session when the shell isn't POSIX.
func TestAdoptCoordinatorShell(t *testing.T) {
	for shell, ok := range map[string]bool{"/bin/zsh": true, "/opt/homebrew/bin/fish": false} {
		var ran []string
		c := tmux.NewFake(func(args []string) (string, error) {
			ran = append(ran, strings.Join(args, " "))
			if slices.Contains(args, "show-option") {
				return shell + "\n", nil
			}
			return "", nil
		})
		err := Adopt(c, "boss-p", "%1", "%2", nil)
		respawned := slices.ContainsFunc(ran, func(r string) bool { return strings.HasPrefix(r, "respawn-pane -k -t %1 "+shell+" -l -i -c exec ") })
		if ok != (err == nil) || ok != respawned || !ok && len(ran) != 1 {
			t.Errorf("shell %q: err %v, ran %q", shell, err, ran)
		}
	}
}

// An existing boss keeps the coordinator it has: another model or effort for it is refused, not dropped.
func TestOpenExistingBossRefusesStart(t *testing.T) {
	var ran []string
	c := tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		if args[0] == "display-message" {
			return "opus\tmedium\n", nil
		}
		return "1\n", nil
	})
	err := Open(c, "p", "/p", agent.Start{Model: "opus", Effort: "low"})
	if want := "session boss-p was started with --effort medium, not low: --model and --effort only apply to a new session"; err == nil || err.Error() != want {
		t.Errorf("Open = %v, want %q", err, want)
	}
	if want := []string{"has-session -t =boss-p:", "show-option -qv -t =boss-p: @dev_herd",
		"display-message -p -t =boss-p: #{@dev_model}\t#{@dev_effort}"}; !slices.Equal(ran, want) {
		t.Errorf("ran %q, want only %q", ran, want)
	}
}
