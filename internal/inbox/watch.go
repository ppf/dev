package inbox

import (
	"strconv"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
)

// Stuck thresholds: a worker working this long, or waiting this long on a decision nobody answered.
const (
	StuckWorking  = 20 * time.Minute
	StuckDecision = 3 * time.Minute
)

// paneRow is one list-panes -a row for Watch.
type paneRow struct {
	Session    string `tmux:"session_name"`
	StartedBy  string `tmux:"@dev_started_by"`
	Command    string `tmux:"pane_current_command"`
	State      string `tmux:"@agent_state"`
	StateCmd   string `tmux:"@agent_cmd"`
	WaitingFor string `tmux:"@agent_waiting_for"`
	Since      string `tmux:"@agent_since"`
}

// stuck is r's stuck note at now, ok=false when r isn't stuck.
func (r paneRow) stuck(now time.Time) (Note, bool) {
	sec, err := strconv.ParseInt(r.Since, 10, 64)
	if err != nil {
		return Note{}, false
	}
	held := now.Sub(time.Unix(sec, 0))
	switch st := agent.Trusted(r.State, r.StateCmd, r.Command); {
	case st == agent.Working && held >= StuckWorking,
		st == agent.Waiting && r.WaitingFor != agent.IdlePrompt && held >= StuckDecision:
		return StuckNote(r.Session, st, held, r.Since), true
	}
	return Note{}, false
}

// Watch runs from the boss session pane is in (its previews): it queues a note for each of the boss's
// workers that is stuck, then delivers the inbox if the coordinator is ready. Notes that waited for the
// coordinator to go idle with an empty input box get there this way too.
func Watch(c *tmux.Client, clk control.Clock, pane string) error {
	b, ok, err := Here(c, pane)
	if !ok || err != nil {
		return err
	}
	id, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return err
	}
	rows, err := tmux.Query[paneRow](c, "list-panes", "-a")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.StartedBy+"\n" != id {
			continue
		}
		if n, ok := r.stuck(clk.Now()); ok {
			if _, err := b.Box.Add(n); err != nil {
				return err
			}
		}
	}
	if pending, err := b.Box.Pending(); !pending || err != nil {
		return err
	}
	_, err = Deliver(c, clk, b)
	return err
}
