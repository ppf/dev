package herd

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

const (
	// envState carries the previews' UI state across a reload's exec
	envState = "DEV_PREVIEWS_STATE"
	// maxState bounds envState, dropping the oldest replies, so the exec doesn't fail on the environment's size
	maxState = 32 << 10
)

// carried is the UI state a reload keeps: the board scrolls to the selection, so that keeps its scroll too.
type carried struct {
	Selected   string          `json:"selected,omitempty"`
	Folds      map[string]bool `json:"folds,omitempty"`
	Expands    map[string]bool `json:"expands,omitempty"`
	TodoFolded bool            `json:"todo_folded,omitempty"`
	WaitOpen   bool            `json:"wait_open,omitempty"`
	History    []string        `json:"history,omitempty"`
	Size       paneSize        `json:"size,omitempty"`
	Msg        string          `json:"msg,omitempty"`
	Alert      bool            `json:"alert,omitempty"`
}

// Run shows the previews in pane until its pane closes or Ctrl-C. When a new release of exe is installed it
// re-execs itself with args at an idle point (same pid, same pane), its UI state carried; an exec that fails
// keeps this version running, the error on the bottom row. It never returns to reload: a clean exit closes the boss.
func Run(c *tmux.Client, pane, exe string, args []string) error {
	own := selfreload.Version()
	m := newModel(c, pane)
	if selfreload.ReloadedTo(own) != "" {
		m = m.restore(os.Getenv(envState))
	}
	if err := os.Unsetenv(envState); err != nil {
		return fmt.Errorf("boss: %w", err)
	}
	o := selfreload.Options{Exe: exe, Own: own, Tmux: c}
	if err := selfreload.RunPane(m, func(m *Model) *selfreload.Pane { return &m.reload }, o, args, Model.carry); err != nil {
		return fmt.Errorf("boss: %w", err)
	}
	return nil
}

// idle: nothing typed, asked, open or in flight would be lost by a reload (Busy counts the commands running).
// A draft kept for another card holds it too, until sent or cleared.
func (m Model) idle() bool {
	return m.box.session == "" && !m.drafted() && m.confirm == "" && m.answering == "" && !m.needMode
}

// drafted reports a kept draft that can still be sent: its session is on the server (only gone from the board,
// it may come back); one for a closed session can't, and holds nothing.
func (m Model) drafted() bool {
	for name := range m.drafts {
		if slices.ContainsFunc(m.snap.all, func(s sidebar.Session) bool { return s.Name == name }) {
			return true
		}
	}
	return false
}

// held says what holds a ready reload, for the bottom row; "" when none is held.
func (m Model) held() string {
	v := m.reload.Held()
	if v == "" {
		return ""
	}
	why := "when idle"
	switch {
	case m.needMode:
		why = "after TO DO (esc)"
	case m.answering != "":
		why = "after answering " + m.answering
	case m.drafted():
		why = "once the unsent message goes"
	}
	return "↻ " + v + " reloads " + why
}

// carry is the reload's environment: the UI state as envState.
func (m Model) carry() []string {
	c := carried{Selected: m.selected, Folds: m.folds, Expands: m.expands, TodoFolded: m.todoFolded, WaitOpen: m.waitOpen,
		History: m.history, Size: m.size, Msg: m.msg.text, Alert: m.msg.alert}
	for {
		b, err := json.Marshal(c)
		if err != nil {
			panic(err) // maps and strings only
		}
		if len(b) <= maxState || len(c.History) == 0 {
			return []string{envState + "=" + string(b)}
		}
		c.History = c.History[1:]
	}
}

// restore is m with the UI state a reload carried in s; one not read is said on the bottom row.
func (m Model) restore(s string) Model {
	if s == "" {
		return m
	}
	var c carried
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		m.reload.Err = fmt.Errorf("reload: UI state not carried: %w", err)
		return m
	}
	m.selected, m.folds, m.expands, m.todoFolded, m.waitOpen, m.history = c.Selected, c.Folds, c.Expands, c.TodoFolded, c.WaitOpen, c.History
	m.msg = message{text: c.Msg, alert: c.Alert}
	m.restoredSize, m.restored = c.Size, true
	return m
}
