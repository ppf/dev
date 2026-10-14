// Package agent records Claude hook state on tmux panes and detects a session's agent pane.
package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// Hook states passed to `dev state`.
const (
	Working    = "working"
	Waiting    = "waiting"
	Idle       = "idle"
	Clear      = "clear"
	ToolFailed = "tool-failed"
)

// Resolve maps a hook state and the hook's stdin JSON to the state to record.
// ok=false means leave the pane alone (subagent events). Malformed JSON is an error.
func Resolve(state string, input []byte) (resolved string, ok bool, err error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", false, fmt.Errorf("agent: hook input: %w", err)
	}
	if state != Working && state != ToolFailed {
		return state, true, nil
	}
	// Subagent tool events would mask the main thread's waiting/idle state
	if _, sub := fields["agent_id"]; sub {
		return "", false, nil
	}
	// Esc during a tool ends the turn without a Stop event
	if state == ToolFailed {
		if string(fields["is_interrupt"]) == "true" {
			return Idle, true, nil
		}
		return Working, true, nil
	}
	return state, true, nil
}

// Record writes state to @agent_state and the pane's current command to @agent_cmd in one chained
// tmux call; Clear unsets both.
func Record(c *tmux.Client, pane, state string) error {
	var err error
	if state == Clear {
		_, err = c.Run("set-option", "-pu", "-t", pane, "@agent_state", ";", "set-option", "-pu", "-t", pane, "@agent_cmd")
	} else {
		// @agent_cmd ties the state to the writing process, so a crash's leftover is ignored by the sidebar
		_, err = c.Run("set-option", "-p", "-t", pane, "@agent_state", state, ";",
			"set-option", "-pF", "-t", pane, "@agent_cmd", "#{pane_current_command}")
	}
	return err
}

// Pane is a pane as the sidebar lists it (list-panes -a).
type Pane struct {
	Session  string `tmux:"session_name"`
	Window   int    `tmux:"window_index"`
	ID       string `tmux:"pane_id"`
	Path     string `tmux:"pane_current_path"`
	Title    string `tmux:"pane_title"`
	Command  string `tmux:"pane_current_command"`
	State    string `tmux:"@agent_state"`
	StateCmd string `tmux:"@agent_cmd"`
}

// Agent is a session's detected agent pane.
type Agent struct {
	Pane  Pane
	Label string // "Claude" or "Codex"
	State string // hook state, only when @agent_cmd matches the pane's command; else ""
}

// Detect finds the agent among one session's panes: in window 1, excluding sidebarPane, the pane whose
// command equals its @agent_cmd (hook-registered Claude), else one whose command is codex or codex-*.
func Detect(panes []Pane, sidebarPane string) (Agent, bool) {
	var codex *Pane
	for i := range panes {
		p := &panes[i]
		if p.Window != 1 || p.ID == sidebarPane {
			continue
		}
		if isCodex(p.Command) {
			if codex == nil {
				codex = p
			}
			continue
		}
		if p.StateCmd != "" && p.Command == p.StateCmd {
			return Agent{Pane: *p, Label: "Claude", State: p.State}, true
		}
	}
	if codex == nil {
		return Agent{}, false
	}
	a := Agent{Pane: *codex, Label: "Codex"}
	if codex.StateCmd == codex.Command {
		a.State = codex.State
	}
	return a, true
}

func isCodex(command string) bool {
	return command == "codex" || strings.HasPrefix(command, "codex-")
}
