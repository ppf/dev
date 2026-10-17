// Package herd is dev herd: a session where a coordinator Claude drives the other agent sessions,
// next to a pane previewing every one of them.
package herd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// Session options dev herd and dev agent start set.
const (
	// OptHerd marks a herd session; its sessions never show as tiles.
	OptHerd = sidebar.OptHerd
	// OptStartedBy is the session id ($N) of the session whose coordinator started a worker.
	OptStartedBy = "@dev_started_by"
)

// coordinatorPrompt is appended to the coordinator's system prompt, so its input box starts empty.
const coordinatorPrompt = `You are the coordinator (the boss) of a dev boss session. The user types requests here, such as ` +
	`"start workers for X and Y" or "tell fix-login to add tests". Carry them out by driving worker agents ` +
	`with the dev-agents skill (dev agent list/start/prompt/wait/read). The pane on your right previews every worker. ` +
	`Your workers (child sessions) are ` + "`dev agent list --mine`" + `; the other dev sessions are the user's own, so ` +
	`drive them only when asked. For dev sessions always use dev agent, not Claude's own session or agent tools. ` +
	`Be proactive: messages starting "[dev boss inbox]" list your workers' news (finished, failed, needs a decision, stuck). ` +
	`For each, act without being asked when the next step is clear and safe (prompt the worker with it, start a follow-up ` +
	`worker, hand finished work to review or a PR) and report what you did in one or two lines. Ask the user only for real ` +
	`decisions: permission prompts, anything destructive or external (merging, pushing, posting), unclear goals; batch ` +
	`the questions. When a batch of work is done, give a short roll-up: what finished, what waits on the user, what's next.`

// SessionName is the boss session for project ("" for none).
func SessionName(project string) string {
	if project == "" {
		return "boss"
	}
	return "boss-" + project
}

// Open attaches to project's boss session, creating it in dir when missing: the sidebar (unless sidebars
// are hidden), the coordinator Claude, then the previews. A session of that name that isn't a boss is refused.
func Open(c *tmux.Client, project, dir string) error {
	session := SessionName(project)
	target := tmux.SessionTarget(session)
	if _, err := c.Run("has-session", "-t", target); err == nil {
		mark, err := c.Run("show-option", "-qv", "-t", target, OptHerd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(mark) == "" {
			return fmt.Errorf("session %q exists and isn't a boss session (a restored boss isn't one either): kill or rename it", session)
		}
		return withSidebar(c, session, sidebar.Origin{})
	}
	claude, err := create(c, session, project, dir)
	if err != nil {
		return err
	}
	return withSidebar(c, session, sidebar.Origin{Session: session, Pane: claude})
}

// withSidebar ensures session's sidebar, focus going back to from's pane, and attaches; a sidebar that
// can't be made is reported after the attach rather than blocking it.
func withSidebar(c *tmux.Client, session string, from sidebar.Origin) error {
	ensureErr := sidebar.Ensure(c, session, from)
	if from.Pane != "" {
		// A new sidebar takes the focus; the user types to the coordinator
		if _, err := c.Run("select-pane", "-t", from.Pane); err != nil {
			return err
		}
	}
	if err := open.Attach(c, session); err != nil {
		return err
	}
	return ensureErr
}

// create builds the boss layout and returns the coordinator's pane; commands go to tmux as argv, so the
// prompt needs no shell quoting.
func create(c *tmux.Client, session, project, dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	width, height := open.ClientSize(c)
	target := tmux.SessionTarget(session)
	run := func(args ...string) string {
		if err != nil {
			return ""
		}
		var res string
		res, err = c.Run(args...)
		return strings.TrimSpace(res)
	}
	claude := run("new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session,
		"-c", dir, "-n", "Boss", "-PF", "#{pane_id}", "claude", "--append-system-prompt", coordinatorPrompt)
	run("set-option", "-t", target, OptHerd, "1")
	if project != "" {
		run("set-option", "-t", target, "@project", project)
	}
	previews := run("split-window", "-t", claude, "-h", "-l", "55%", "-c", dir, "-PF", "#{pane_id}", exe, "boss", "--previews")
	id := run("display-message", "-p", "-t", claude, "#{session_id}")
	// Without the coordinator (or the previews) it is no boss: closing either ends the session; its sidebar
	// comes and goes like any other. Pane and session ids ($N, %N) need no quoting, unlike a project name.
	run("set-hook", "-t", target, "pane-exited", "if -F '#{||:#{==:#{hook_pane},"+claude+"},#{==:#{hook_pane},"+previews+"}}' "+
		"{ kill-session -t '"+id+"' }")
	run("select-pane", "-t", claude)
	return claude, err
}

// MarkStartedBy records on session that the coordinator in pane started it.
func MarkStartedBy(c *tmux.Client, session, pane string) error {
	id, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return err
	}
	_, err = c.Run("set-option", "-t", tmux.SessionTarget(session), OptStartedBy, strings.TrimSpace(id))
	return err
}
