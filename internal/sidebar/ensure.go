package sidebar

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// paneWindow is a pane's start command and window, from one display-message.
type paneWindow struct {
	Start  string `tmux:"pane_start_command"`
	Window string `tmux:"window_id"`
	Zoomed bool   `tmux:"window_zoomed_flag"`
}

// ResolveOrigin returns client's current session and that session's active pane; client "" means
// what `display-message -p` resolves for this process (its pane inside tmux, else the best session).
func ResolveOrigin(c *tmux.Client, client string) (Origin, error) {
	if client == "" {
		rows, err := displayRow[struct {
			Session string `tmux:"session_name"`
			Pane    string `tmux:"pane_id"`
		}](c, "", "#{session_name}", "#{pane_id}")
		if err != nil {
			return Origin{}, err
		}
		return Origin{Session: rows.Session, Pane: rows.Pane}, nil
	}
	// list-clients, not display-message -c: on tmux 3.8 `display-message -c TTY -p` formats for the
	// best client of the current session, whichever client TTY is.
	clients, err := tmux.Query[Client](c, "list-clients")
	if err != nil {
		return Origin{}, err
	}
	i := slices.IndexFunc(clients, func(cl Client) bool { return cl.TTY == client })
	if i < 0 {
		return Origin{}, fmt.Errorf("sidebar: no client %s", client)
	}
	session := clients[i].Session
	pane, err := c.Run("display-message", "-t", tmux.SessionTarget(session), "-p", "#{pane_id}")
	if err != nil {
		return Origin{}, err
	}
	return Origin{Session: session, Pane: strings.TrimSuffix(pane, "\n")}, nil
}

// displayRow formats vars for target ("" = this process's pane) with display-message and parses them into T.
func displayRow[T any](c *tmux.Client, target string, vars ...string) (T, error) {
	var zero T
	args := []string{"display-message"}
	if target != "" {
		args = append(args, "-t", target)
	}
	out, err := c.Run(append(args, "-p", strings.Join(vars, tmux.Sep)+tmux.Sep)...)
	if err != nil {
		return zero, err
	}
	rows, err := tmux.Parse[T](out)
	if err != nil {
		return zero, err
	}
	if len(rows) != 1 {
		return zero, fmt.Errorf("sidebar: display-message %q: %d rows, want 1", vars, len(rows))
	}
	return rows[0], nil
}

// PaneCommand is the shell command a sidebar pane runs: exec this binary's `sidebar`.
func PaneCommand() (string, error) {
	cmd, err := selfCommand("sidebar")
	if err != nil {
		return "", err
	}
	return "exec " + cmd, nil
}

// Ensure shows target's sidebar unless sidebars are hidden (toggle-sidebar.sh <session>): it focuses
// an existing one (not when compact mode's zoom hides it), replaces a bash sidebar, or creates one in
// target's active window and gives focus back to from's pane.
func Ensure(c *tmux.Client, target string, from Origin) error {
	return ensureSidebar(c, target, from, false)
}

// Toggle is prefix+S (toggle-sidebar.sh without arguments): when from's session shows a sidebar, every
// sidebar is hidden and @sidebar_hidden set; otherwise it is cleared and from's session gets one.
func Toggle(c *tmux.Client, from Origin) error {
	return ensureSidebar(c, from.Session, from, true)
}

// ensureSidebar is toggle-sidebar.sh: toggle mode without a session argument, ensure mode with one.
func ensureSidebar(c *tmux.Client, target string, from Origin, toggle bool) error {
	if target == "" {
		return errors.New("sidebar: no target session")
	}
	if !toggle {
		hidden, err := c.Run("show-option", "-gqv", optHidden)
		if err != nil {
			return err
		}
		if strings.TrimSpace(hidden) == "1" {
			return nil
		}
	}

	stored, err := storedSidebar(c, target)
	if err != nil {
		return err
	}

	if toggle {
		if stored != "" {
			if err := hideAll(c); err != nil {
				return err
			}
			if _, err := c.Run("set-option", "-g", optHidden, "1"); err != nil {
				return err
			}
			resetKeyTable(c, "")
			return nil
		}
		// Like bash's `|| true`: unsetting an option that isn't set is fine
		_, _ = c.Run("set-option", "-gu", optHidden)
	}

	var info paneWindow
	if stored != "" {
		info, err = displayRow[paneWindow](c, stored, "#{pane_start_command}", "#{window_id}", "#{window_zoomed_flag}")
		if err != nil {
			return err
		}
		if strings.Contains(info.Start, "sidebar.sh") {
			if err := replaceBashSidebar(c, target, stored, info); err != nil {
				return err
			}
			stored = ""
		}
	}

	if stored != "" {
		if target == from.Session && from.Pane == stored {
			pane, err := firstContentPane(c, target)
			if err != nil {
				return err
			}
			if pane != "" {
				if _, err := c.Run("select-pane", "-t", pane); err != nil {
					return err
				}
			}
			resetKeyTable(c, "")
		} else if !info.Zoomed {
			// A zoomed window (compact mode) hides the sidebar; focusing it would unzoom
			if _, err := c.Run("select-pane", "-t", stored); err != nil {
				return err
			}
			return setSelected(c, target)
		}
		return nil
	}

	anchor, err := firstContentPane(c, target)
	if err != nil {
		return err
	}
	if anchor == "" {
		return fmt.Errorf("sidebar: session %s has no pane to put a sidebar next to", target)
	}
	cmd, err := PaneCommand()
	if err != nil {
		return err
	}
	out, err := c.Run("split-window", "-t", anchor, "-hb", "-l", "40", "-PF", "#{pane_id}", cmd)
	if err != nil {
		return err
	}
	pane := strings.TrimSpace(out)
	if _, err := c.Run("set-option", "-t", tmux.SessionTarget(target), optSidebarPane, pane); err != nil {
		return err
	}

	if target == from.Session {
		if _, err := c.Run("select-pane", "-t", pane); err != nil {
			return err
		}
		return setSelected(c, target)
	}
	if from.Pane == "" {
		return nil
	}
	if _, err := c.Run("display-message", "-t", from.Pane, "-p", ""); err != nil {
		// The pane the command came from is gone: nothing to give focus back to
		return nil
	}
	_, err = c.Run("select-pane", "-t", from.Pane)
	return err
}

// storedSidebar returns session's @sidebar_pane, unsetting it when that pane no longer exists.
func storedSidebar(c *tmux.Client, session string) (string, error) {
	out, err := c.Run("show-option", "-qv", "-t", tmux.SessionTarget(session), optSidebarPane)
	if err != nil {
		return "", err
	}
	stored := strings.TrimSpace(out)
	if stored == "" {
		return "", nil
	}
	panes, err := c.Run("list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Fields(panes), stored) {
		return stored, nil
	}
	if _, err := c.Run("set-option", "-t", tmux.SessionTarget(session), "-u", optSidebarPane); err != nil {
		return "", err
	}
	return "", nil
}

// replaceBashSidebar kills session's bash sidebar pane (plan 3e), keeping its window's zoom, and forgets it.
func replaceBashSidebar(c *tmux.Client, session, pane string, w paneWindow) error {
	if _, err := c.Run("kill-pane", "-t", pane); err != nil {
		return err
	}
	if w.Zoomed {
		if _, err := c.Run("resize-pane", "-Z", "-t", w.Window); err != nil {
			return err
		}
	}
	_, err := c.Run("set-option", "-t", tmux.SessionTarget(session), "-u", optSidebarPane)
	return err
}

// hideAll kills every session's sidebar and unsets its @sidebar_pane, re-zooming a window the kill
// unzoomed (hide_all_sidebars).
func hideAll(c *tmux.Client) error {
	sessions, err := tmux.Query[struct {
		Name    string `tmux:"session_name"`
		Sidebar string `tmux:"@sidebar_pane"`
	}](c, "list-sessions")
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Sidebar == "" {
			continue
		}
		// Killing a pane hidden by zoom unzooms its window (compact mode); keep the zoom. An error means
		// a stale pane id: no window to re-zoom, and kill-pane fails too (ignored, like bash's || true).
		w, werr := displayRow[paneWindow](c, s.Sidebar, "#{pane_start_command}", "#{window_id}", "#{window_zoomed_flag}")
		_, _ = c.Run("kill-pane", "-t", s.Sidebar)
		if werr == nil && w.Zoomed {
			if _, err := c.Run("resize-pane", "-Z", "-t", w.Window); err != nil {
				return err
			}
		}
		if _, err := c.Run("set-option", "-t", tmux.SessionTarget(s.Name), "-u", optSidebarPane); err != nil {
			return err
		}
	}
	return nil
}

// switchTo ensures target's sidebar, then switches client ("" = tmux's current client) to target.
func switchTo(c *tmux.Client, client, target string, from Origin) error {
	// Like bash's >/dev/null 2>&1 || true: a missing sidebar never blocks the switch
	_ = Ensure(c, target, from)
	args := append(append([]string{"switch-client"}, clientFlag("-c", client)...), "-t", tmux.SessionTarget(target))
	if _, err := c.Run(args...); err != nil {
		return err
	}
	resetKeyTable(c, client)
	return nil
}
