// Package agent records Claude and Codex hook state on tmux panes, detects a session's agent pane and
// gives the command that reopens a saved Claude conversation.
package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// IdlePrompt is the Notification type Claude sends after a minute idle: waiting, but safe to prompt.
const IdlePrompt = "idle_prompt"

// Hook is the part of a hook's input Record needs besides the Session.
type Hook struct {
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
}

// ParseHook reads a hook's input; malformed input gives the zero Hook (Resolve reports it).
func ParseHook(input []byte) Hook {
	var h Hook
	_ = json.Unmarshal(input, &h)
	return h
}

// Session is the conversation a hook event belongs to.
type Session struct {
	ID         string `json:"session_id"`
	Transcript string `json:"transcript_path"`
}

// safeID is a session id that can be typed into a shell as one argument (Claude's are UUIDs).
var safeID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]*$`)

// HookSession reads the session from hook stdin that Resolve accepted; an id unsafe to type into a shell,
// or a transcript path that would break the tab-separated resurrect state, gives no session.
func HookSession(input []byte) Session {
	var s Session
	if json.Unmarshal(input, &s) != nil || !safeID.MatchString(s.ID) || strings.ContainsAny(s.Transcript, "\t\n") {
		return Session{}
	}
	return s
}

// Record writes state to @agent_state, the pane's current command to @agent_cmd and s to @agent_session
// and @agent_transcript in one chained tmux call, bumping @agent_prompts on UserPromptSubmit and setting
// @agent_waiting_for to why a waiting state waits (Claude's notification type, else the event: Codex's
// PermissionRequest); Clear (or no session) unsets the session options.
func Record(c *tmux.Client, pane, state string, s Session, h Hook) error {
	unset := func(option string) []string { return []string{"set-option", "-pu", "-t", pane, option} }
	set := func(option, value string) []string { return []string{"set-option", "-p", "-t", pane, option, value} }
	var cmds [][]string
	if state == Clear {
		// @agent_prompts stays: a count that restarted could repeat the value a waiter read before /clear
		s = Session{}
		cmds = [][]string{unset("@agent_state"), unset("@agent_cmd")}
	} else {
		// @agent_cmd ties the state to the writing process, so a crash's leftover is ignored by the sidebar
		cmds = [][]string{set("@agent_state", state), {"set-option", "-pF", "-t", pane, "@agent_cmd", "#{pane_current_command}"}}
		if h.Event == "UserPromptSubmit" {
			// Counts prompts, so `dev agent prompt --wait` sees a turn that ended between two polls
			cmds = append(cmds, []string{"set-option", "-pF", "-t", pane, "@agent_prompts", "#{e|+:#{@agent_prompts},1}"})
		}
		if state == Waiting {
			// dev agent prompt refuses a waiting agent unless it's only idle: a paste would answer a permission dialog
			cmds = append(cmds, set("@agent_waiting_for", cmp.Or(h.NotificationType, h.Event)))
		}
	}
	if s.ID == "" {
		cmds = append(cmds, unset("@agent_session"), unset("@agent_transcript"))
	} else {
		cmds = append(cmds, set("@agent_session", s.ID), set("@agent_transcript", s.Transcript))
	}
	var args []string
	for i, cmd := range cmds {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, cmd...)
	}
	_, err := c.Run(args...)
	return err
}

// Trusted is state when the process that recorded it (stateCmd) still runs in the pane, else "".
func Trusted(state, stateCmd, command string) string {
	if stateCmd == "" || stateCmd != command {
		return ""
	}
	return state
}

// Claude is the agent kind whose sessions `dev state` records.
const Claude = "claude"

// ResumeCommand is the shell command that restarts an agent of kind: resumed, its conversation id when
// the id is safe to type and the conversation's transcript still exists, else a fresh agent. ok=false for
// an unknown kind.
func ResumeCommand(kind, id, transcript string) (cmd string, resumed, ok bool) {
	if kind != Claude {
		return "", false, false
	}
	if safeID.MatchString(id) {
		if _, err := os.Stat(transcript); err == nil {
			return "claude --resume " + id, true, true
		}
	}
	return "claude", false, true
}

type codexPane struct {
	ID      string `tmux:"pane_id"`
	Command string `tmux:"pane_current_command"`
	Path    string `tmux:"pane_current_path"`
}

// CodexPane finds the pane of the Codex session a hook reports on: the only pane running codex whose
// directory is the hook's cwd. Codex runs hooks in its shared app-server daemon, whose TMUX_PANE is the
// pane that started it, so the hook's environment can't say. ok=false: no tmux server, no such pane
// (e.g. Codex's background memory agent) or several in that directory. A missing cwd is an error.
func CodexPane(c *tmux.Client, input []byte) (pane string, ok bool, err error) {
	var hook struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(input, &hook); err != nil {
		return "", false, fmt.Errorf("agent: hook input: %w", err)
	}
	if hook.Cwd == "" {
		return "", false, errors.New("agent: hook input: no cwd")
	}
	// tmux reports pane paths with symlinks resolved
	cwd, err := filepath.EvalSymlinks(hook.Cwd)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil // no pane is in a deleted directory
	}
	if err != nil {
		return "", false, fmt.Errorf("agent: hook cwd: %w", err)
	}
	panes, err := tmux.Query[codexPane](c, "list-panes", "-a", "-f", "#{m:codex*,#{pane_current_command}}")
	if tmux.IsNoServer(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, p := range panes {
		if !isCodex(p.Command) || p.Path != cwd {
			continue
		}
		// A window linked into several sessions lists its panes once per session
		if pane != "" && pane != p.ID {
			return "", false, nil
		}
		pane = p.ID
	}
	return pane, pane != "", nil
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
	return Agent{Pane: *codex, Label: "Codex", State: Trusted(codex.State, codex.StateCmd, codex.Command)}, true
}

func isCodex(command string) bool {
	return command == "codex" || strings.HasPrefix(command, "codex-")
}
