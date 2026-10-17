package herd

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// actionMsg is an action's outcome to show on the bottom row; "" clears it.
type actionMsg struct{ msg string }

// handleKey: j/k/↓/↑ move (wrapping), ⏎ switch to the worker, i reply to it here, 1-9 answer a waiting
// worker's dialog, o open its PR, x kill (asks y/n), ctrl+c quit.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.reply != "" {
		return m.typeReply(k)
	}
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
	case "i":
		if len(m.snap.tiles) > 0 {
			t := m.snap.tiles[i]
			m.reply, m.replyPane, m.draft = t.sess.Name, t.pane, nil
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if len(m.snap.tiles) == 0 {
			return m, nil
		}
		t := m.snap.tiles[i]
		if t.sess.State != agent.Waiting {
			m.msg = "1-9 answer a waiting worker's dialog; " + t.sess.Name + " isn't waiting (i to reply)"
			return m, nil
		}
		return m, m.action(func() error { return control.SendKeys(m.c, t.pane, []string{key}) })
	case "o":
		return m.openPR(i)
	case "x":
		if len(m.snap.tiles) == 0 {
			return m, nil
		}
		// The previews run in a boss
		if t := m.snap.tiles[i]; !sidebar.Names(t.startedBy, m.snap.self, m.snap.name, true) {
			m.msg = t.sess.Name + " wasn't started from this boss: kill it from its sidebar"
		} else {
			m.confirm = t.sess.Name
		}
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// handleMouse: a left click on a tile selects it and opens the reply box for it, so the worker is answered
// in place (⏎ without a reply open switches to it).
func (m Model) handleMouse(ms tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ms.Action != tea.MouseActionPress || ms.Button != tea.MouseButtonLeft {
		return m, nil
	}
	i := m.tileAt(ms.X, ms.Y)
	if i < 0 {
		return m, nil
	}
	t := m.snap.tiles[i]
	if t.sess.Name != m.reply {
		// A click on another tile moves the reply box there; one on the same keeps the draft
		m.selected, m.reply, m.replyPane, m.draft = t.sess.Name, t.sess.Name, t.pane, nil
	}
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

// typeReply edits the reply on the bottom row: ⏎ sends it to the worker as a prompt (as dev agent prompt
// does, so one waiting on a decision refuses it), esc cancels, backspace and ctrl+u delete, ctrl+c quits.
func (m Model) typeReply(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.reply, m.draft = "", nil
	case tea.KeyEnter:
		text, target, pane := string(m.draft), m.reply, m.replyPane
		m.reply, m.draft = "", nil
		if strings.TrimSpace(text) == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			// The worker may have exited while the reply was typed: its pane's shell would run the text
			if a, err := control.Resolve(m.c, target); err != nil || a.Pane != pane {
				return actionMsg{"❌ " + target + ": its agent is gone, reply not sent"}
			}
			if err := control.Prompt(m.c, control.RealClock, pane, text); err != nil {
				return actionMsg{"❌ " + target + ": " + err.Error()}
			}
			return actionMsg{"Sent to " + target}
		}
	case tea.KeyBackspace:
		if len(m.draft) > 0 {
			m.draft = m.draft[:len(m.draft)-1]
		}
	case tea.KeyCtrlU:
		m.draft = nil
	case tea.KeySpace:
		m.draft = append(m.draft, ' ')
	case tea.KeyRunes:
		m.draft = append(m.draft, k.Runes...)
	}
	return m, nil
}
