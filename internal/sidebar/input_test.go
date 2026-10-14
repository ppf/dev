package sidebar

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputMove(t *testing.T) {
	order := []string{"a", "b", "c"}
	for _, tt := range []struct {
		order    []string
		selected string
		dir      int
		want     string
	}{
		{order, "a", 1, "b"},
		{order, "c", 1, "a"},  // wraps down
		{order, "a", -1, "c"}, // wraps up
		{order, "b", -1, "a"},
		{order, "gone", 1, "b"},  // unknown counts as row 0
		{order, "gone", -1, "c"}, // unknown counts as row 0
		{[]string{"a"}, "a", 1, "a"},
		{nil, "a", 1, ""},
	} {
		if got := move(tt.order, tt.selected, tt.dir); got != tt.want {
			t.Errorf("move(%q, %q, %d) = %q, want %q", tt.order, tt.selected, tt.dir, got, tt.want)
		}
	}
}

func TestInputButtonZone(t *testing.T) {
	for _, tt := range []struct {
		width int
		zone  []int // columns in the zone
	}{
		{16, []int{12, 13, 14}},
		{30, []int{26, 27, 28}},
		{15, nil},
	} {
		var got []int
		for x := range tt.width + 2 {
			if buttonZone(x, tt.width) {
				got = append(got, x)
			}
		}
		if !slices.Equal(got, tt.zone) {
			t.Errorf("width %d: zone %v, want %v", tt.width, got, tt.zone)
		}
	}
}

func TestInputRowAt(t *testing.T) {
	rows := []rowStart{{2, "a"}, {4, "b"}, {7, "c"}}
	for _, tt := range []struct {
		y         int
		session   string
		exact, ok bool
	}{
		{0, "", false, false}, // header
		{1, "", false, false},
		{2, "a", true, true},
		{3, "a", false, true}, // a's agent line
		{4, "b", true, true},
		{6, "b", false, true},
		{7, "c", true, true},
		{20, "c", false, true},
	} {
		s, exact, ok := rowAt(rows, tt.y)
		if s != tt.session || exact != tt.exact || ok != tt.ok {
			t.Errorf("rowAt(%d) = %q, %t, %t; want %q, %t, %t", tt.y, s, exact, ok, tt.session, tt.exact, tt.ok)
		}
	}
	if _, _, ok := rowAt(nil, 0); ok {
		t.Error("no rows: found one")
	}
}

// inputModel is a sidebar in session a (pane %0, content %1) among a, b and c, with client /dev/tty1 on a.
func inputModel(selected string) (Model, *actWorld) {
	w := &actWorld{sessions: []string{"a", "b", "c"}, panes: []map[string]string{
		actPane("a", "%0", "%0", "/"), actPane("a", "%1", "%0", "/"),
		actPane("b", "%2", "%3", "/"), actPane("b", "%3", "%3", "/"),
		actPane("c", "%4", "", "/"),
	}}
	return Model{c: w.client(), pane: "%0", selected: selected, snap: snapshot{
		sessions: []Session{{Name: "a", Group: "a"}, {Name: "b", Group: "b"}, {Name: "c", Group: "c"}},
		current:  "a", width: 30, height: 20, focused: true,
		clients: []Client{{TTY: "/dev/tty2", Session: "b", Activity: 9}, {TTY: "/dev/tty1", Session: "a", Activity: 1}},
	}}, w
}

// inputRun runs cmd as Bubble Tea would and returns its message.
func inputRun(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestInputMoveKeys(t *testing.T) {
	for _, tt := range []struct {
		key      tea.KeyMsg
		selected string
		want     string
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}, "a", "b"},
		{tea.KeyMsg{Type: tea.KeyDown}, "c", "a"},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}, "", "c"}, // no selection: from the current session
		{tea.KeyMsg{Type: tea.KeyUp}, "b", "a"},
	} {
		m, w := inputModel(tt.selected)
		next, cmd := m.handleKey(tt.key)
		if cmd != nil {
			t.Errorf("%s: unexpected command", tt.key)
		}
		if got := next.(Model).selected; got != tt.want {
			t.Errorf("%s from %q: selected %q, want %q", tt.key, tt.selected, got, tt.want)
		}
		if got, want := w.log(), []string{"set-option -gq @sidebar_selected_session " + tt.want}; !slices.Equal(got, want) {
			t.Errorf("%s: calls %q, want %q", tt.key, got, want)
		}
	}
}

func TestInputKeys(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	focus := []string{"list-panes -t =a: -F …", "select-pane -t %1", "switch-client -c /dev/tty1 -T root"}
	for _, tt := range []struct {
		name     string
		key      tea.KeyMsg
		selected string
		want     []string // the calls, or for enter on b the last one
	}{
		{"enter on the current session", tea.KeyMsg{Type: tea.KeyEnter}, "", focus},
		{"q", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}, "b", focus},
		{"d", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}, "b", []string{
			"switch-client -c /dev/tty1 -T root",
			"display-popup -c /dev/tty1 -E -w 80% -h 60% -b rounded -e SIDEBAR_SELECT_IN_POPUP=1 -- '" + exe + "'",
		}},
		{"+", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")}, "b", []string{
			"switch-client -c /dev/tty1 -T root",
			"display-popup -c /dev/tty1 -E -w 80% -h 60% -b rounded -e SIDEBAR_SELECT_IN_POPUP=1 -- '" + exe + "'",
		}},
		{"x", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}, "c", []string{
			"list-sessions -F …",
			"list-panes -t =c: -F …", "display-message -t %4 -p #{pane_current_path}",
			"list-sessions -F …", "kill-session -t =c:", "has-session -t =c:", "has-session -t =a:",
			"set-option -gq @sidebar_selected_session a",
		}},
		{"r", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, "b", []string{
			"command-prompt -t /dev/tty1 -I b -p Rename session: rename-session -t '=b:' -- '%%'",
		}},
		{"unbound key", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}, "b", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, w := inputModel(tt.selected)
			_, cmd := m.handleKey(tt.key)
			if msg := inputRun(cmd); msg != nil {
				t.Errorf("message %v, want none", msg)
			}
			if got := w.log(); !slices.Equal(got, tt.want) {
				t.Errorf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestInputEnterSwitches(t *testing.T) {
	m, w := inputModel("b")
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	inputRun(cmd)
	if got, want := w.log(), "switch-client -c /dev/tty1 -t =b:"; !slices.Contains(got, want) {
		t.Errorf("calls =\n%s\nwant %q among them", strings.Join(got, "\n"), want)
	}
}

func TestInputCtrlC(t *testing.T) {
	m, w := inputModel("")
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := inputRun(cmd).(tea.QuitMsg); !ok {
		t.Error("ctrl+c doesn't quit")
	}
	if len(w.calls) != 0 {
		t.Errorf("calls %q, want none", w.calls)
	}
}

func TestInputActionError(t *testing.T) {
	m, w := inputModel("")
	w.failCmd = "list-panes"
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	inputRun(cmd)
	got := w.log()
	if len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "display-message -c /dev/tty1 ❌ tmux list-panes") {
		t.Errorf("calls =\n%s\nwant the error flashed last", strings.Join(got, "\n"))
	}
}

func TestInputMouse(t *testing.T) {
	m, _ := inputModel("")
	rows := m.render().rows
	if len(rows) != 3 {
		t.Fatalf("rendered rows %v, want a, b and c", rows)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	click := func(x, y int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	}
	focus := []string{"list-panes -t =a: -F …", "select-pane -t %1", "switch-client -c /dev/tty1 -T root"}
	for _, tt := range []struct {
		name     string
		msg      tea.MouseMsg
		selected string // "" = unchanged
		want     []string
		switches bool // want is among the calls, after the selection and switchTo's ensure step
	}{
		{"+ on the header", click(27, 0), "", []string{
			"switch-client -c /dev/tty1 -T root",
			"display-popup -c /dev/tty1 -E -w 80% -h 60% -b rounded -e SIDEBAR_SELECT_IN_POPUP=1 -- '" + exe + "'",
		}, false},
		{"× on c's name row", click(26, rows[2].line), "", []string{
			"list-sessions -F …",
			"list-panes -t =c: -F …", "display-message -t %4 -p #{pane_current_path}",
			"list-sessions -F …", "kill-session -t =c:", "has-session -t =c:", "has-session -t =a:",
			"set-option -gq @sidebar_selected_session a",
		}, false},
		{"header", click(2, 0), "", nil, false},
		{"current session", click(2, rows[0].line), "a",
			append([]string{"set-option -gq @sidebar_selected_session a"}, focus...), false},
		{"below b's name row", click(2, rows[1].line+1), "b", []string{"switch-client -c /dev/tty1 -t =b:"}, true},
		{"× column below c's name row", click(26, rows[2].line+1), "c", []string{"switch-client -c /dev/tty1 -t =c:"}, true},
		{"right button", tea.MouseMsg{X: 2, Y: rows[1].line, Action: tea.MouseActionPress, Button: tea.MouseButtonRight}, "", nil, false},
		{"release", tea.MouseMsg{X: 2, Y: rows[1].line, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}, "", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, w := inputModel("")
			next, cmd := m.handleMouse(tt.msg)
			inputRun(cmd)
			if got := next.(Model).selected; got != tt.selected {
				t.Errorf("selected %q, want %q", got, tt.selected)
			}
			got := w.log()
			if tt.switches {
				if len(got) == 0 || got[0] != "set-option -gq @sidebar_selected_session "+tt.selected || !slices.Contains(got, tt.want[0]) {
					t.Errorf("calls =\n%s\nwant the selection, then %q", strings.Join(got, "\n"), tt.want[0])
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}
