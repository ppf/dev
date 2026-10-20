package inbox

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// Stuck thresholds: a worker working this long, or waiting this long on a decision nobody answered.
const (
	StuckWorking  = 20 * time.Minute
	StuckDecision = 3 * time.Minute
)

// paneRow is one list-panes row for Watch and WorkerHook.
type paneRow struct {
	Session    string `tmux:"session_name"`
	Window     int    `tmux:"window_index"`
	Pane       string `tmux:"pane_id"`
	Sidebar    string `tmux:"@sidebar_pane"`
	StartedBy  string `tmux:"@dev_started_by"`
	Command    string `tmux:"pane_current_command"`
	State      string `tmux:"@agent_state"`
	StateCmd   string `tmux:"@agent_cmd"`
	WaitingFor string `tmux:"@agent_waiting_for"`
	Since      string `tmux:"@agent_since"`
	Prompts    string `tmux:"@agent_prompts"`
	// A rate-limited worker's scheduled resume (scheduleResume)
	ResumeAt      string `tmux:"@dev_resume_at"`
	ResumePrompts string `tmux:"@dev_resume_prompts"`
	ResumeSession string `tmux:"@dev_resume_session"`
	AgentSession  string `tmux:"@agent_session"`
	Resumes       string `tmux:"@dev_resumes"`
}

// teammates is the set of panes in rows that aren't their session's agent pane (agent.Detect, as dev agent
// get picks it), in sessions that have one: an agent team's teammates, which send only their decisions.
func teammates(rows []paneRow) map[string]bool {
	panes := map[string][]agent.Pane{}
	sidebars := map[string]string{}
	for _, r := range rows {
		panes[r.Session] = append(panes[r.Session], agent.Pane{Session: r.Session, Window: r.Window, ID: r.Pane,
			Command: r.Command, State: r.State, StateCmd: r.StateCmd})
		sidebars[r.Session] = r.Sidebar
	}
	mates := map[string]bool{}
	for session, ps := range panes {
		a, ok := agent.Detect(ps, sidebars[session])
		for _, p := range ps {
			mates[p.ID] = ok && p.ID != a.Pane.ID
		}
	}
	return mates
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

// Watch runs from the boss session pane is in (its previews): it resumes the boss's workers whose rate
// limit reset (resume) and queues a note for each of its workers that is stuck or resumed, also recording
// it on the worker's pane (OptNote), then delivers the inbox if the coordinator is ready. Notes that waited
// for the coordinator to go idle with an empty input box get there this way too.
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
	mates := teammates(rows)
	var noteErrs []error
	for _, r := range rows {
		if !sidebar.Names(r.StartedBy, id, b.Name, true) || mates[r.Pane] {
			continue
		}
		if r.ResumeAt != "" {
			resumed, err := resume(c, clk, r)
			if resumed {
				n := Note{Worker: r.Session, Kind: Resumed, Text: "its rate limit reset, dev sent it: continue", Key: Resumed + "\x00" + r.ResumeAt}
				if _, err := b.Box.Add(n); err != nil {
					return err
				}
				noteErrs = append(noteErrs, setNote(c, r.Pane, n.Summary()))
			}
			if err != nil && !strings.Contains(err.Error(), "can't find pane") {
				// The boss was told dev resumes it
				n := Note{Worker: r.Session, Kind: Failed, Text: clean("resuming it after its rate limit: " + err.Error()), Key: Failed + "\x00resume" + r.ResumeAt}
				if _, err := b.Box.Add(n); err != nil {
					return err
				}
			}
			if resumed || err != nil {
				continue
			}
		}
		n, ok := r.stuck(clk.Now())
		if !ok {
			continue
		}
		// A dialog answered No or Esc, or a turn Esc ended, fired no hook: the check records it, moving @agent_since
		st, err := control.ReadChecked(c, r.Pane)
		if err != nil && strings.Contains(err.Error(), "can't find pane") {
			continue // closed since list-panes
		}
		if err != nil {
			return err
		}
		if st.Since != r.Since {
			continue
		}
		shot, err := c.Run("capture-pane", "-p", "-t", r.Pane)
		if err != nil && strings.Contains(err.Error(), "can't find pane") {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := b.Box.AddStuck(n, screen.Snapshot(shot)); err != nil {
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
