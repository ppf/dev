package inbox

import (
	"errors"
	"strings"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// AskingFor is how long the manager's question counts as up without a pick: a dialog closed with Esc fires no hook.
const AskingFor = 30 * time.Minute

// Asking is item ID of Boss's list, reading Text, whose question the manager has up in its AskUserQuestion dialog
// since At (unix seconds): dev todo asking.
type Asking struct {
	Boss string `json:"boss"`
	ID   int    `json:"id"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

// SetAsking keeps a as the question the coordinator has up, in place of any earlier one; its next pick (Picked)
// clears it.
func (b Box) SetAsking(a Asking) error {
	return b.update(func(s *state) error {
		s.Asking = &a
		return nil
	})
}

// Asking is the question the coordinator has up at now, nil for none: none set, picked since, or set AskingFor ago.
func (b Box) Asking(now time.Time) (*Asking, error) {
	s, err := b.locked()
	if err != nil || s.Asking == nil || now.Sub(time.Unix(s.Asking.At, 0)) >= AskingFor {
		return nil, err
	}
	return s.Asking, nil
}

// ManagerAsking is the question the live Manager has up at now (Box.Asking), nil with no Manager session or its inbox off.
func ManagerAsking(c *tmux.Client, now time.Time) (*Asking, error) {
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(Manager)); err != nil {
		if strings.Contains(err.Error(), "can't find session") {
			return nil, nil
		}
		return nil, err
	}
	m, err := Recipient(c, Manager)
	if errors.Is(err, ErrInboxOff) || errors.Is(err, ErrNotBoss) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.Box.Asking(now)
}
