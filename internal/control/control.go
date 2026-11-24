// Package control drives agent panes for `dev agent`: list, resolve a target, read, prompt, wait, keys.
package control

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/wait"
)

var (
	// ErrTimeout: the state wanted didn't show up within the timeout.
	ErrTimeout = errors.New("timed out")
	// ErrStalled: a prompt was sent but no UserPromptSubmit hook counted it (@agent_prompts) within the
	// stall window.
	ErrStalled = errors.New("stalled: the agent never picked up the prompt")

	// errNoState: waiting needs the hook state, which only Claude panes have.
	errNoState = errors.New("agent has no hook state (Codex or Grok without its hooks): use read")
	// errGone: the hook state stopped being trusted mid-wait, as the agent left its pane.
	errGone = errors.New("agent's hook state is gone: did it exit? read the pane")
	// errNoAgentPane: the session exists but no agent registered in it (yet).
	errNoAgentPane = errors.New("has no agent pane")
)

// Agent is one session's agent pane as `dev agent list` and `get` report it.
type Agent struct {
	Session string `json:"session"`
	Pane    string `json:"pane"`
	Label   string `json:"agent"` // "Claude", "Codex", "Grok" or "Agy"
	State   string `json:"state"` // trusted hook state (working, waiting, idle) or "" when unknown
	// Queued marks a working Claude holding a prompt it reads at its turn's next step (screen.ClaudeQueued).
	Queued bool `json:"queued,omitempty"`
	// Input is a Claude pane's input box (dev agent get): text the user typed, Claude Code's own dim text, or empty
	Input   *screen.Input `json:"input,omitempty"`
	Project string        `json:"project"`
	Branch  string        `json:"branch"`
	Path    string        `json:"path"`
	// CostUSD is the session's estimated cost (`dev statusline`); omitted when unknown.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// CostTotalUSD is a boss's cost with everything under it, closed sessions included, CostClosedUSD the closed
	// part; omitted for non-bosses, when unknown or nothing closed, and when CostError says why a ledger couldn't be read.
	CostTotalUSD  *float64 `json:"cost_total_usd,omitempty"`
	CostClosedUSD *float64 `json:"cost_closed_usd,omitempty"`
	CostError     string   `json:"cost_error,omitempty"`
	// StartedBy is the session whose coordinator started this one with `dev agent start`; "" otherwise.
	StartedBy string `json:"started_by,omitempty"`
	// Boss marks a `dev boss` coordinator session.
	Boss bool `json:"boss,omitempty"`
	// Outside marks a worker a coordinator outside tmux started for StartedBy (dev agent start --boss).
	Outside bool `json:"outside,omitempty"`
	// Model and Effort are the --model and --effort its Claude or Codex was started with (dev agent start, dev boss), not
	// a later /model or /effort; omitted when it started without that flag.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Questions are every question (tab) of the AskUserQuestion dialog it waits on, open or not; omitted when none.
	Questions []Question `json:"questions,omitempty"`
	// Deciding marks a state waiting on the user's decision (a permission prompt or question), not Claude's idle reminder.
	Deciding bool `json:"deciding,omitempty"`
	// Wait is its planned wait (dev agent wait-until); omitted when none.
	Wait *wait.Wait `json:"wait,omitempty"`
	// HideCost is @dev_cost off: text output leaves the cost out, --json keeps cost_usd.
	HideCost bool `json:"-"`
}

// Question is one question (tab) of an agent's AskUserQuestion dialog.
type Question struct {
	Header      string   `json:"header"`
	Question    string   `json:"question"`
	MultiSelect bool     `json:"multi_select,omitempty"`
	Options     []Option `json:"options"`
}

// Option is a Question's option; Recommended is agent.Choice.Recommended.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// dialogQuestions are the questions of the AskUserQuestion dialog an agent in state, waiting for waitingFor, shows,
// from its @agent_question; nil when it decides nothing (the option outlives an answered dialog until the turn
// ends) or the question is a permission's.
func dialogQuestions(state, waitingFor, question string) []Question {
	q, ok := agent.ParseQuestion(question)
	if !ok || q.Tool != agent.AskUserQuestion || !agent.Deciding(state, waitingFor) {
		return nil
	}
	qs := make([]Question, len(q.Questions))
	for i, a := range q.Questions {
		qs[i] = Question{Header: a.Header, Question: a.Question, MultiSelect: a.MultiSelect, Options: make([]Option, len(a.Options))}
		for k, c := range a.Options {
			qs[i].Options[k] = Option{Label: c.Label, Description: c.Description, Recommended: c.Recommended()}
		}
	}
	return qs
}

// row is one list-panes -a row: agent.Pane's fields plus the session options Detect and Agent need.
type row struct {
	Session  string `tmux:"session_name"`
	Window   int    `tmux:"window_index"`
	ID       string `tmux:"pane_id"`
	Path     string `tmux:"pane_current_path"`
	Title    string `tmux:"pane_title"`
	Command  string `tmux:"pane_current_command"`
	State    string `tmux:"@agent_state"`
	StateCmd string `tmux:"@agent_cmd"`
	Cost     string `tmux:"@agent_cost"`
	Project  string `tmux:"@project"`
	Sidebar  string `tmux:"@sidebar_pane"`
	// The session's own id, the id of the session that started it, and the herd marker
	SessionID string `tmux:"session_id"`
	StartedBy string `tmux:"@dev_started_by"`
	Herd      string `tmux:"@dev_herd"`
	CostOpt   string `tmux:"@dev_cost"`
	Outside   string `tmux:"@dev_started_outside"`
	Model     string `tmux:"@dev_model"`
	Effort    string `tmux:"@dev_effort"`
	// Pane options of each pane: only the agent pane's count
	WaitingFor string `tmux:"@agent_waiting_for"`
	Question   string `tmux:"@agent_question"`
	WaitUntil  string `tmux:"@dev_wait_until"`
	WaitReason string `tmux:"@dev_wait_reason"`
	// The key of its spent ledger, and the pane's conversation
	Socket       string `tmux:"socket_path"`
	Created      string `tmux:"session_created"`
	Conversation string `tmux:"@agent_session"`
}

// List returns every session's agent, in tmux's session order; sessions without one are left out.
func List(c *tmux.Client) ([]Agent, error) {
	agents, err := list(c)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		agents[i].Branch = sidebar.Branch(agents[i].Path)
		if err := agents[i].ReadQueued(c); err != nil {
			return nil, err
		}
	}
	return agents, nil
}

// ReadQueued sets Queued: whether a working Claude holds a queued prompt (a capture, so only list and get).
func (a *Agent) ReadQueued(c *tmux.Client) error {
	if a.State != agent.Working || a.Label != "Claude" {
		return nil
	}
	capture, err := c.Run("capture-pane", "-p", "-t", a.Pane)
	if err != nil && strings.Contains(err.Error(), "can't find pane") {
		return nil // closed since list-panes: nothing queued
	}
	a.Queued = err == nil && screen.ClaudeQueued(capture)
	return err
}

// ReadInput sets a Claude pane's Input from its screen; none without its input box (a dialog in its place).
func (a *Agent) ReadInput(c *tmux.Client) error {
	if a.Label != "Claude" {
		return nil
	}
	in, ok, err := inputBox(c, a.Pane)
	if err != nil && strings.Contains(err.Error(), "can't find pane") {
		return nil // closed since list-panes: no box
	}
	if ok {
		a.Input = &in
	}
	return err
}

// inputBox is pane's Claude input box (screen.InputBox), read from its screen with colours.
func inputBox(c *tmux.Client, pane string) (screen.Input, bool, error) {
	capture, err := c.Run("capture-pane", "-p", "-e", "-t", pane)
	if err != nil {
		return screen.Input{}, false, err
	}
	in, ok := screen.InputBox(capture)
	return in, ok, nil
}

// list is List without the branches and queued prompts, which cost a git run or a capture per agent.
func list(c *tmux.Client) ([]Agent, error) {
	rows, err := tmux.Query[row](c, "list-panes", "-a")
	if err != nil {
		return nil, err
	}
	var first []row // each session's first row: session options are the same on all its rows
	panes := map[string][]agent.Pane{}
	byID := map[string]row{}
	names := map[string]string{} // @dev_started_by value → the starter's name
	for _, r := range rows {
		if _, seen := panes[r.Session]; !seen {
			first = append(first, r)
			names[sidebar.Starter(r.SessionID, r.Session, false)] = r.Session
			if r.Herd != "" {
				names[sidebar.Starter(r.SessionID, r.Session, true)] = r.Session
			}
		}
		byID[r.ID] = r
		panes[r.Session] = append(panes[r.Session], agent.Pane{
			Session: r.Session, Window: r.Window, ID: r.ID, Path: r.Path,
			Title: r.Title, Command: r.Command, State: r.State, StateCmd: r.StateCmd, Cost: r.Cost,
		})
	}
	agents := []Agent{}
	// As the sidebar reads them, for the bosses' totals
	sessions := make([]sidebar.Session, 0, len(first))
	created, socket := map[string]string{}, ""
	for _, r := range first {
		s := sidebar.Session{Name: r.Session, Group: sidebar.Group(r.Session, r.Project), ID: r.SessionID, Boss: r.Herd != "", StartedBy: r.StartedBy}
		created[r.SessionID], socket = r.Created, r.Socket
		a, ok := agent.Detect(panes[r.Session], r.Sidebar)
		if !ok {
			sessions = append(sessions, s)
			continue
		}
		s.Agent, s.State, s.CostUSD, s.HasCost, s.Conversation = a.Label, a.State, a.Cost, a.CostOK, byID[a.Pane.ID].Conversation
		sessions = append(sessions, s)
		ag := Agent{
			Session: r.Session, Pane: a.Pane.ID, Label: a.Label, State: a.State,
			Project: r.Project, Path: a.Pane.Path, StartedBy: names[r.StartedBy], Boss: r.Herd != "",
			Outside: r.Outside != "", Model: r.Model, Effort: r.Effort, HideCost: sidebar.CostHidden(r.CostOpt),
		}
		ag.Deciding = agent.Deciding(a.State, byID[a.Pane.ID].WaitingFor)
		if p := byID[a.Pane.ID]; p.Question != "" {
			ag.Questions = dialogQuestions(a.State, p.WaitingFor, p.Question)
		}
		if a.CostOK {
			ag.CostUSD = &a.Cost
		}
		if w := wait.FromOptions(byID[a.Pane.ID].WaitUntil, byID[a.Pane.ID].WaitReason); w.Held() {
			ag.Wait = &w
		}
		agents = append(agents, ag)
	}
	if err := sidebar.LoadSpent(sessions, socket, created); err != nil {
		return nil, err
	}
	rolls := sidebar.Subtree(sidebar.Order(sessions), nil)
	for i, a := range agents {
		switch r := rolls[a.Session]; {
		case r == nil:
		case r.CostErr != "":
			agents[i].CostError = r.CostErr
		case r.HasCost:
			agents[i].CostTotalUSD = &r.CostUSD
			if r.ClosedUSD > 0 {
				agents[i].CostClosedUSD = &r.ClosedUSD
			}
		}
	}
	return agents, nil
}

// BossPane is a pane of boss session session (its active one, maybe not the coordinator's): callers only read its session.
func BossPane(c *tmux.Client, session string) (string, error) {
	// display-message prints empty formats for a missing session, without failing
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err != nil {
		return "", fmt.Errorf("no session %q: %w", session, err)
	}
	out, err := c.Run("display-message", "-p", "-t", tmux.SessionTarget(session), "#{pane_id}\t#{"+sidebar.OptHerd+"}")
	if err != nil {
		return "", err
	}
	pane, herd, _ := strings.Cut(strings.TrimSuffix(out, "\n"), "\t")
	if pane == "" { // gone since has-session
		return "", fmt.Errorf("no session %q", session)
	}
	if herd == "" {
		return "", fmt.Errorf("%s is not a boss session (dev boss)", session)
	}
	return pane, nil
}

// StartedBy keeps the agents whose session was started (dev agent start) from pane's session.
func StartedBy(c *tmux.Client, agents []Agent, pane string) ([]Agent, error) {
	own, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return nil, err
	}
	own = strings.TrimSpace(own)
	mine := []Agent{}
	for _, a := range agents {
		if a.StartedBy == own {
			mine = append(mine, a)
		}
	}
	return mine, nil
}

// Resolve finds the agent a target names: a session name (exact) or an agent pane id (%N).
func Resolve(c *tmux.Client, target string) (Agent, error) {
	agents, err := list(c)
	if err != nil {
		return Agent{}, err
	}
	for _, a := range agents {
		if a.Session == target || a.Pane == target {
			a.Branch = sidebar.Branch(a.Path)
			return a, nil
		}
	}
	if strings.HasPrefix(target, "%") {
		return Agent{}, fmt.Errorf("pane %s is not an agent pane", target)
	}
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(target)); err != nil {
		return Agent{}, fmt.Errorf("no session %q", target)
	}
	return Agent{}, fmt.Errorf("session %q %w", target, errNoAgentPane)
}

// NoAgent reports Resolve's error for a session with no agent registered in it.
func NoAgent(err error) bool { return errors.Is(err, errNoAgentPane) }

// AwaitAgent polls until session has a registered agent; a timeout reports the session's agent window.
func AwaitAgent(c *tmux.Client, clk Clock, session string, timeout time.Duration) (Agent, error) {
	start := clk.Now()
	for {
		a, err := Resolve(c, session)
		if !errors.Is(err, errNoAgentPane) {
			return a, err
		}
		if clk.Now().Sub(start) >= timeout {
			screen, cerr := c.Run("capture-pane", "-p", "-t", tmux.SessionTarget(session)+"1")
			if cerr != nil {
				screen = cerr.Error()
			}
			return a, fmt.Errorf("%w after %s: %w; its screen:\n%s", ErrTimeout, timeout, err, strings.TrimSpace(screen))
		}
		clk.Sleep(pollInterval)
	}
}

// Status is a pane's hook state, read on every poll.
type Status struct {
	Command    string `tmux:"pane_current_command"`
	State      string `tmux:"@agent_state"`
	StateCmd   string `tmux:"@agent_cmd"`
	Prompts    string `tmux:"@agent_prompts"`
	WaitingFor string `tmux:"@agent_waiting_for"`
	Since      string `tmux:"@agent_since"`
	Session    string `tmux:"@agent_session"`
	Active     string `tmux:"@agent_active"`
}

// Trusted is the state when the hook that wrote it still runs in the pane, else "".
func (s Status) Trusted() string {
	return agent.Trusted(s.State, s.StateCmd, s.Command)
}

// checkPromptable refuses an agent waiting on a decision (a permission dialog, a question), where a
// pasted prompt and its Enter would pick an answer; a waiting agent that is only idle is fine.
func (s Status) checkPromptable() error {
	if s.Trusted() != agent.Waiting || s.WaitingFor == agent.IdlePrompt {
		return nil
	}
	return fmt.Errorf("agent is waiting for a decision (%s): read it and answer with send-keys", cmp.Or(s.WaitingFor, "unknown"))
}

// Settled reports an agent done with its turn: idle, or waiting for the user.
func (s Status) Settled() bool {
	st := s.Trusted()
	return st == agent.Idle || st == agent.Waiting
}

// ReadStatus reads pane's hook state.
func ReadStatus(c *tmux.Client, pane string) (Status, error) {
	out, err := c.Run("display-message", "-p", "-t", pane, tmux.Format[Status]())
	if err != nil {
		return Status{}, err
	}
	rows, err := tmux.Parse[Status](out)
	if err != nil {
		return Status{}, err
	}
	if len(rows) != 1 {
		return Status{}, fmt.Errorf("control: %d status rows for %s", len(rows), pane)
	}
	return rows[0], nil
}

// ReadChecked is ReadStatus, but a Claude or agy pane's decision (agent.Deciding) or working state is checked
// against its screen first, as neither fires a hook when its permission prompt is declined or cancelled, or Esc
// ends its turn (an agy question isn't: its PostToolUse settles it). For a decision, its own input box back at the bottom (screen.ClaudeState, screen.AgyBox) means no dialog
// is open; the state it shows, idle or working, is recorded and returned. A working pane showing its turn
// interrupted (screen.ClaudeInterrupted, screen.AgyInterrupted) is recorded idle.
func ReadChecked(c *tmux.Client, pane string) (Status, error) {
	st, err := ReadStatus(c, pane)
	if err != nil {
		return st, err
	}
	if agent.AgyCommand(st.Command) && st.Trusted() == agent.Working {
		return checkInterrupted(c, pane, st, screen.AgyInterrupted)
	}
	if agent.AgyCommand(st.Command) && agent.Deciding(st.Trusted(), st.WaitingFor) {
		// A declined or cancelled permission prompt fires no hook; a question's answer or skip does (PostToolUse),
		// and its hook records it before agy draws it
		if asks, err := agyAsks(c, pane); asks || err != nil {
			return st, err
		}
		return checkDialog(c, pane, st, func(capture string) string {
			switch {
			case screen.AgyIdle(capture):
				return agent.Idle
			case screen.AgyBox(capture):
				return agent.Working
			}
			return ""
		})
	}
	if !agent.ClaudeCommand(st.Command) {
		return st, nil
	}
	if st.Trusted() == agent.Working {
		return checkInterrupted(c, pane, st, screen.ClaudeInterrupted)
	}
	// Only permission prompts and questions: no other dialog's screen is known to replace the input box
	if st.Trusted() != agent.Waiting || st.WaitingFor != agent.PermissionPrompt {
		return st, nil
	}
	return checkDialog(c, pane, st, screen.ClaudeState)
}

// agyAsks reports an agy question (agent.AgyHook) recorded as pane's @agent_question.
func agyAsks(c *tmux.Client, pane string) (bool, error) {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{"+agent.OptQuestion+"}")
	q, ok := agent.ParseQuestion(strings.TrimSpace(out))
	return ok && q.Tool == agent.AskUserQuestion, err
}

// checkDialog records pane st, waiting on a decision, as the state its screen shows (shows: "" while its dialog is
// open) once its dialog closed without a hook. Not within dialogGrace of the decision: Claude draws a dialog
// seconds before its Notification records it.
func checkDialog(c *tmux.Client, pane string, st Status, shows func(capture string) string) (Status, error) {
	sec, err := strconv.ParseInt(st.Since, 10, 64)
	if err != nil || now().Sub(time.Unix(sec, 0)) < dialogGrace {
		return st, nil
	}
	capture, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return st, err
	}
	state := shows(capture)
	if state == "" {
		return st, nil
	}
	if err := agent.DialogGone(c, pane, state, st.Since); err != nil {
		return st, err
	}
	// A hook may have moved the pane on meanwhile
	return ReadStatus(c, pane)
}

// checkInterrupted records working pane st idle when its screen shows its turn ended without a hook
// (interrupted: Esc, or for agy also a declined permission prompt). Not within dialogGrace of its last hook
// (agent.OptActive): a new prompt's may run before the agent draws the prompt, under the last turn's "Interrupted".
func checkInterrupted(c *tmux.Client, pane string, st Status, interrupted func(string) bool) (Status, error) {
	if sec, err := strconv.ParseInt(st.Active, 10, 64); err == nil && now().Sub(time.Unix(sec, 0)) < dialogGrace {
		return st, nil
	}
	capture, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil || !interrupted(capture) {
		return st, err
	}
	if err := agent.Interrupted(c, pane, st.Since, st.Prompts); err != nil {
		return st, err
	}
	return ReadStatus(c, pane)
}

// dialogGrace is how old a decision must be before ReadChecked reads its screen.
const dialogGrace = 2 * time.Second

// now is the clock ReadChecked ages decisions by; tests replace it.
var now = time.Now

// Clock is what polling needs from time; tests replace it.
type Clock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
}

// RealClock polls in real time.
var RealClock = Clock{Now: time.Now, Sleep: time.Sleep}

// pollInterval is how often Wait and PromptWait read the pane's state.
const pollInterval = 200 * time.Millisecond

// Wait polls pane until done reports true for its status, or timeout passes (ErrTimeout). A pane
// without a trusted state (Codex or Grok without its hooks, agy before its start saw its input box) fails at once.
func Wait(c *tmux.Client, clk Clock, pane string, timeout time.Duration, done func(Status) bool) (Status, error) {
	st, err := ReadChecked(c, pane)
	if err != nil {
		return st, err
	}
	if st.Trusted() == "" {
		return st, errNoState
	}
	return poll(c, clk, pane, timeout, func(st Status, _ time.Duration) (bool, error) { return done(st), nil })
}

// lostGrace is how long the state may go untrusted before a wait gives up (errGone): /clear unsets it
// until the next SessionStart.
const lostGrace = 3 * time.Second

// screenEvery is how often poll checks the pane's screen (ReadChecked) instead of only its state.
const screenEvery = time.Second

// poll reads pane's status until check is done or fails, the state stays untrusted for lostGrace
// (errGone), or timeout passes (ErrTimeout); check also gets the time since polling started. Every
// screenEvery it reads it with ReadChecked, so a turn ended by Esc doesn't stay working.
func poll(c *tmux.Client, clk Clock, pane string, timeout time.Duration, check func(Status, time.Duration) (bool, error)) (Status, error) {
	start := clk.Now()
	var lost time.Time
	checked := start
	for {
		read := ReadStatus
		if clk.Now().Sub(checked) >= screenEvery {
			read, checked = ReadChecked, clk.Now()
		}
		st, err := read(c, pane)
		if err != nil {
			return st, err
		}
		now := clk.Now()
		elapsed := now.Sub(start)
		if done, err := check(st, elapsed); done || err != nil {
			return st, err
		}
		switch {
		case st.Trusted() != "":
			lost = time.Time{}
		case lost.IsZero():
			lost = now
		case now.Sub(lost) >= lostGrace:
			return st, errGone
		}
		if elapsed >= timeout {
			return st, fmt.Errorf("%w after %s, state %q", ErrTimeout, timeout, st.Trusted())
		}
		clk.Sleep(pollInterval)
	}
}

// enterDelay separates the paste from Enter, so the agent doesn't take Enter as part of the paste.
const enterDelay = 150 * time.Millisecond

// Prompt pastes text into pane as one bracketed paste, then presses Enter, so multi-line text submits
// as a single prompt. A busy agent queues it (queued=true); one waiting on a decision, or a busy Claude whose
// screen shows no input box (a dialog not reported yet), is refused. A slash command sent to an idle Claude
// is watched for up to commandWait until it ran (see command); not seen running by then is an error.
func Prompt(c *tmux.Client, clk Clock, pane, text string) (queued bool, err error) {
	st, err := ReadChecked(c, pane)
	if err != nil {
		return false, notSent{err}
	}
	if err := st.checkPromptable(); err != nil {
		return false, notSent{err}
	}
	if err := checkAgyBox(c, pane, st); err != nil {
		return false, notSent{err}
	}
	claude := agent.ClaudeCommand(st.Command)
	queued = st.Trusted() == agent.Working && claude
	if queued && claude {
		// Claude records a dialog seconds after drawing it (not at all while someone types in it)
		capture, err := c.Run("capture-pane", "-p", "-t", pane)
		if err != nil {
			return false, notSent{err}
		}
		if screen.ClaudeState(capture) == "" {
			return false, notSent{errors.New("agent is working, but its screen shows no input box: a dialog may be open; read it and answer with send-keys")}
		}
	}
	if err := paste(c, clk, pane, text); err != nil {
		return false, err
	}
	if st.Trusted() == agent.Working || !claude || !isCommand(text) {
		return queued, nil
	}
	cmd := command{text: text}
	for start := clk.Now(); clk.Now().Sub(start) < commandWait; clk.Sleep(pollInterval) {
		if ran, err := cmd.step(c, pane, clk.Now().Sub(start)); ran != "" || err != nil {
			return false, err
		}
	}
	return false, fmt.Errorf("%s not seen running within %s: read the pane", strings.Fields(text)[0], commandWait)
}

const (
	// commandWait bounds how long a plain prompt watches a slash command it sent.
	commandWait = 3 * time.Second
	// localGrace is how long after it was sent a slash command must show Claude idle to count as run
	// locally (/effort): a command that starts a turn (a skill's) shows working by then.
	localGrace = time.Second
)

// checkAgyBox refuses an agy pane whose screen shows no input box: agy records no hook for its permission prompts
// and questions, and a paste and its Enter would pick an answer.
func checkAgyBox(c *tmux.Client, pane string, st Status) error {
	if !agent.AgyCommand(st.Command) {
		return nil
	}
	capture, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return err
	}
	if !screen.AgyBox(capture) {
		return errors.New("agy's screen shows no input box: a dialog may be open; read it and answer with send-keys")
	}
	return nil
}

// squash is s without whitespace, so a command wrapped in the input box still matches.
func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// isCommand reports a one-line slash command.
func isCommand(text string) bool {
	return strings.HasPrefix(text, "/") && !strings.Contains(text, "\n")
}

// command watches Claude's screen once a slash command (text) was sent: Claude fires no hook for one it
// runs itself (/effort, /model), and those open a dialog mid-conversation.
type command struct {
	text    string
	pressed bool
}

// step reads pane's screen: the dialog /effort and /model open mid-conversation (screen.ClaudeCacheDialog)
// is confirmed with 1, any other dialog is an error once localGrace passed. ran is agent.Working once a turn
// or long command (/compact) runs, agent.Idle once its input box is back, idle, without the command, and
// localGrace passed since it was sent, else "".
func (cmd *command) step(c *tmux.Client, pane string, elapsed time.Duration) (ran string, err error) {
	capture, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return "", err
	}
	if screen.ClaudeCacheDialog(capture) {
		if cmd.pressed {
			return "", nil
		}
		// Only once it shows: a 1 sent before would stay in the input box
		cmd.pressed = true
		if err := SendKeys(c, pane, []string{"1"}); err != nil {
			return "", fmt.Errorf("%s sent, confirming its dialog failed: %w", strings.Fields(cmd.text)[0], err)
		}
		return "", nil
	}
	box, ok := screen.ClaudeBox(capture)
	switch {
	case !ok && elapsed >= localGrace:
		return "", fmt.Errorf("%s opened a dialog: read it and answer with send-keys", strings.Fields(cmd.text)[0])
	case !ok || strings.Contains(squash(box), squash(cmd.text)):
		// Still in the box, maybe wrapped
		return "", nil
	}
	if st := screen.ClaudeState(capture); st == agent.Working || elapsed >= localGrace {
		return st, nil
	}
	return "", nil
}

// notSent is an error from before a prompt's text reached the pane.
type notSent struct{ error }

func (e notSent) Unwrap() error { return e.error }

// NotSent reports whether err, from Prompt or PromptWait, came before the text reached the pane: nothing was typed.
func NotSent(err error) bool {
	var n notSent
	return errors.As(err, &n)
}

func paste(c *tmux.Client, clk Clock, pane, text string) error {
	if strings.TrimSpace(text) == "" {
		return notSent{errors.New("empty prompt")}
	}
	// From a file, not argv: tmux splits commands at an argument's trailing ";" and caps a command's size
	f, err := os.CreateTemp("", "dev-agent-prompt-")
	if err != nil {
		return notSent{err}
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return notSent{err}
	}
	buffer := "dev-agent-" + strconv.Itoa(os.Getpid())
	if _, err := c.Run("load-buffer", "-b", buffer, f.Name()); err != nil {
		return notSent{err}
	}
	if _, err := c.Run("paste-buffer", "-p", "-d", "-b", buffer, "-t", pane); err != nil {
		// -d only deletes after a paste
		_, _ = c.Run("delete-buffer", "-b", buffer)
		return notSent{err}
	}
	clk.Sleep(enterDelay)
	_, err = c.Run("send-keys", "-t", pane, "Enter")
	return err
}

// PromptWait prompts a settled agent and waits until the hook counted the prompt (@agent_prompts) and
// the agent settled again, so a turn shorter than a poll isn't missed and the settled state from before
// isn't taken for done. /clear starts no turn: it waits for the new session's SessionStart (@agent_session)
// instead. No count (or session) within stall is ErrStalled; no settled state within timeout is ErrTimeout.
// Another slash command Claude runs itself counts no prompt: its screen (command.step) says when it ran.
func PromptWait(c *tmux.Client, clk Clock, pane, text string, stall, timeout time.Duration) (Status, error) {
	return PromptWaitSent(c, clk, pane, text, stall, timeout, func() {})
}

// PromptWaitSent is PromptWait calling sent once the text reached the pane, before it waits.
func PromptWaitSent(c *tmux.Client, clk Clock, pane, text string, stall, timeout time.Duration, sent func()) (Status, error) {
	before, err := ReadChecked(c, pane)
	if err != nil {
		return before, notSent{err}
	}
	switch st := before.Trusted(); {
	case st == "":
		return before, notSent{errNoState}
	case !before.Settled():
		// Its Stop would end the wait before the queued prompt even starts
		return before, notSent{fmt.Errorf("agent is %s: wait for it first", st)}
	}
	if err := before.checkPromptable(); err != nil {
		return before, notSent{err}
	}
	if err := checkAgyBox(c, pane, before); err != nil {
		return before, notSent{err}
	}
	// Claude Code's aliases of /clear too
	reset := slices.Contains([]string{"/clear", "/reset", "/new"}, strings.TrimSpace(text))
	// agy's hooks record a session, but only from its first turn on: none starts with /clear
	if reset && (before.Session == "" || agent.AgyCommand(before.Command)) {
		return before, notSent{errors.New("--wait confirms /clear by the new session its hook records, and this agent's hook records none at it (Codex, Grok, agy): send it without --wait")}
	}
	err = paste(c, clk, pane, text)
	if !NotSent(err) {
		sent()
	}
	if err != nil {
		return before, err
	}
	var cmd *command
	if !reset && isCommand(text) && agent.ClaudeCommand(before.Command) {
		cmd = &command{text: text}
	}
	running := false
	return poll(c, clk, pane, timeout, func(st Status, elapsed time.Duration) (bool, error) {
		picked := st.Prompts != before.Prompts
		if reset {
			picked = st.Session != "" && st.Session != before.Session
		}
		if picked {
			return st.Settled(), nil
		}
		if cmd != nil {
			ran, err := cmd.step(c, pane, elapsed)
			if err != nil || ran == agent.Idle && st.Settled() {
				return err == nil, err
			}
			running = running || ran != ""
		}
		if !running && elapsed >= stall {
			return false, fmt.Errorf("%w within %s", ErrStalled, stall)
		}
		return false, nil
	})
}

// Read returns the last lines of pane's history and screen, trailing spaces and blank lines trimmed.
// An agent drawing on the alternate screen (Claude Code's full-screen UI, Codex) has only that screen. A claude pane's
// input row holding only Claude Code's dim text ends with suggestionMark.
func Read(c *tmux.Client, pane string, lines int, claude bool) (string, error) {
	if lines < 1 {
		return "", fmt.Errorf("lines must be at least 1, got %d", lines)
	}
	args := []string{"capture-pane", "-p", "-J", "-S", "-" + strconv.Itoa(lines), "-t", pane}
	if claude {
		// With its colours: the mark and the text from one capture, as the box may change between two
		args = append(args, "-e")
	}
	out, err := c.Run(args...)
	if err != nil {
		return "", err
	}
	in, ok := screen.InputBox(out)
	all := strings.Split(out, "\n")
	for i, l := range all {
		all[i] = strings.TrimRight(screen.Strip(l), " ")
	}
	for len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	if len(all) == 0 {
		return "", nil
	}
	if claude && ok && in.Suggestion {
		markSuggestion(all, in)
	}
	return strings.Join(all, "\n") + "\n", nil
}

// suggestionMark ends the input box's row in Read when Claude Code's dim suggestion fills it.
const suggestionMark = "  [suggestion]"

// markSuggestion ends with suggestionMark the last of lines that is the prompt row of in, Claude Code's dim suggestion
// (screen.InputBox), as plain text can't tell it from the user's typing.
func markSuggestion(lines []string, in screen.Input) {
	// Its prompt row: a long suggestion wraps onto the rows under it
	first, _, _ := strings.Cut(in.Text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text, ok := screen.PromptRow(lines[i]); ok && strings.TrimSpace(text) == first {
			lines[i] += suggestionMark
			return
		}
	}
}

// keyNames maps send-keys names to tmux keys.
var keyNames = map[string]string{
	"esc": "Escape", "enter": "Enter", "tab": "Tab", "shift+tab": "BTab", "space": "Space",
	"backspace": "BSpace", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"ctrl+c": "C-c", "ctrl+d": "C-d",
}

// KeyNames lists the names SendKeys accepts besides single characters, sorted.
func KeyNames() []string {
	return slices.Sorted(maps.Keys(keyNames))
}

// SendKeys presses keys in pane in order: names from KeyNames, or a single character typed as is.
// Every key is checked before the first is sent.
func SendKeys(c *tmux.Client, pane string, keys []string) error {
	args := make([][]string, len(keys))
	for i, k := range keys {
		if name, ok := keyNames[strings.ToLower(k)]; ok {
			args[i] = []string{"send-keys", "-t", pane, name}
			continue
		}
		if len([]rune(k)) != 1 {
			return fmt.Errorf("unknown key %q, want one character or one of %s", k, strings.Join(KeyNames(), ", "))
		}
		if k == ";" {
			// A lone ";" argument is tmux's command separator
			k = `\;`
		}
		args[i] = []string{"send-keys", "-t", pane, "-l", k}
	}
	for _, a := range args {
		if _, err := c.Run(a...); err != nil {
			return err
		}
	}
	return nil
}

// escWait bounds how long AfterEsc watches for Claude to draw its interrupted turn.
const escWait = 3 * time.Second

// AfterEsc records a working Claude pane idle once its screen shows the turn Esc ended (ReadChecked), as
// no hook reports it; it gives up after escWait, as Esc may have closed something else.
func AfterEsc(c *tmux.Client, clk Clock, pane string) error {
	for start := clk.Now(); clk.Now().Sub(start) < escWait; clk.Sleep(pollInterval) {
		st, err := ReadChecked(c, pane)
		if err != nil || st.Trusted() != agent.Working {
			return err
		}
	}
	return nil
}

// SendText types text into pane literally, as one send-keys.
func SendText(c *tmux.Client, pane, text string) error {
	if t, ok := strings.CutSuffix(text, ";"); ok {
		// An argument's trailing ";" is tmux's command separator
		text = t + `\;`
	}
	// "--": text starting with "-" would be taken for flags
	_, err := c.Run("send-keys", "-t", pane, "-l", "--", text)
	return err
}
