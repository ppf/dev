package herd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/tmux"
)

// The compact view: per worker its tile header, then its latest note in faint, no screen.
func TestCompactRender(t *testing.T) {
	m := twoTiles()
	m.compact = true
	m.snap.tiles[0].note = "needs a decision: Claude needs your permission to use Bash"
	got := m.render()
	want := []string{
		" ● a Claude · waiting · $1.20",
		"   needs a decision: Claude needs your p",
		" ● b Codex #3 open fix",
		"", "", "", "", "", "", "", "",
		" j/k ⏎ switch  v view  i reply  o PR  x…",
	}
	if s := ansiStrip(got); !slices.Equal(trimRight(s), want) {
		t.Fatalf("compact view:\n%s", strings.Join(s, "\n"))
	}
	if !strings.HasPrefix(got[0], waitingBG) || !strings.HasPrefix(got[1], reset+rule) {
		t.Errorf("waiting header %q, note %q", got[0], got[1])
	}
	for i, l := range got[:3] {
		if ansi.StringWidth(l) != 40 {
			t.Errorf("line %d is %d wide", i, ansi.StringWidth(l))
		}
	}
	// Clicks land on the rows' workers, also on a note row
	for y, want := range map[int]int{0: 0, 1: 0, 2: 1, 3: -1} {
		if got := m.tileAt(5, y); got != want {
			t.Errorf("tileAt row %d = %d, want %d", y, got, want)
		}
	}
	// The reply box's worker in its accent, the others dimmed
	m.open(m.snap.tiles[1])
	got = m.render()
	if !strings.HasPrefix(got[0], headerBG+rule) || !strings.HasPrefix(got[2], "\x1b[48;") || strings.HasPrefix(got[2], headerBG) {
		t.Errorf("box open: %q / %q", got[0], got[2])
	}
}

func trimRight(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " ")
	}
	return out
}

// A list taller than the pane starts at the tile that keeps the selection in view.
func TestCompactScrolls(t *testing.T) {
	m := twoTiles()
	m.compact = true
	for _, n := range []string{"c", "d", "e"} {
		tl := m.snap.tiles[1]
		tl.sess.Name, tl.note = n, "finished: "+n
		m.snap.tiles = append(m.snap.tiles, tl)
	}
	m.height = 5 // 4 rows above the hints
	tests := []struct {
		selected string
		want     []int
	}{
		{"a", []int{0, 1, 2, 2}},
		{"c", []int{0, 1, 2, 2}},
		{"d", []int{2, 2, 3, 3}},
		{"e", []int{3, 3, 4, 4}},
	}
	for _, tt := range tests {
		m.selected = tt.selected
		if got := m.listRows(4); !slices.Equal(got, tt.want) {
			t.Errorf("selected %s: rows %v, want %v", tt.selected, got, tt.want)
		}
	}
	got := m.render()
	if s := ansi.Strip(got[0]); !strings.HasPrefix(s, " ● d ") {
		t.Errorf("first row %q", s)
	}
	if s := ansi.Strip(got[4]); !strings.HasPrefix(s, " 5/5  ") {
		t.Errorf("bottom row %q", s)
	}
	m.snap.tiles = m.snap.tiles[:2]
	if s := ansi.Strip(m.render()[4]); strings.Contains(s, "/2") {
		t.Errorf("a list that fits has no position: %q", s)
	}
}

// v toggles the view, records it on the boss session and resizes the pane: compact to 30% of the window
// (at least minTileW columns, at most half), full back to 55%.
func TestToggleView(t *testing.T) {
	var ran []string
	width := "200"
	m := twoTiles()
	m.pane = "%2"
	m.c = tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return width + "\n", nil
	})
	press := func(m Model) Model {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
		if msg := cmd().(actionMsg); msg.msg != "" {
			t.Fatal(msg.msg)
		}
		return next.(Model)
	}
	if m = press(m); !m.compact {
		t.Fatal("v doesn't switch to compact")
	}
	want := []string{"display-message -p -t %2 #{window_width}", "set-option -t %2 @dev_boss_view compact ; resize-pane -t %2 -x 60"}
	if !slices.Equal(ran, want) {
		t.Errorf("compact ran %q", ran)
	}
	for w, cols := range map[string]string{"80": "30", "50": "25"} {
		ran, width = nil, w
		m.compact = false
		press(m)
		if !strings.HasSuffix(ran[1], "-x "+cols) {
			t.Errorf("window %s: %q, want %s columns", w, ran[1], cols)
		}
	}
	ran, width, m.compact = nil, "200", true
	if m = press(m); m.compact || ran[1] != "set-option -u -t %2 @dev_boss_view ; resize-pane -t %2 -x 110" {
		t.Errorf("full: compact %v, ran %q", m.compact, ran)
	}
}

// A previews pane starts in the view its boss session keeps; one it can't read starts full, saying why.
func TestNewModelView(t *testing.T) {
	for view, compact := range map[string]bool{"compact\n": true, "\n": false} {
		if m := newModel(tmux.NewFake(func(args []string) (string, error) { return view, nil }), "%2"); m.compact != compact || m.msg != "" {
			t.Errorf("view %q: compact %v, msg %q", view, m.compact, m.msg)
		}
	}
	m := newModel(tmux.NewFake(func(args []string) (string, error) { return "", errors.New("no server") }), "%2")
	if m.compact || !strings.HasPrefix(m.msg, "❌ view: ") || !strings.HasSuffix(m.msg, "no server") {
		t.Errorf("unreadable view: compact %v, msg %q", m.compact, m.msg)
	}
}

// The compact view captures only deciding workers (for their dialogs' picker), and keeps no screen; each
// tile gets its pane's note.
func TestRefreshCompact(t *testing.T) {
	r := tmux.FormatRow
	rows := r("herd", "$1", "1", "1", "%1", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "")
	rows += r("w1", "$2", "1", "1", "%w1", "/w1", "", "claude", "idle", "claude", "", "", "", "", "herd", "", "", "", "", "", "finished: done")
	rows += r("w2", "$3", "1", "1", "%w2", "/w2", "", "claude", "waiting", "claude", "", "", "", "", "herd", "permission_prompt", "", "", "", "", "")
	open := strings.Repeat("─", 40) + "\n Bash command\n\n   rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	var captured []string
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return rows, nil
		case "list-clients":
			return r("/dev/ttys1", "herd", "5"), nil
		}
		captured = append(captured, args[len(args)-1])
		return open, nil
	})
	m := Model{c: c, pane: "%1", look: &fakeLookups{}, width: 40, height: 20, compact: true}
	msg := m.refresh()().(refreshMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	tl := msg.snap.tiles
	if !slices.Equal(captured, []string{"%w2"}) || tl[0].note != "finished: done" || tl[1].pick == nil || tl[0].body != nil || tl[1].body != nil {
		t.Errorf("captured %q, tiles %+v", captured, tl)
	}
}
