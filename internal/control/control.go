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
	Label   string `json:"agent"` // "Claude", "Codex" or "Grok"
	State   string `json:"state"` // trusted hook state (working, waiting, idle) or "" when unknown
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	// CostUSD is the session's estimated cost (`dev statusline`); omitted when unknown.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// StartedBy is the session whose coordinator started this one with `dev agent start`; "" otherwise.
	StartedBy string `json:"started_by,omitempty"`
	// Boss marks a `dev boss` coordinator session.
	Boss bool `json:"boss,omitempty"`
	// HideCost is @dev_cost off: text output leaves the cost out, --json keeps cost_usd.
	HideCost bool `json:"-"`
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
}

// List returns every session's agent, in tmux's session order; sessions without one are left out.
func List(c *tmux.Client) ([]Agent, error) {
	agents, err := list(c)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		agents[i].Branch = sidebar.Branch(agents[i].Path)
	}
	return agents, nil
}

// list is List without the branches, which cost a git run per agent.
func list(c *tmux.Client) ([]Agent, error) {
	rows, err := tmux.Query[row](c, "list-panes", "-a")
	if err != nil {
		return nil, err
	}
	var first []row // each session's first row: session options are the same on all its rows
	panes := map[string][]agent.Pane{}
	names := map[string]string{} // @dev_started_by value → the starter's name
	for _, r := range rows {
		if _, seen := panes[r.Session]; !seen {
			first = append(first, r)
			names[sidebar.Starter(r.SessionID, r.Session, false)] = r.Session
			if r.Herd != "" {
				names[sidebar.Starter(r.SessionID, r.Session, true)] = r.Session
			}
		}
		panes[r.Session] = append(panes[r.Session], agent.Pane{
			Session: r.Session, Window: r.Window, ID: r.ID, Path: r.Path,
			Title: r.Title, Command: r.Command, State: r.State, StateCmd: r.StateCmd, Cost: r.Cost,
		})
	}
	agents := []Agent{}
	for _, r := range first {
		a, ok := agent.Detect(panes[r.Session], r.Sidebar)
		if !ok {
			continue
		}
		ag := Agent{
			Session: r.Session, Pane: a.Pane.ID, Label: a.Label, State: a.State,
			Project: r.Project, Path: a.Pane.Path, StartedBy: names[r.StartedBy], Boss: r.Herd != "",
			HideCost: sidebar.CostHidden(r.CostOpt),
		}
		if a.CostOK {
			ag.CostUSD = &a.Cost
		}
		agents = append(agents, ag)
	}
	return agents, nil
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

// ReadChecked is ReadStatus, but a decision (agent.Deciding) is checked against pane's screen first: Claude
// fires no hook when its dialog is answered No or Esc, so the state would stay waiting. Its own input box
// back at the bottom (screen.ClaudeState) means no dialog is open; the state it shows, idle or working, is
// recorded and returned.
func ReadChecked(c *tmux.Client, pane string) (Status, error) {
	st, err := ReadStatus(c, pane)
	// Only permission prompts and questions: no other dialog's screen is known to replace the input box
	if err != nil || st.Trusted() != agent.Waiting || st.WaitingFor != agent.PermissionPrompt || !agent.ClaudeCommand(st.Command) {
		return st, err
	}
	// Claude draws a dialog seconds before its Notification records it: a fresh decision's dialog may not be
	// on screen yet
	sec, err := strconv.ParseInt(st.Since, 10, 64)
	if err != nil || now().Sub(time.Unix(sec, 0)) < dialogGrace {
		return st, nil
	}
	capture, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return st, err
	}
	state := screen.ClaudeState(capture)
	if state == "" {
		return st, nil
	}
	if err := agent.DialogGone(c, pane, state, st.Since); err != nil {
		return st, err
	}
	// A hook may have moved the pane on meanwhile
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
// without a trusted state (Codex or Grok without its hooks) fails at once.
func Wait(c *tmux.Client, clk Clock, pane string, timeout time.Duration, done func(Status) bool) (Status, error) {
	st, err := ReadStatus(c, pane)
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

// poll reads pane's status until check is done or fails, the state stays untrusted for lostGrace
// (errGone), or timeout passes (ErrTimeout); check also gets the time since polling started.
func poll(c *tmux.Client, clk Clock, pane string, timeout time.Duration, check func(Status, time.Duration) (bool, error)) (Status, error) {
	start := clk.Now()
	var lost time.Time
	for {
		st, err := ReadStatus(c, pane)
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
// as a single prompt. A busy agent queues it; one waiting on a decision, or a busy Claude whose screen shows
// no input box (a dialog not reported yet), is refused.
func Prompt(c *tmux.Client, clk Clock, pane, text string) error {
	st, err := ReadChecked(c, pane)
	if err != nil {
		return err
	}
	if err := st.checkPromptable(); err != nil {
		return err
	}
	if st.Trusted() == agent.Working && agent.ClaudeCommand(st.Command) {
		// Claude records a dialog seconds after drawing it (not at all while someone types in it)
		capture, err := c.Run("capture-pane", "-p", "-t", pane)
		if err != nil {
			return err
		}
		if screen.ClaudeState(capture) == "" {
			return errors.New("agent is working, but its screen shows no input box: a dialog may be open; read it and answer with send-keys")
		}
	}
	return paste(c, clk, pane, text)
}

func paste(c *tmux.Client, clk Clock, pane, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty prompt")
	}
	// From a file, not argv: tmux splits commands at an argument's trailing ";" and caps a command's size
	f, err := os.CreateTemp("", "dev-agent-prompt-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	buffer := "dev-agent-" + strconv.Itoa(os.Getpid())
	if _, err := c.Run("load-buffer", "-b", buffer, f.Name()); err != nil {
		return err
	}
	if _, err := c.Run("paste-buffer", "-p", "-d", "-b", buffer, "-t", pane); err != nil {
		// -d only deletes after a paste
		_, _ = c.Run("delete-buffer", "-b", buffer)
		return err
	}
	clk.Sleep(enterDelay)
	_, err = c.Run("send-keys", "-t", pane, "Enter")
	return err
}

// PromptWait prompts a settled agent and waits until the hook counted the prompt (@agent_prompts) and
// the agent settled again, so a turn shorter than a poll isn't missed and the settled state from before
// isn't taken for done. /clear starts no turn: it waits for the new session's SessionStart (@agent_session)
// instead. No count (or session) within stall is ErrStalled; no settled state within timeout is ErrTimeout.
func PromptWait(c *tmux.Client, clk Clock, pane, text string, stall, timeout time.Duration) (Status, error) {
	before, err := ReadChecked(c, pane)
	if err != nil {
		return before, err
	}
	switch st := before.Trusted(); {
	case st == "":
		return before, errNoState
	case !before.Settled():
		// Its Stop would end the wait before the queued prompt even starts
		return before, fmt.Errorf("agent is %s: wait for it first", st)
	}
	if err := before.checkPromptable(); err != nil {
		return before, err
	}
	// Claude Code's aliases of /clear too
	reset := slices.Contains([]string{"/clear", "/reset", "/new"}, strings.TrimSpace(text))
	if reset && before.Session == "" {
		return before, errors.New("--wait confirms /clear by the new session its hook records, and this agent's hook records none (Codex, Grok): send it without --wait")
	}
	if err := paste(c, clk, pane, text); err != nil {
		return before, err
	}
	return poll(c, clk, pane, timeout, func(st Status, elapsed time.Duration) (bool, error) {
		picked := st.Prompts != before.Prompts
		if reset {
			picked = st.Session != "" && st.Session != before.Session
		}
		if picked {
			return st.Settled(), nil
		}
		if elapsed >= stall {
			return false, fmt.Errorf("%w within %s", ErrStalled, stall)
		}
		return false, nil
	})
}

// Read returns the last lines of pane's history and screen, trailing spaces and blank lines trimmed.
// An agent drawing on the alternate screen (Claude Code's full-screen UI, Codex) has only that screen.
func Read(c *tmux.Client, pane string, lines int) (string, error) {
	if lines < 1 {
		return "", fmt.Errorf("lines must be at least 1, got %d", lines)
	}
	out, err := c.Run("capture-pane", "-p", "-J", "-S", "-"+strconv.Itoa(lines), "-t", pane)
	if err != nil {
		return "", err
	}
	all := strings.Split(out, "\n")
	for i, l := range all {
		all[i] = strings.TrimRight(l, " ")
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
	return strings.Join(all, "\n") + "\n", nil
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
