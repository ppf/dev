package inbox

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
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
	Pane       string `tmux:"pane_id"`
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
		agent.Deciding(st, r.WaitingFor) && held >= StuckDecision:
		return StuckNote(r.Session, st, held, r.Since), true
	}
	return Note{}, false
}

// Watch runs from the boss session pane is in (its previews): it queues a note for each of the boss's
// workers that is stuck, also recording it on the worker's pane (OptNote), then delivers the inbox if the
// coordinator is ready. Notes that waited for the coordinator to go idle with an empty input box get there
// this way too.
func Watch(c *tmux.Client, clk control.Clock, pane string) error {
	b, ok, err := Here(c, pane)
	if !ok || err != nil {
		return err
	}
	out, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(out)
	rows, err := tmux.Query[paneRow](c, "list-panes", "-a")
	if err != nil {
		return err
	}
	var noteErrs []error
	for _, r := range rows {
		if !sidebar.Names(r.StartedBy, id, b.Name, true) {
			continue
		}
		n, ok := r.stuck(clk.Now())
		if !ok {
			continue
		}
		if agent.Deciding(agent.Trusted(r.State, r.StateCmd, r.Command), r.WaitingFor) {
			// A dialog answered No or Esc fired no hook: it isn't unanswered
			st, err := control.ReadChecked(c, r.Pane)
			if err != nil && strings.Contains(err.Error(), "can't find pane") {
				continue // closed since list-panes
			}
			if err != nil {
				return err
			}
			if !agent.Deciding(st.Trusted(), st.WaitingFor) {
				continue
			}
		}
		if _, err := b.Box.Add(n); err != nil {
			return err
		}
		// Each round, so the minutes held stay current
		if err := setNote(c, r.Pane, n.Summary()); err != nil && !strings.Contains(err.Error(), "can't find pane") {
			noteErrs = append(noteErrs, err)
		}
	}
	if pending, err := b.Box.Pending(); !pending || err != nil {
		return errors.Join(append(noteErrs, err)...)
	}
	_, err = Deliver(c, clk, b)
	return errors.Join(append(noteErrs, err)...)
}
