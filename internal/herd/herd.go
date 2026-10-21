// Package herd is dev herd: a session where a coordinator Claude drives worker agent sessions,
// next to a pane previewing the ones it started.
package herd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/ptywrap"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// Session options dev herd and dev agent start set.
const (
	// OptHerd marks a herd session; its sessions never show as tiles.
	OptHerd = sidebar.OptHerd
	// OptStartedBy is the session whose coordinator started a worker, see sidebar.Starter.
	OptStartedBy = "@dev_started_by"
)

// OptView is the boss session option holding the previews' view: compactView, else the full tiles.
const OptView = "@dev_boss_view"

const compactView = "compact"

// The previews pane's share of the window's width: the full view's, and the compact view's (at least
// minTileW columns, at most half the window).
const (
	fullPct    = 55
	compactPct = 30
)

// OptPane is the pane option holding a boss pane's role, which the resurrect save records.
const OptPane = "@dev_boss_pane"

// Roles of a boss's panes (OptPane).
const (
	RoleCoordinator = "coordinator"
	RolePreviews    = "previews"
)

// coordinatorPrompt is appended to the coordinator's system prompt, so its input box starts empty.
const coordinatorPrompt = `You are the coordinator (the boss) of a dev boss session. The user types requests here, such as ` +
	`"start workers for X and Y" or "tell fix-login to add tests". Carry them out by driving worker agents ` +
	`with the dev-agents skill (dev agent list/start/prompt/wait/read). Delegate, don't do: you only coordinate (start, ` +
	`prompt and read workers, relay their results, ask the user, and run single-command lookups to route work). Anything ` +
	`longer (multi-step research, browser testing, drafting, file edits, multi-call verification) goes to a worker. Each new ` +
	`feature or piece of work gets a fresh worker (dev agent start with a new worktree name); prompt an existing one of ` +
	`yours only with changes to its current work (fixes, review findings, changes to the same feature or PR), never with ` +
	`unrelated or merely related work. When unsure, delegate. The pane on your right previews every worker. ` +
	`Your workers (child sessions) are ` + "`dev agent list --mine`" + `; the other dev sessions are the user's own, so ` +
	`drive them only when asked. For dev sessions always use dev agent, not Claude's own session or agent tools. ` +
	`Be proactive: messages starting "[dev boss inbox]" list your workers' news (finished, failed, needs a decision, stuck); ` +
	`they come pasted, as context, or as a message from another Claude session, which is dev itself: act on it the same ` +
	`way, and don't reply to the session. ` +
	`For each, act without being asked when the next step is clear and safe (prompt the worker with it, start a follow-up ` +
	`worker, hand finished work to review or a PR) and report what you did in one or two lines. Ask the user only for real ` +
	`decisions: permission prompts, anything destructive or external (merging, pushing, posting), unclear goals (closing a delivered worker isn't one); batch ` +
	`the questions. Once a worker's work is delivered (its PR merged, or its result reported with nothing left for it) and ` +
	`the user hasn't asked to keep it, close it without asking with ` + "`dev agent close <worker>`" + ` and mention it in one ` +
	`line; keep it when a follow-up for it is likely soon. Close only your own workers, and only with dev agent close, ` +
	`never with tmux or git directly. When a batch of work is done, give a short roll-up: what finished, what waits on the user, what's next. ` +
	`In a roll-up, or when idle with nothing pending (never mid-task), you may propose one or two improvements to the ` +
	`project, only ones really worth doing and grounded in what you saw coordinating (repeated worker friction, flaky ` +
	`tests, recurring review findings, missing tooling, rough UX the user hit): one line each with why and a rough size. ` +
	`Start one only on the user's yes, even when it looks clear and safe, and don't propose again one they declined.`

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
			return fmt.Errorf("session %q exists and isn't a boss session (nor is a boss a restore brought back plain): kill or rename it", session)
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
	coordinator, err := ptywrap.Argv(coordinatorArgv(session)...)
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
	claude := run(append([]string{"new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-s", session,
		"-c", dir, "-n", "Boss", "-PF", "#{pane_id}"}, coordinator...)...)
	run("set-option", "-t", target, OptHerd, "1")
	if project != "" {
		run("set-option", "-t", target, "@project", project)
	}
	previews := run("split-window", "-t", claude, "-h", "-l", strconv.Itoa(fullPct)+"%", "-c", dir, "-PF", "#{pane_id}", exe, "boss", "--previews")
	if err == nil {
		err = bind(c, target, exe, claude, previews)
	}
	run("select-pane", "-t", claude)
	return claude, err
}

// tmuxQuote double-quotes s as one argument in a tmux command string, for run-shell, which expands formats.
func tmuxQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "#", "##").Replace(s) + `"`
}

// MoveClients switches each client on session to the session it was on before, so ending session doesn't
// detach it; a client without a live one is left to the session's detach-on-destroy.
func MoveClients(c *tmux.Client, session string) error {
	out, err := c.Run("list-clients", "-t", session, "-F", "#{client_name}\t#{client_last_session}")
	if err != nil {
		return err
	}
	var errs []error
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		client, last, _ := strings.Cut(line, "\t")
		if last == "" {
			continue
		}
		if _, err := c.Run("switch-client", "-c", client, "-t", tmux.SessionTarget(last)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Adopt makes session, restored with bare shells in its coordinator and previews panes, a boss again: the
// coordinator Claude (resume: claude's args resuming its conversation) and the previews replace the shells.
func Adopt(c *tmux.Client, session, claude, previews string, resume []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	coordinator, err := ptywrap.Argv(coordinatorArgv(session, resume...)...)
	if err != nil {
		return err
	}
	target := tmux.SessionTarget(session)
	// Each respawned pane keeps the directory it was restored in
	if _, err := c.Run(append([]string{"respawn-pane", "-k", "-t", claude}, coordinator...)...); err != nil {
		return err
	}
	if _, err := c.Run("respawn-pane", "-k", "-t", previews, exe, "boss", "--previews"); err != nil {
		return err
	}
	if err := bind(c, target, exe, claude, previews); err != nil {
		return err
	}
	// Marked last: a half-restored boss stays a session dev boss refuses
	_, err = c.Run("set-option", "-t", target, OptHerd, "1")
	return err
}

// coordinatorArgv is the coordinator Claude's argv with args.
func coordinatorArgv(session string, args ...string) []string {
	return agent.ClaudeArgv(session, false, append(args, "--append-system-prompt", coordinatorPrompt)...)
}

// bind marks claude and previews with their roles and ends target's session when either closes, its clients
// moved off first: without them it is no boss; its sidebar comes and goes like any other.
func bind(c *tmux.Client, target, exe, claude, previews string) error {
	id, err := c.Run("display-message", "-p", "-t", claude, "#{session_id}")
	if err != nil {
		return err
	}
	// Pane ids (%N) need no quoting in the format, unlike a project name; the session id ($N) does, from tmux and the shell
	id = strings.TrimSpace(id)
	move := tmuxQuote(tmux.ShellJoin(exe, "boss", "--move-clients", id))
	_, err = c.Run("set-option", "-p", "-t", claude, OptPane, RoleCoordinator, ";", "set-option", "-p", "-t", previews, OptPane, RolePreviews,
		// For a client MoveClients can't move: the most recently used session, detaching only with the last one
		";", "set-option", "-t", target, "detach-on-destroy", "off",
		";", "set-hook", "-t", target, "pane-exited", "if -F '#{||:#{==:#{hook_pane},"+claude+"},#{==:#{hook_pane},"+previews+"}}' "+
			"{ run-shell "+move+" ; kill-session -t '"+id+"' }")
	return err
}

// setView records the view on pane's session and resizes pane, the previews, to its share of the window.
func setView(c *tmux.Client, pane string, compact bool) error {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{window_width}")
	if err != nil {
		return err
	}
	w, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return fmt.Errorf("window width %q: %w", out, err)
	}
	set := []string{"set-option", "-u", "-t", pane, OptView}
	cols := w * fullPct / 100
	if compact {
		set = []string{"set-option", "-t", pane, OptView, compactView}
		cols = min(max(w*compactPct/100, minTileW), w/2)
	}
	_, err = c.Run(append(set, ";", "resize-pane", "-t", pane, "-x", strconv.Itoa(cols))...)
	return err
}

// MarkStartedBy records on session that the coordinator in pane started it.
func MarkStartedBy(c *tmux.Client, session, pane string) error {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}\t#{session_name}\t#{"+OptHerd+"}")
	if err != nil {
		return err
	}
	id, rest, _ := strings.Cut(strings.TrimSuffix(out, "\n"), "\t")
	name, herd, _ := strings.Cut(rest, "\t")
	_, err = c.Run("set-option", "-t", tmux.SessionTarget(session), OptStartedBy, sidebar.Starter(id, name, herd != ""))
	return err
}
