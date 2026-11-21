package sidebar

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/tmux"
)

// Run shows the sidebar in pane until the process is killed (its pane closes) or Ctrl-C. When a new release
// of exe is installed it re-execs itself with args at an idle point (same pid, same pane); an exec that fails
// keeps this version running, the error on the bottom row. It never returns to reload.
func Run(c *tmux.Client, pane, exe string, args []string) error {
	o := selfreload.Options{Exe: exe, Own: selfreload.Version(), Tmux: c}
	if err := selfreload.RunPane(newModel(c, pane), func(m *Model) *selfreload.Pane { return &m.reload }, o, args, nil); err != nil {
		return fmt.Errorf("sidebar: %w", err)
	}
	return nil
}

// idle: nothing the user started is in flight or open, so a reload loses nothing.
func (m Model) idle() bool {
	return !m.answering && !m.needMode && !m.needOpen && m.ticked == ""
}

// runAction runs fn as a command, off the update loop since display-popup blocks until the popup closes,
// and flashes its error on client. It counts as running from now, so a reload waits for it.
func (m Model) runAction(client string, fn func() error) tea.Cmd {
	c := m.c
	return m.reload.Busy.Run(func() tea.Msg {
		if err := fn(); err != nil {
			showMessage(c, client, "❌ "+err.Error())
		}
		return nil
	})
}
