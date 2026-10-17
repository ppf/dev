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
const coordinatorPrompt = `You are the coordinator of a dev herd session. The user types requests here, such as ` +
	`"start workers for X and Y" or "tell fix-login to add tests". Carry them out by driving worker agents ` +
	`with the dev-agents skill (dev agent list/start/prompt/wait/read). The pane on your right previews every worker.`

// SessionName is the herd session for project ("" for none).
func SessionName(project string) string {
	if project == "" {
		return "herd"
	}
	return "herd-" + project
}

// Open attaches to project's herd session, creating it in dir when missing: the coordinator Claude on
// the left, the previews on the right. A session of that name that isn't a herd is refused.
func Open(c *tmux.Client, project, dir string) error {
	session := SessionName(project)
	target := tmux.SessionTarget(session)
	if _, err := c.Run("has-session", "-t", target); err == nil {
		mark, err := c.Run("show-option", "-qv", "-t", target, OptHerd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(mark) == "" {
			return fmt.Errorf("session %q exists and isn't a herd session (a restored herd isn't one either): kill or rename it", session)
		}
		return open.Attach(c, session)
	}
	if err := create(c, session, project, dir); err != nil {
		return err
	}
	return open.Attach(c, session)
}

// create builds the herd layout; commands go to tmux as argv, so the prompt needs no shell quoting.
func create(c *tmux.Client, session, project, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
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
		"-c", dir, "-n", "Herd", "-PF", "#{pane_id}", "claude", "--append-system-prompt", coordinatorPrompt)
	run("set-option", "-t", target, OptHerd, "1")
	// Without the coordinator (or the previews) it is no herd: closing either pane ends the session
	run("set-hook", "-t", target, "pane-exited", "kill-session -t "+tmux.ShellJoin(target))
	if project != "" {
		run("set-option", "-t", target, "@project", project)
	}
	run("split-window", "-t", claude, "-h", "-l", "55%", "-c", dir, exe, "herd", "--previews")
	run("select-pane", "-t", claude)
	return err
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
