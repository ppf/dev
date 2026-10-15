package sidebar

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/tmux"
)

// handleKey runs a key on the focused sidebar: j/k/↑/↓ move (wrapping), ⏎ switch, q back,
// d/+ picker, x kill, r rename, o open PR, ctrl+c quit.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c, client, current := m.c, m.client(), m.snap.current
	target := m.selected
	if target == "" {
		target = current
	}
	switch k.String() {
	case "j", "down":
		return m.moveSelection(client, target, 1), nil
	case "k", "up":
		return m.moveSelection(client, target, -1), nil
	case "enter":
		if target == current {
			return m, runAction(c, client, func() error { return focusContent(c, client, current) })
		}
		from := Origin{Session: current, Pane: m.pane}
		return m, runAction(c, client, func() error { return switchTo(c, client, target, from) })
	case "q":
		return m, runAction(c, client, func() error { return focusContent(c, client, current) })
	case "d", "+":
		return m, runAction(c, client, func() error { return OpenPicker(c, client) })
	case "x":
		return m, runAction(c, client, func() error { return kill(c, client, current, target) })
	case "r":
		return m, runAction(c, client, func() error { return rename(c, client, target) })
	case "o":
		return m, m.openPR(client, target)
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// handleMouse runs a left click: × kills its row, + on the header opens the picker, a PR tag opens its
// PR, any other line selects the session row at or above it, switching to it or focusing the current
// one's content.
func (m Model) handleMouse(ms tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ms.Action != tea.MouseActionPress || ms.Button != tea.MouseButtonLeft {
		return m, nil
	}
	c, client, current := m.c, m.client(), m.snap.current
	f := m.render()
	for _, l := range f.links {
		if ms.Y == l.line && ms.X >= l.from && ms.X < l.to {
			return m, runAction(c, client, func() error { return m.prs.open(l.url) })
		}
	}
	session, exact, ok := rowAt(f.rows, ms.Y)
	if buttonZone(ms.X, m.snap.width) {
		if ms.Y == 0 {
			return m, runAction(c, client, func() error { return OpenPicker(c, client) })
		}
		if ok && exact {
			return m, runAction(c, client, func() error { return kill(c, client, current, session) })
		}
	}
	if !ok {
		return m, nil
	}
	if err := m.setSelection(session); err != nil {
		showMessage(c, client, "❌ "+err.Error())
	}
	if session == current {
		return m, runAction(c, client, func() error { return focusContent(c, client, current) })
	}
	from := Origin{Session: current, Pane: m.pane}
	return m, runAction(c, client, func() error { return switchTo(c, client, session, from) })
}

// openPR opens target's PR in the browser, or says why there is none.
func (m Model) openPR(client, target string) tea.Cmd {
	var pr PR
	if i := slices.IndexFunc(m.snap.sessions, func(s Session) bool { return s.Name == target }); i >= 0 {
		pr = m.snap.sessions[i].PR
	}
	switch {
	case pr.Number != 0:
		return runAction(m.c, client, func() error { return m.prs.open(pr.URL) })
	case pr.Err != "":
		showMessage(m.c, client, "❌ "+pr.Err)
	default:
		showMessage(m.c, client, "No pull request for "+target)
	}
	return nil
}

// moveSelection selects the session dir rows from target in display order.
func (m Model) moveSelection(client, target string, dir int) Model {
	names := make([]string, len(m.snap.sessions))
	for i, s := range m.snap.sessions {
		names[i] = s.Name
	}
	next := move(names, target, dir)
	if next == "" {
		return m
	}
	if err := m.setSelection(next); err != nil {
		showMessage(m.c, client, "❌ "+err.Error())
	}
	return m
}

// runAction runs fn as a command, off the update loop since display-popup blocks until the popup closes,
// and flashes its error on client.
func runAction(c *tmux.Client, client string, fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			showMessage(c, client, "❌ "+err.Error())
		}
		return nil
	}
}

// move returns the session dir rows away from selected in order, wrapping; an unknown selection
// counts as row 0 (move_selection).
func move(order []string, selected string, dir int) string {
	n := len(order)
	if n == 0 {
		return ""
	}
	i := max(slices.Index(order, selected), 0)
	return order[((i+dir)%n+n)%n]
}

// buttonZone reports whether column x is in the ×/+ column [width-4, width-1); none below 16 columns.
func buttonZone(x, width int) bool {
	col := width - 4
	return col >= 12 && x >= col && x < col+3
}

// rowAt returns the session whose row starts at line y, or the last one above it (click_session).
func rowAt(rows []rowStart, y int) (session string, exact, ok bool) {
	for _, r := range rows {
		if r.line > y {
			break
		}
		session, exact, ok = r.session, r.line == y, true
	}
	return session, exact, ok
}
