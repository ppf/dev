package herd

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/tmux"
)

const (
	// envState carries the previews' UI state across a reload's exec
	envState = "DEV_PREVIEWS_STATE"
	// maxState is envState's bound, kept by dropping the oldest replies, then the bottom row's alert; folds, expands
	// and kept replies always go
	maxState = 32 << 10
)

// carried is the UI state a reload keeps: the board scrolls to the selection, so that keeps its scroll too.
type carried struct {
	Selected   string           `json:"selected,omitempty"`
	Folds      map[string]bool  `json:"folds,omitempty"`
	Expands    map[string]bool  `json:"expands,omitempty"`
	TodoFolded bool             `json:"todo_folded,omitempty"`
	WaitOpen   bool             `json:"wait_open,omitempty"`
	History    []string         `json:"history,omitempty"`
	Size       paneSize         `json:"size,omitempty"`
	Msg        string           `json:"msg,omitempty"`
	Alert      bool             `json:"alert,omitempty"`
	Drafts     map[string]draft `json:"drafts,omitempty"`
}

// draft is a reply kept for a card (Model.drafts).
type draft struct {
	Pane string `json:"pane"`
	Text string `json:"text"`
	Pos  int    `json:"pos"`
}

// Run shows the previews in pane until its pane closes or Ctrl-C. When a new release of exe is installed it
// re-execs itself with args at an idle point (same pid, same pane), its UI state carried; an exec that fails
// keeps this version running, the error on the bottom row. It never returns to reload: a clean exit closes the boss.
func Run(c *tmux.Client, pane, exe string, args []string) error {
	own := selfreload.Version()
	m := newModel(c, pane)
	switch s := os.Getenv(envState); {
	case selfreload.ReloadedTo(own) != "":
		m = m.restore(s)
	case s != "":
		m.reload.Err = fmt.Errorf("reload: UI state not carried: made for another version than %s", own)
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

// idle: nothing typed, asked, open or in flight would be lost by a reload (Busy counts the commands running);
// replies kept for cards are carried.
func (m Model) idle() bool {
	return m.box.session == "" && m.confirm == "" && m.answering == "" && !m.needMode && m.fits()
}

// fits reports the carried state within maxState: a longer one (a long kept reply) could fail the exec on the environment's size.
func (m Model) fits() bool {
	return len(m.carry()[0]) <= len(envState)+1+maxState
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
	case !m.fits():
		why = "once the long kept message goes"
	}
	return "↻ " + v + " reloads " + why
}

// carry is the reload's environment: the UI state as envState.
func (m Model) carry() []string {
	c := carried{Selected: m.selected, Folds: m.folds, Expands: m.expands, TodoFolded: m.todoFolded, WaitOpen: m.waitOpen,
		History: m.history, Size: m.size}
	if m.msg.alert {
		// An info line would hide the flash of the version just reloaded into
		c.Msg, c.Alert = m.msg.text, true
	}
	for name, b := range m.drafts {
		if !slices.ContainsFunc(m.snap.tiles, func(t tile) bool { return t.sess.Name == name && t.pane == b.pane }) {
			continue // its card is gone or its pane replaced: open won't bring it back
		}
		if c.Drafts == nil {
			c.Drafts = map[string]draft{}
		}
		c.Drafts[name] = draft{Pane: b.pane, Text: string(b.ed.text), Pos: b.ed.pos}
	}
	for {
		b, err := json.Marshal(c)
		if err != nil {
			panic(err) // maps and strings only
		}
		switch {
		case len(b) <= maxState:
			return []string{envState + "=" + string(b)}
		case len(c.History) > 0:
			c.History = c.History[1:]
		case c.Msg != "":
			c.Msg, c.Alert = "", false
		default:
			return []string{envState + "=" + string(b)}
		}
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
	for name, d := range c.Drafts {
		if m.drafts == nil {
			m.drafts = map[string]replyBox{}
		}
		text := []rune(d.Text)
		m.drafts[name] = replyBox{session: name, pane: d.Pane, ed: editor{text: text, pos: min(max(d.Pos, 0), len(text))}, hist: -1}
	}
	return m
}
