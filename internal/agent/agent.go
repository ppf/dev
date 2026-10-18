// Package agent records Claude, Codex and Grok hook state on tmux panes, detects a session's agent pane and
// gives the command that reopens a saved Claude conversation.
package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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

// GrokSkip reports a Grok hook event that isn't the main session's turn, so it must not move the pane:
//   - a subagent's event (Grok marks it with subagentType, not agent_id), except a permission prompt or
//     question, which blocks the pane like the main session's;
//   - the observe-only Stop at session end (reason other than end_turn), which would notify "done".
//
// Malformed input isn't skipped.
func GrokSkip(input []byte) bool {
	var e struct {
		Event            string  `json:"hook_event_name"`
		NotificationType string  `json:"notification_type"`
		Subagent         *string `json:"subagentType"`
		Reason           string  `json:"reason"`
	}
	if json.Unmarshal(input, &e) != nil {
		return false
	}
	if e.Subagent != nil {
		return e.Event != "Notification" || e.NotificationType != "permission_prompt" && e.NotificationType != "elicitation_dialog"
	}
	return e.Event == "Stop" && e.Reason != "end_turn"
}

// resumers are the background task types whose end resumes Claude on its own. Left out: Claude's own
// housekeeping (dream, auto-mode scan, memory import), and monitors and teammates, which stay registered
// as running while idle.
var resumers = []string{"subagent", "workflow", "shell", "MCP task", "cloud session"}

// Backgrounded reports a Claude Stop with background tasks (subagents, shells) still running or pending:
// Claude resumes when they finish, so it isn't a finished turn.
func Backgrounded(input []byte) bool {
	var e struct {
		Event string `json:"hook_event_name"`
		Tasks []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"background_tasks"`
	}
	if json.Unmarshal(input, &e) != nil || e.Event != "Stop" {
		return false
	}
	for _, t := range e.Tasks {
		if slices.Contains(resumers, t.Type) && (t.Status == "running" || t.Status == "pending") {
			return true
		}
	}
	return false
}

// GrokStale reports a Grok turn end (Stop, StopFailure, StopCancelled) for another turn than the one the
// pane runs: Grok sends a cancelled turn's report off its command loop, so it can land after the next
// UserPromptSubmit. An event or pane without a prompt id isn't stale.
func GrokStale(c *tmux.Client, pane string, h Hook) (bool, error) {
	if h.PromptID == "" || h.Event != "Stop" && h.Event != "StopFailure" && h.Event != "StopCancelled" {
		return false, nil
	}
	current, err := c.Run("display-message", "-t", pane, "-p", "#{@agent_prompt_id}")
	if err != nil {
		return false, err
	}
	current = strings.TrimSpace(current)
	return current != "" && current != h.PromptID, nil
}

// IdlePrompt is the Notification type Claude sends after a minute idle: waiting, but safe to prompt.
const IdlePrompt = "idle_prompt"

// Deciding reports a trusted state waiting on the user's decision (a permission prompt or question),
// not just Claude's idle reminder; waitingFor is @agent_waiting_for.
func Deciding(state, waitingFor string) bool {
	return state == Waiting && waitingFor != IdlePrompt
}

// Hook is the part of a hook's input Record needs besides the Session.
type Hook struct {
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	PromptID         string `json:"promptId"` // Grok's turn id
	ToolInput        struct {
		Command string `json:"command"` // a shell tool's command
	} `json:"tool_input"`
}

// prCommand is a gh command that opens a PR or changes its state, global flags (-R owner/repo) included.
var prCommand = regexp.MustCompile(`\bgh(\s+-\S+(\s+[^-\s]\S*)?)*\s+pr\s+(create|merge|close|reopen|ready)\b`)

// ParseHook reads a hook's input; input that isn't JSON gives the zero Hook (Resolve reports it), and a
// field of another type (Codex's apply_patch tool_input is a string) is left zero.
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

// Record writes state to @agent_state (and when it changed to @agent_since), the pane's current command to @agent_cmd and s to @agent_session
// and @agent_transcript in one chained tmux call, bumping @agent_prompts (and recording Grok's turn id in
// @agent_prompt_id) on UserPromptSubmit, setting @agent_waiting_for to why a waiting state waits (Claude's
// notification type, else the event: Codex's PermissionRequest) and bumping @dev_pr_gen on a PostToolUse of a
// gh command that changes a PR; Clear (or no session) unsets the session options.
func Record(c *tmux.Client, pane, state string, s Session, h Hook) error {
	unset := func(option string) []string { return []string{"set-option", "-pu", "-t", pane, option} }
	set := func(option, value string) []string { return []string{"set-option", "-p", "-t", pane, option, value} }
	var cmds [][]string
	if state == Clear {
		// @agent_prompts stays: a count that restarted could repeat the value a waiter read before /clear
		s = Session{}
		cmds = [][]string{unset("@agent_state"), unset("@agent_cmd"), unset("@agent_cost"), unset("@agent_since"), unset("@agent_prompt_id")}
	} else {
		// @agent_since (unix seconds) only moves when the state changes, so it must run before @agent_state is set;
		// @agent_cmd ties the state to the writing process, so a crash's leftover is ignored by the sidebar
		since := "#{?#{&&:#{==:#{@agent_state}," + state + "},#{@agent_since}},#{@agent_since}," + strconv.FormatInt(now().Unix(), 10) + "}"
		cmds = [][]string{{"set-option", "-pF", "-t", pane, "@agent_since", since}, set("@agent_state", state), {"set-option", "-pF", "-t", pane, "@agent_cmd", "#{pane_current_command}"}}
		if h.Event == "SessionStart" {
			// A crashed session's cost would show until the new one's first status line
			cmds = append(cmds, unset("@agent_cost"))
		}
		if h.Event == "PostToolUse" && prCommand.MatchString(h.ToolInput.Command) {
			// The views' shared PR lookup (@dev_pr) is stale: every view looks it up again
			cmds = append(cmds, []string{"set-option", "-pF", "-t", pane, "@dev_pr_gen", "#{e|+:#{@dev_pr_gen},1}"})
		}
		if h.Event == "UserPromptSubmit" {
			// Counts prompts, so `dev agent prompt --wait` sees a turn that ended between two polls
			cmds = append(cmds, []string{"set-option", "-pF", "-t", pane, "@agent_prompts", "#{e|+:#{@agent_prompts},1}"})
			if h.PromptID != "" {
				cmds = append(cmds, set("@agent_prompt_id", h.PromptID))
			}
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

// now is the clock @agent_since reads; tests replace it.
var now = time.Now

// Trusted is state when the process that recorded it (stateCmd) still runs in the pane, else "".
func Trusted(state, stateCmd, command string) string {
	if stateCmd == "" || stateCmd != command {
		return ""
	}
	return state
}

// Claude is the agent kind whose sessions `dev state` records.
const Claude = "claude"

// Codex and Grok are the other agent kinds dev agent start can open.
const (
	Codex = "codex"
	Grok  = "grok"
)

// ClaudeArgv is claude's argv with args, its Remote Control session named session. Not --name: that pins
// the pane title to the name, leaving the sidebar and previews no task title. Joined with =, as the name
// is optional: a name starting with - would be taken for a flag.
func ClaudeArgv(session string, args ...string) []string {
	return append(append([]string{Claude}, args...), "--remote-control="+session)
}

// ResumeCommand is the shell command that restarts an agent of kind in session: resumed, its conversation
// id when the id is safe to type and the conversation's transcript still exists, else a fresh agent.
// ok=false for an unknown kind.
func ResumeCommand(kind, id, transcript, session string) (cmd string, resumed, ok bool) {
	if kind != Claude {
		return "", false, false
	}
	if safeID.MatchString(id) {
		if _, err := os.Stat(transcript); err == nil {
			return tmux.ShellJoin(ClaudeArgv(session, "--resume", id)...), true, true
		}
	}
	return tmux.ShellJoin(ClaudeArgv(session)...), false, true
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
	Cost     string `tmux:"@agent_cost"`
}

// Agent is a session's detected agent pane.
type Agent struct {
	Pane  Pane
	Label string // "Claude", "Codex" or "Grok"
	State string // hook state, only when @agent_cmd matches the pane's command; else ""
	// Cost is @agent_cost in USD, only for a hook-registered Claude pane (@agent_cmd ties it to the process)
	Cost   float64
	CostOK bool
}

// Detect finds the agent among one session's panes: in window 1, excluding sidebarPane, the pane whose
// command equals its @agent_cmd (hook-registered Claude), else one whose command is grok or grok-*, else
// one whose command is codex or codex-*.
func Detect(panes []Pane, sidebarPane string) (Agent, bool) {
	var codex, grok *Pane
	for i := range panes {
		p := &panes[i]
		if p.Window != 1 || p.ID == sidebarPane {
			continue
		}
		// Before the Claude check: Grok's hooks set @agent_cmd too
		if isGrok(p.Command) {
			if grok == nil {
				grok = p
			}
			continue
		}
		if isCodex(p.Command) {
			if codex == nil {
				codex = p
			}
			continue
		}
		if p.StateCmd != "" && p.Command == p.StateCmd {
			a := Agent{Pane: *p, Label: "Claude", State: p.State}
			a.Cost, a.CostOK = ParseCost(p.Cost)
			return a, true
		}
	}
	if grok != nil {
		return Agent{Pane: *grok, Label: "Grok", State: Trusted(grok.State, grok.StateCmd, grok.Command)}, true
	}
	if codex == nil {
		return Agent{}, false
	}
	return Agent{Pane: *codex, Label: "Codex", State: Trusted(codex.State, codex.StateCmd, codex.Command)}, true
}

func isCodex(command string) bool {
	return command == "codex" || strings.HasPrefix(command, "codex-")
}

// isGrok matches grok and its versioned binary (grok-1.0.62-macos-aarch64, cut by macOS to grok-1.0.62-mac).
func isGrok(command string) bool {
	return command == "grok" || strings.HasPrefix(command, "grok-")
}

// StatusCost reads cost.total_cost_usd from a Claude status line's stdin; ok=false when it's missing,
// malformed or negative.
func StatusCost(input []byte) (usd float64, ok bool) {
	var v struct {
		Cost struct {
			USD *float64 `json:"total_cost_usd"`
		} `json:"cost"`
	}
	if json.Unmarshal(input, &v) != nil || v.Cost.USD == nil || *v.Cost.USD < 0 {
		return 0, false
	}
	return *v.Cost.USD, true
}

// RecordCost writes usd to the pane's @agent_cost.
func RecordCost(c *tmux.Client, pane string, usd float64) error {
	_, err := c.Run("set-option", "-p", "-t", pane, "@agent_cost", strconv.FormatFloat(usd, 'f', -1, 64))
	return err
}

// ClearCost unsets the pane's @agent_cost.
func ClearCost(c *tmux.Client, pane string) error {
	_, err := c.Run("set-option", "-pu", "-t", pane, "@agent_cost")
	return err
}

// ParseCost reads an @agent_cost value; ok=false when unset, malformed, negative or not finite.
func ParseCost(v string) (float64, bool) {
	usd, err := strconv.ParseFloat(v, 64)
	if err != nil || usd < 0 || math.IsInf(usd, 0) || math.IsNaN(usd) {
		return 0, false
	}
	return usd, true
}
