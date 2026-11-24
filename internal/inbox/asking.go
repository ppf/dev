package inbox

import (
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// AskingFor is how long the manager's question counts as up without a pick: a dialog closed with Esc fires no hook.
const AskingFor = 30 * time.Minute

// Asking is item ID of Boss's list, reading Text, whose question the manager has up in its AskUserQuestion dialog
// since At (unix seconds): dev todo asking.
type Asking struct {
	Boss  string `json:"boss"`
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Added int64  `json:"added"` // the item's Added when marked: a reopened or replaced item has another
	At    int64  `json:"at"`
}

// SetAsking keeps as, the items of the dialog the coordinator has up, in place of any earlier ones; its next pick
// (Picked) clears them.
func (b Box) SetAsking(as []Asking) error {
	return b.update(func(s *state) error {
		s.Asking = as
		return nil
	})
}

// ClearAsking drops the question the coordinator had up, as its dialog is gone (a resumed session).
func (b Box) ClearAsking() error {
	return b.update(func(s *state) error {
		s.Asking = nil
		return nil
	})
}

// Asking is the items whose questions the coordinator has up at now, none when none set, picked since, or set
// AskingFor ago.
func (b Box) Asking(now time.Time) ([]Asking, error) {
	s, err := b.locked()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(s.Asking, func(a Asking) bool { return now.Sub(time.Unix(a.At, 0)) >= AskingFor }), nil
}

// ManagerAsking is the items whose questions the live Manager has up at now (Box.Asking), its inbox on or off: none
// with no tmux server or no Manager session.
func ManagerAsking(c *tmux.Client, now time.Time) ([]Asking, error) {
	// display-message prints nothing for a missing session, without failing
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(Manager)); err != nil {
		if tmux.IsNoServer(err) || strings.Contains(err.Error(), "can't find session") {
			return nil, nil
		}
		return nil, err
	}
	v, err := view(c, tmux.SessionTarget(Manager))
	if err != nil {
		return nil, err
	}
	// Its inbox off too: dev todo asking writes it all the same
	v.Off = ""
	m, ok, err := v.boss()
	if !ok || err != nil {
		return nil, err
	}
	return m.Box.Asking(now)
}
