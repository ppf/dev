// Package compact zooms the claude and nvim panes of the dev session a narrow client (an iPad over SSH)
// views, and undoes only its own zooms once the client is wide again (compact.sh). Every failure is
// warned in tmux before it is returned: the client-* hooks discard output.
package compact

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// threshold is the client width below which dev windows are zoomed.
const threshold = 140

// hookClient is one list-clients row; hook_client is a client_name.
type hookClient struct {
	Name    string `tmux:"client_name"`
	Width   int    `tmux:"client_width"`
	Session string `tmux:"client_session"`
}

// window is one list-windows row of the client's session.
type window struct {
	Index  int    `tmux:"window_index"`
	Name   string `tmux:"window_name"`
	Panes  int    `tmux:"window_panes"`
	Zoomed bool   `tmux:"window_zoomed_flag"`
	Active string `tmux:"pane_id"`
	Ours   string `tmux:"@dev_compact_zoomed"` // the pane compact zoomed; "" when the zoom isn't ours
}

// action is what Run does to one window.
type action int

const (
	none   action = iota
	zoom          // zoom zoomTarget's pane and mark it
	unzoom        // unzoom and clear the mark
	unmark        // only clear the mark: the zoom is gone or moved to another pane
)

// Run applies compact mode for the hook's client (#{hook_client}).
func Run(c *tmux.Client, client string) error {
	if client == "" {
		return fail(c, client, "no hook client passed")
	}
	// list-clients, not display-message -c: on tmux 3.8 `display-message -c TTY -p` formats for the best
	// client of the current session, and client_* formats in a hook can resolve to another client too
	clients, err := tmux.Query[hookClient](c, "list-clients")
	if err != nil {
		return fail(c, client, "list-clients failed: "+stderr(err))
	}
	i := slices.IndexFunc(clients, func(cl hookClient) bool { return cl.Name == client })
	if i < 0 {
		return nil // the client detached since the hook fired
	}
	width, session := clients[i].Width, clients[i].Session
	target := tmux.SessionTarget(session)
	// A session that vanished since the hook fired has no options; nothing to do
	if project, err := c.Run("show-option", "-qv", "-t", target, "@project"); err != nil || strings.TrimRight(project, "\n") == "" {
		return nil
	}
	// Ignored like bash: a session gone this late fails list-windows next, which warns
	sidebar, _ := c.Run("show-option", "-qv", "-t", target, "@sidebar_pane")
	sidebar = strings.TrimRight(sidebar, "\n")
	windows, err := tmux.Query[window](c, "list-windows", "-t", target)
	if err != nil {
		return fail(c, client, "list-windows failed for "+session)
	}
	// Per-window failures are only warned: bash's loop ended with status 0
	for _, win := range windows {
		apply(c, client, width, sidebar, win, fmt.Sprintf("=%s:%d", session, win.Index))
	}
	return nil
}

// apply carries out decide's action on win (target "=session:index"), warning on failure.
func apply(c *tmux.Client, client string, width int, sidebar string, win window, target string) {
	switch decide(width, win) {
	case zoom:
		out, err := c.Run("list-panes", "-t", target, "-F", "#{pane_id}")
		if err != nil {
			warn(c, client, "list-panes failed for "+target)
			return
		}
		pane := zoomTarget(win.Name, strings.Fields(out), sidebar)
		if pane == "" {
			return
		}
		if _, err := c.Run("resize-pane", "-Z", "-t", pane, ";",
			"set-option", "-wq", "-t", target, "@dev_compact_zoomed", pane); err != nil {
			warn(c, client, "zoom "+target+": "+stderr(err))
		}
	case unzoom:
		if _, err := c.Run("resize-pane", "-Z", "-t", target, ";",
			"set-option", "-wqu", "-t", target, "@dev_compact_zoomed"); err != nil {
			warn(c, client, "unzoom "+target+": "+stderr(err))
		}
	case unmark:
		if _, err := c.Run("set-option", "-wqu", "-t", target, "@dev_compact_zoomed"); err != nil {
			warn(c, client, "unmark "+target+": "+stderr(err))
		}
	}
}

// decide picks w's action at client width (compact.sh's loop without the pane lookup).
func decide(width int, w window) action {
	if width < threshold {
		// Zoom once per narrow period: a window already marked or zoomed (by hand) is left alone
		if w.Ours != "" || w.Zoomed || w.Panes < 2 {
			return none
		}
		if w.Name != "Agent" && w.Name != "Files" {
			return none
		}
		return zoom
	}
	if w.Ours == "" {
		return none
	}
	if w.Zoomed && w.Active == w.Ours {
		return unzoom
	}
	return unmark
}

// zoomTarget picks the pane to zoom from a window's panes in order: Agent's first non-sidebar pane,
// Files' first pane.
func zoomTarget(name string, panes []string, sidebar string) string {
	switch name {
	case "Agent":
		for _, p := range panes {
			if p != sidebar {
				return p
			}
		}
	case "Files":
		if len(panes) > 0 {
			return panes[0]
		}
	}
	return ""
}

// warn shows msg on client and appends it to @dev_compact_warnings.
func warn(c *tmux.Client, client, msg string) {
	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	// Best effort: warn is the only channel left to report on, so its own failures go nowhere
	_, _ = c.Run(append(args, "⚠ dev compact: "+msg)...)
	_, _ = c.Run("set-option", "-ga", "@dev_compact_warnings", msg+"; ")
}

// fail warns msg and returns it as an error.
func fail(c *tmux.Client, client, msg string) error {
	warn(c, client, msg)
	return errors.New(msg)
}

// stderr is what tmux printed for a failed command, as bash captured with 2>&1.
func stderr(err error) string {
	var e *tmux.Error
	if errors.As(err, &e) {
		return e.Stderr
	}
	return err.Error()
}
