// Package sidebar is the session sidebar: a Bubble Tea list of tmux sessions running in its own pane
// (sidebar.sh), its keys and clicks (sidebar-action.sh), showing and hiding it per session
// (toggle-sidebar.sh), and dev goto.
package sidebar

import (
	"fmt"
	"os"

	"github.com/ppf/dev/internal/tmux"
)

// Options shared with the bash scripts, which read and write the same ones while both coexist.
const (
	optSidebarPane = "@sidebar_pane"             // session: the sidebar's pane id
	optSelected    = "@sidebar_selected_session" // global: the highlighted session, shared by every sidebar
	optHidden      = "@sidebar_hidden"           // global: "1" while prefix+S has hidden every sidebar
	optOrder       = "@sidebar_session_order"    // global: session names in sidebar order, one per line
	optProject     = "@project"                  // session: the project key the sidebar groups by
)

// OptHerd marks a dev boss session ("1"), which has no sidebar.
const OptHerd = "@dev_herd"

// Session is one sidebar entry.
type Session struct {
	Name   string
	Group  string // see Group
	Branch string // the agent pane's branch; "" without an agent pane
	Agent  string // "Claude", "Codex", or "" for no agent
	State  string // trusted hook state (working, waiting, idle) or ""
	Title  string // the agent pane's cleaned title, "" when generic
	PR     PR     // Branch's pull request; none without a branch or while no client views this sidebar
	Cost   string // the agent's estimated session cost, e.g. "$1.24"; "" when unknown

	ID        string // tmux session id ($N)
	StartedBy string // @dev_started_by: the id of the session whose coordinator started this one
	Depth     int    // how deep Order nested it under the session that started it
}

// Client is an attached client as list-clients reports it.
type Client struct {
	TTY      string `tmux:"client_tty"`
	Session  string `tmux:"client_session"`
	Activity int    `tmux:"client_activity"`
}

// Origin is where a command came from: the session and pane bash's `display-message -p` resolved.
type Origin struct {
	Session string
	Pane    string
}

// frame is one rendered screen: at most height lines, where each session row starts and where its PR tag is.
type frame struct {
	lines []string
	rows  []rowStart // display order
	links []link
}

// link is a clickable PR tag: columns [from, to) of line.
type link struct {
	line, from, to int
	url            string
}

// rowStart is the line a session's name row is drawn on (bash's @sidebar_row_map entry).
type rowStart struct {
	line    int
	session string
}

// selfCommand returns a shell command line running this binary with args.
func selfCommand(args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("sidebar: %w", err)
	}
	return tmux.ShellJoin(append([]string{exe}, args...)...), nil
}

// setSelected makes name the selected session of every sidebar; an empty name changes nothing.
func setSelected(c *tmux.Client, name string) error {
	if name == "" {
		return nil
	}
	_, err := c.Run("set-option", "-gq", optSelected, name)
	return err
}

// showMessage flashes msg on client's status line (tmux's current client when client is "").
func showMessage(c *tmux.Client, client, msg string) {
	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	// Like bash's `|| true`: a client that detached meanwhile has nowhere to show it.
	_, _ = c.Run(append(args, msg)...)
}

// firstContentPane returns the first pane of session's active window that isn't its sidebar ("" if none).
func firstContentPane(c *tmux.Client, session string) (string, error) {
	panes, err := tmux.Query[struct {
		ID      string `tmux:"pane_id"`
		Sidebar string `tmux:"@sidebar_pane"`
	}](c, "list-panes", "-t", tmux.SessionTarget(session))
	if err != nil {
		return "", err
	}
	for _, p := range panes {
		if p.ID != p.Sidebar {
			return p.ID, nil
		}
	}
	return "", nil
}

// activeClient returns the most recently active client viewing session (any session when session is "").
func activeClient(clients []Client, session string) (Client, bool) {
	var best Client
	found := false
	for _, c := range clients {
		if session != "" && c.Session != session {
			continue
		}
		// Strictly newer, so equal activity keeps list-clients order
		if !found || c.Activity > best.Activity {
			best, found = c, true
		}
	}
	return best, found
}
