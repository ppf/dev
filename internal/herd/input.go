package herd

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/tmux"
)

// actionMsg is an action's outcome to show on the bottom row; "" clears it.
type actionMsg struct{ msg string }

// handleKey: j/k/↓/↑ move (wrapping), ⏎ switch to the worker, o open its PR, x kill (asks y/n), ctrl+c quit.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	m.msg = ""
	if m.confirm != "" {
		target := m.confirm
		m.confirm = ""
		if key == "y" {
			return m, m.action(func() error {
				_, err := m.c.Run("kill-session", "-t", tmux.SessionTarget(target))
				return err
			})
		}
		return m, nil
	}
	i := index(m.snap.tiles, m.selected)
	switch key {
	case "j", "down":
		m.selected = m.move(i, 1)
	case "k", "up":
		m.selected = m.move(i, -1)
	case "enter":
		return m, m.switchTo(m.selected)
	case "o":
		return m.openPR(i)
	case "x":
		if len(m.snap.tiles) == 0 {
			return m, nil
		}
		if t := m.snap.tiles[i]; t.startedBy == "" || t.startedBy != m.snap.self {
			m.msg = t.sess.Name + " wasn't started from this herd: kill it from its sidebar"
		} else {
			m.confirm = t.sess.Name
		}
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// handleMouse: a left click selects a tile; a click on the selected tile switches to it.
func (m Model) handleMouse(ms tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ms.Action != tea.MouseActionPress || ms.Button != tea.MouseButtonLeft {
		return m, nil
	}
	i := m.tileAt(ms.X, ms.Y)
	if i < 0 {
		return m, nil
	}
	name := m.snap.tiles[i].sess.Name
	if name == m.selected {
		return m, m.switchTo(name)
	}
	m.selected = name
	return m, nil
}

// move is the session dir tiles away from tile i, wrapping.
func (m Model) move(i, dir int) string {
	n := len(m.snap.tiles)
	if n == 0 {
		return ""
	}
	return m.snap.tiles[((i+dir)%n+n)%n].sess.Name
}

// switchTo switches the client viewing the herd to session.
func (m Model) switchTo(session string) tea.Cmd {
	if session == "" {
		return nil
	}
	args := []string{"switch-client"}
	if m.snap.client != "" {
		args = append(args, "-c", m.snap.client)
	}
	return m.action(func() error {
		_, err := m.c.Run(append(args, "-t", tmux.SessionTarget(session))...)
		return err
	})
}

// openPR opens tile i's PR, or says why there is none.
func (m Model) openPR(i int) (tea.Model, tea.Cmd) {
	if len(m.snap.tiles) == 0 {
		return m, nil
	}
	t := m.snap.tiles[i]
	switch pr := t.sess.PR; {
	case pr.Number != 0:
		return m, m.action(func() error { return m.look.OpenPR(pr.URL) })
	case pr.Err != "":
		m.msg = pr.Err
	default:
		m.msg = "No pull request for " + t.sess.Name
	}
	return m, nil
}

// action runs fn off the update loop and reports its error on the bottom row.
func (m Model) action(fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return actionMsg{"❌ " + err.Error()}
		}
		return actionMsg{}
	}
}
