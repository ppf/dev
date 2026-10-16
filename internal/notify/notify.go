// Package notify sends a macOS notification (terminal-notifier) when an agent needs input, finishes or
// fails, unless its pane is in view.
package notify

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/ppf/dev/internal/tmux"
)

// event is the part of a Claude or Codex hook's stdin that notifications use.
type event struct {
	Name      string          `json:"hook_event_name"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	Last      string          `json:"last_assistant_message"`
	Error     string          `json:"error"`
	Tool      string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// note is a notification's text, without its title (the session name).
type note struct {
	Subtitle string
	Message  string
	Sound    bool
}

const maxText = 100

// decide maps a hook event to its note; ok=false for events that never notify.
func decide(e event) (note, bool) {
	switch e.Name {
	case "Notification":
		return note{Subtitle: cmp.Or(clean(e.Title), "Needs input"), Message: cmp.Or(clean(e.Message), "Needs input"), Sound: true}, true
	case "PermissionRequest":
		var in struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(e.ToolInput, &in) // tool_input may be a string (a patch) without a description
		return note{Subtitle: "Needs approval", Message: cmp.Or(clean(in.Description), clean(e.Tool), "Needs approval"), Sound: true}, true
	case "Stop":
		return note{Subtitle: "Done", Message: cmp.Or(clean(e.Last), "Finished")}, true
	case "StopFailure":
		return note{Subtitle: "Failed", Message: cmp.Or(clean(e.Error), "Stopped on an error")}, true
	}
	return note{}, false
}

// clean collapses whitespace, drops a leading plist delimiter or dash (terminal-notifier reads arguments
// through NSUserDefaults, which parses the first as a property list and drops a value starting with
// "-") and truncates.
func clean(s string) string {
	s = strings.TrimLeft(strings.Join(strings.Fields(s), " "), `[({"<- `)
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText-1]) + "…"
	}
	return s
}

type paneView struct {
	Notify       string `tmux:"@dev_notify"`
	Activate     string `tmux:"@dev_notify_activate"`
	FocusEvents  bool   `tmux:"focus-events"`
	Session      string `tmux:"session_name"`
	WindowActive bool   `tmux:"window_active"`
	PaneActive   bool   `tmux:"pane_active"`
	Zoomed       bool   `tmux:"window_zoomed_flag"`
	Socket       string `tmux:"socket_path"`
	ServerPID    string `tmux:"pid"`
}

type clientView struct {
	Session string `tmux:"client_session"`
	Flags   string `tmux:"client_flags"`
}

// visible: the pane's window is current in its session and not zoomed onto another pane, and a focused
// client shows that session. Without focus-events a client stays "focused" after the terminal loses
// focus, so focus is unknown and the pane counts as not visible.
func visible(p paneView, clients []clientView) bool {
	if !p.FocusEvents || !p.WindowActive || p.Zoomed && !p.PaneActive {
		return false
	}
	return slices.ContainsFunc(clients, func(c clientView) bool {
		return c.Session == p.Session && slices.Contains(strings.Split(c.Flags, ","), "focused")
	})
}

// Run notifies for the Claude or Codex hook event in input; bundleID is the click's fallback for @dev_notify_activate.
func Run(c *tmux.Client, pane string, input []byte, bundleID string) error {
	// A field of the wrong type only matters to events that notify; Unmarshal still fills the rest.
	var e event
	err := json.Unmarshal(input, &e)
	n, ok := decide(e)
	if !ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notify: hook input: %w", err)
	}
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	args, ok, err := buildArgs(c, pane, n, tmuxBin, bundleID)
	if err != nil || !ok {
		return err
	}
	return send(args)
}

// buildArgs returns the terminal-notifier arguments for n on pane, or ok=false when @dev_notify is off or
// the pane is in view. A click runs tmuxBin to switch to the pane and raises the app @dev_notify_activate
// names, else bundleID, if any.
func buildArgs(c *tmux.Client, pane string, n note, tmuxBin, bundleID string) (args []string, ok bool, err error) {
	out, err := c.Run("display", "-p", "-t", pane, tmux.Format[paneView](), ";", "list-clients", "-F", tmux.Format[clientView]())
	if err != nil {
		return nil, false, err
	}
	paneOut, clientsOut, _ := strings.Cut(out, tmux.Sep+"\n")
	panes, err := tmux.Parse[paneView](paneOut + tmux.Sep + "\n")
	if err != nil {
		return nil, false, err
	}
	clients, err := tmux.Parse[clientView](clientsOut)
	if err != nil {
		return nil, false, err
	}
	p := panes[0]
	switch p.Notify {
	case "", "on":
	case "off":
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("notify: @dev_notify is %q, want on or off", p.Notify)
	}
	if visible(p, clients) {
		return nil, false, nil
	}
	args = []string{"-title", clean(p.Session), "-subtitle", n.Subtitle, "-message", n.Message, "-group", "dev-" + p.ServerPID + "-" + pane,
		"-execute", tmux.ShellJoin(tmuxBin, "-S", p.Socket, "switch-client", "-t", pane)}
	if app := cmp.Or(p.Activate, bundleID); app != "" {
		args = append(args, "-activate", app)
	}
	if n.Sound {
		args = append(args, "-sound", "default")
	}
	return args, true, nil
}

// send starts terminal-notifier without waiting for it: stdio is /dev/null (nothing holds the hook's
// pipes) and its own process group keeps it out of signals sent to the hook's group.
func send(args []string) error {
	bin, err := exec.LookPath("terminal-notifier")
	if err != nil {
		return fmt.Errorf("notify: terminal-notifier not in PATH (brew install terminal-notifier, or tmux set -g @dev_notify off): %w", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return cmd.Process.Release()
}
