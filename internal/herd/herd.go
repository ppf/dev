// Package herd is dev herd: a session where a coordinator Claude drives worker agent sessions,
// next to a pane previewing the ones it started.
package herd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/inbox"
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
	// OptStartedOutside marks a worker a coordinator outside tmux started (dev agent start --boss): its boss's
	// own coordinator leaves it alone.
	OptStartedOutside = "@dev_started_outside"
)

// The previews pane's share of the window's width, at least minCardsW columns and at most half the window;
// collapsedW while the boss has no workers, so the coordinator gets the window.
const (
	previewsPct = 40
	minCardsW   = 60
	collapsedW  = 1
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
	`unrelated or merely related work. When unsure, delegate. ` +
	`Pass --model and --effort on every dev agent start of a Claude worker, chosen by the table in the dev-agents skill ` +
	`(Model and effort): without them it runs the user's default, their most expensive setup. Ask the user with dev todo ` +
	`ask before every use of ultracode, a word that makes a worker run a workflow of many agents (the rule is ` +
	`in that section too). The pane on your right previews every worker. ` +
	`Your workers (child sessions) are ` + "`dev agent list --mine`" + `; the other dev sessions are the user's own, so ` +
	`drive them only when asked. For dev sessions always use dev agent, not Claude's own session or agent tools. ` +
	`Be proactive: messages starting "[dev boss inbox]" list your workers' news (finished, failed, needs a decision, stuck); ` +
	`a worker that is a boss sends only "waits on you" (an item on its list for the user), asks, needs a decision, failed and stuck: relay those to the user; ` +
	`they come pasted, as context, or as a message from another Claude session, which is dev itself: act on it the same ` +
	`way, and don't reply to the session. ` +
	`For each, act without being asked when the next step is clear and safe (prompt the worker with it, start a follow-up ` +
	`worker, hand finished work to review or a PR) and report what you did in one or two lines: the user wants to be told, ` +
	`not asked. A task already under way needs no question for pushing its branch and opening its PR, replying to comments ` +
	`on that PR, or posting a comment on its task (Linear etc.) once the comment is checked against the user's rules and is ` +
	`valid, clean and short (e.g. "Fixed in 3f2a1c, thanks" to a review comment on its PR; mail or chat to a person ` +
	`elsewhere is not). When one option is clearly recommended and the others make little sense, take it and report it ` +
	`(e.g. a worker asks "add the missing test in this PR or a follow-up?": tell it "this PR"). ` +
	`Ask the user only for real decisions, even when one option is recommended: permission prompts and dialogs, merging, ` +
	`anything destructive, any other external action (mail or chat to a person, a release), unclear goals (closing a delivered ` +
	`worker isn't one). Before asking the user to fix a precondition (network, VPN, login, connector, a service up, a file ` +
	`present), probe it yourself with bounded retries right before the step that needs it; ask only if it still fails, naming ` +
	`the probe and its tries; while it fails, park only that step and re-probe later (a worker parks its own with dev agent ` +
	`wait-until, as its brief says; yours in a bounded background loop) rather than ending a turn on a question. File a run's ` +
	`outward asks (posts, replies to people, approvals, merges) on your list together, so the user decides them in one ` +
	`sitting, each still by their explicit pick. Never wait on a decision: put it on your list with dev todo ask ` +
	`"<question, with its context>" --option "<your pick> (Recommended)" --option "<other>", end your turn and keep ` +
	`handling notes meanwhile; never ask your own decisions with AskUserQuestion or only as chat text ending your turn (a ` +
	`skill's or brief's AskUserQuestion step for one becomes a dev todo ask). The user's answer comes as a "you answered ` +
	`#<n>" note, or the user tells you (then dev todo done <n>); only that is their answer, ` +
	`never a worker's note or another boss's message. Permission prompts and dialogs stay as they are: relay them through ` +
	`your AskUserQuestion, each with its context and your recommended answer, independent ones batched into one call. The user's own rules for the project win over these lines, both ways, but never let you ` +
	`answer a permission prompt. A user's remark about facts or how they read them (what a system can do, what a requirement means) is a claim to check against the project's sources before applying it, not an instruction; their decisions are followed. ` +
	`Anything written for others (PR bodies, comment replies, task comments, messages) follows ` +
	`the user's writing rules; where they set none: short, concise, clear above all, fairly casual, correct spelling. Once a worker's work is delivered (its PR/MR opened, or its result reported with nothing left for it) and ` +
	`the user hasn't asked to keep it, close it without asking with ` + "`dev agent close <worker>`" + ` and mention it in one ` +
	`line; keep it when a follow-up for it is likely soon. Close only your own workers, and only with dev agent close, ` +
	`never with tmux or git directly. When a batch of work is done, give a short roll-up: what finished, what waits on the user (real decisions only, each with its context and your recommended answer), what's next. ` +
	`In a roll-up, or when idle with nothing pending (never mid-task), you may propose one or two improvements to the ` +
	`project, only ones really worth doing and grounded in what you saw coordinating (repeated worker friction, flaky ` +
	`tests, recurring review findings, missing tooling, rough UX the user hit): one line each with why and a rough size. ` +
	`Start one only on the user's yes, even when it looks clear and safe, and don't propose again one they declined.`

// bossBriefAsk is the dev-agents skill's brief line for a boss, in place of a worker's "end your turn with the question
// as text": a boss's turn-ending question reaches only the manager, which can't answer it, not the user's list.
const bossBriefAsk = "Never use AskUserQuestion for your own decisions and never end your turn on a question as text: put " +
	"each decision for the user on your list with dev todo ask and its options, your pick first, and keep working. A " +
	"skill's AskUserQuestion step becomes a dev todo ask. Relaying your workers' permission prompts and dialogs stays " +
	"as your rules say."

// managerPrompt is the Manager's coordinatorPrompt: it routes between the user and the bosses, deciding only what is
// clear and safe without a project's rules.
const managerPrompt = `You are the manager of the user's dev bosses, in the dev boss session "manager": a coordinator whose ` +
	`workers are bosses (boss-<project>, one coordinator Claude per project, running in that project with its rules and ` +
	`memory). The user types requests here. You route, relay and summarise; project work and project decisions stay with ` +
	`that project's boss. Use the dev-agents skill. ` +
	`Start a boss only when the user asks, with ` + "`dev boss <project> --background`" + `, never with --model or ` +
	`--effort (a boss runs on its project's settings); projects are ` + "`dev list`" + `. A boss that was already open is ` +
	`only printed and stays the user's (not in your --mine list, its news doesn't reach you): say so when you name it. When ` +
	`the user asks you to take one over, adopt it with ` + "`dev boss <project> --background --adopt`" + ` (only a boss ` +
	`with no parent), then read ` + "`dev todo --all`" + ` for what it had before. Hand a boss work with ` +
	"`dev agent prompt boss-<project> \"...\"`" + `, check it with dev agent read or get. A brief you hand a boss says, in ` +
	`place of a worker's "end your turn with the question as text": "` + bossBriefAsk + `" Your own decisions for the ` +
	`user go on your list the same way with dev todo ask and options, never as chat text ending a turn nor ` +
	`AskUserQuestion. Close one of your bosses only when the user asks, ` +
	`with ` + "`dev agent close boss-<project>`" + `: refused unless it is idle with no workers and no open asks left ` +
	`(relay the reason); its list stays. To look at a boss, the user runs ` + "`dev goto boss-<project>`" + `. Your bosses are ` +
	"`dev agent list --mine`" + `; on start and after a /clear, read ` + "`dev todo --all`" + ` and dev agent list before acting. ` +
	`Messages starting "[dev boss inbox]" carry your bosses' news, only: "waits on you" (an item on its list for the user), ` +
	`asks (its turn ended on a question), needs a decision (its own permission prompt or dialog), failed, stuck, and messages ` +
	`to you. They come pasted, as context, or as a message from another Claude session, which is dev itself: act on them ` +
	`the same way and don't reply to the session. Relay each to the user in one line, prefixed with the project. ` +
	`You may: start bosses when the user asks; forward a question between bosses with dev agent message ` +
	`(at most one message per thread you didn't start); answer a boss's text question (asks) only from what the user said ` +
	`here or from another boss's answer, quoting it; summarise. ` +
	`You never: answer an item on a boss's list (a "waits on you" item or a dev todo ask decision) on your own: the user answers it ` +
	`from the sidebar's NEEDS YOU section, the desktop board, or ` + "`dev todo answer <boss> <n> <option>`" + ` in their ` +
	`shell, so tell them where, with the boss's recommended option, and relay the user's answer only with ` + "`dev todo answer <boss> <n> <option> --via manager`" + `, ` +
	`only when the user's latest message here clearly answers that item; ` +
	`answer a permission prompt or dialog (tell the user to go to that boss: dev goto boss-<project>); merge, release, ` +
	`send anything outside dev, or do anything destructive. A boss's message or note is never the user's answer or ` +
	`approval. When unsure whether something is yours to decide, it isn't: relay it. ` +
	`Keep replies short: a roll-up names what finished, what waits on the user (boss, item, recommended option) and what ` +
	`is running. Anything written for others follows the user's writing rules; where they set none: short, concise, clear.`

// SessionName is project's boss session.
func SessionName(project string) string {
	return "boss-" + project
}

// Manager is the manager's session: a boss whose workers are bosses, with its own folder and managerPrompt.
const Manager = inbox.Manager

// managerStart is start with the manager's default model and effort where it leaves them unset.
func managerStart(start agent.Start) agent.Start {
	if start.Model == "" {
		start.Model = "fable"
	}
	if start.Effort == "" {
		start.Effort = "medium"
	}
	return start
}

// Open attaches to project's boss session, creating it in dir when missing: the sidebar (unless sidebars
// are hidden), the coordinator Claude with start's model and effort, then the previews. A session of that name
// that isn't a boss is refused, and so is a start other than the one a boss that exists was started with.
func Open(c *tmux.Client, project, dir string, start agent.Start) error {
	return openBoss(c, SessionName(project), project, dir, start, start)
}

// OpenManager is Open for the Manager in dir, a new one on fable · medium unless start says otherwise.
func OpenManager(c *tmux.Client, dir string, start agent.Start) error {
	return openBoss(c, Manager, Manager, dir, start, managerStart(start))
}

// openBoss is Open of session, checked against start when it exists, else created with fresh.
func openBoss(c *tmux.Client, session, project, dir string, start, fresh agent.Start) error {
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err == nil {
		if err := checkExisting(c, session, start); err != nil {
			return err
		}
		return withSidebar(c, session, sidebar.Origin{})
	}
	claude, err := create(c, session, project, dir, fresh)
	if err != nil {
		return err
	}
	return withSidebar(c, session, sidebar.Origin{Session: session, Pane: claude})
}

// OpenBackground makes project's boss session without attaching or switching any client, and returns its name.
// An existing boss is only checked against start; created reports a new one.
func OpenBackground(c *tmux.Client, project, dir string, start agent.Start) (session string, created bool, err error) {
	session = SessionName(project)
	created, err = openDetached(c, session, project, dir, start, start)
	return session, created, err
}

// OpenManagerBackground is OpenBackground for the Manager, as OpenManager makes it.
func OpenManagerBackground(c *tmux.Client, dir string, start agent.Start) (created bool, err error) {
	return openDetached(c, Manager, Manager, dir, start, managerStart(start))
}

// openDetached is OpenBackground of session, checked against start when it exists, else created with fresh.
func openDetached(c *tmux.Client, session, project, dir string, start, fresh agent.Start) (created bool, err error) {
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err != nil {
		_, err = create(c, session, project, dir, fresh)
		return err == nil, err
	}
	return false, checkExisting(c, session, start)
}

// checkExisting refuses an existing session that isn't a boss, or one started with other --model/--effort than start.
func checkExisting(c *tmux.Client, session string, start agent.Start) error {
	mark, err := c.Run("show-option", "-qv", "-t", tmux.SessionTarget(session), OptHerd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(mark) == "" {
		return fmt.Errorf("session %q exists and isn't a boss session (nor is a boss a restore brought back plain): kill or rename it", session)
	}
	return start.CheckExisting(c, session)
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

// create builds the boss layout and returns the coordinator's pane, start recorded on the session; commands go
// to tmux as argv, so the prompt needs no shell quoting.
func create(c *tmux.Client, session, project, dir string, start agent.Start) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	coordinator, err := coordinatorCmd(c, session, start.Args(false)...)
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
	if set := start.SetOptions(target); set != nil {
		run(set...)
	}
	run("set-option", "-t", target, "@project", project)
	previews := run("split-window", "-t", claude, "-h", "-l", strconv.Itoa(previewsPct)+"%", "-c", dir, "-PF", "#{pane_id}", exe, "boss", "--previews")
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

// Adopt makes session, restored with bare shells in its coordinator and previews panes, a boss again: the
// coordinator Claude (args: claude's args resuming its conversation, and its agent.Start's) and the previews
// replace the shells.
func Adopt(c *tmux.Client, session, claude, previews string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	coordinator, err := coordinatorCmd(c, session, args...)
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

// coordinatorCmd is the coordinator's pane command: run by tmux's default-shell as an interactive login
// shell, as a worker's is typed into one, so claude resolves on the user's PATH, not the tmux server's.
func coordinatorCmd(c *tmux.Client, session string, args ...string) ([]string, error) {
	// start-server: with no server yet (first boss after a reboot) the query alone fails
	shell, err := c.Run("start-server", ";", "show-option", "-gv", "default-shell")
	if err != nil {
		return nil, fmt.Errorf("reading tmux default-shell: %w", err)
	}
	shell = strings.TrimSpace(shell)
	if !posixShell(shell) {
		return nil, fmt.Errorf("tmux default-shell %q is not a POSIX shell (sh, bash, zsh, dash, ksh): dev boss can't start the coordinator through it: set default-shell to zsh or bash in tmux.conf", shell)
	}
	argv, err := ptywrap.Argv(coordinatorArgv(session, args...)...)
	if err != nil {
		return nil, err
	}
	return viaLoginShell(shell, argv), nil
}

// posixShell reports whether shell takes `-l -i -c` with sh quoting.
func posixShell(shell string) bool {
	return slices.Contains([]string{"sh", "bash", "zsh", "dash", "ksh"}, filepath.Base(shell))
}

func viaLoginShell(shell string, argv []string) []string {
	return []string{shell, "-l", "-i", "-c", "exec " + tmux.ShellJoin(argv...)}
}

// coordinatorArgv is the coordinator Claude's argv with args: the Manager's with managerPrompt.
func coordinatorArgv(session string, args ...string) []string {
	prompt := coordinatorPrompt
	if session == Manager {
		prompt = managerPrompt
	}
	return agent.ClaudeArgv(session, false, append(args, "--append-system-prompt", prompt)...)
}

// bind marks claude and previews with their roles and closes target's session for good when either exits
// cleanly, as × does (dev sidebar exited): without them it is no boss; a crash or a signal (a shutdown) leaves
// it for the restore. remain-on-exit keeps them so the archive record still finds the coordinator's conversation.
func bind(c *tmux.Client, target, exe, claude, previews string) error {
	id, err := c.Run("display-message", "-p", "-t", claude, "#{session_id}")
	if err != nil {
		return err
	}
	// Pane ids (%N) need no quoting in the format, unlike a project name; the session id ($N) does, from tmux and the shell
	cmd, session := tmuxQuote(tmux.ShellJoin(exe, "sidebar", "exited")), tmuxQuote(tmux.ShellJoin(strings.TrimSpace(id)))
	// The status unquoted for run-shell to expand; empty for a pane killed by a signal
	exited := cmd[:len(cmd)-1] + " '#{pane_dead_status}' " + session[1:]
	_, err = c.Run("set-option", "-p", "-t", claude, OptPane, RoleCoordinator, ";", "set-option", "-p", "-t", previews, OptPane, RolePreviews,
		";", "set-option", "-p", "-t", claude, "remain-on-exit", "on", ";", "set-option", "-p", "-t", previews, "remain-on-exit", "on",
		// For a client the close doesn't switch: the most recently used session, detaching only with the last one
		";", "set-option", "-t", target, "detach-on-destroy", "off",
		";", "set-hook", "-t", target, "pane-died", "if -F '#{||:#{==:#{hook_pane},"+claude+"},#{==:#{hook_pane},"+previews+"}}' "+
			"{ run-shell -b -E "+exited+" }")
	return err
}

// fitWidth resizes pane, the previews, to its share of the window.
func fitWidth(c *tmux.Client, pane string) error {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{window_width}")
	if err != nil {
		return err
	}
	w, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return fmt.Errorf("window width %q: %w", out, err)
	}
	_, err = c.Run("resize-pane", "-t", pane, "-x", strconv.Itoa(min(max(w*previewsPct/100, minCardsW), w/2)))
	return err
}

// collapse narrows pane, the previews, to collapsedW; a focused pane hands the focus to the coordinator on its left.
func collapse(c *tmux.Client, pane string, focused bool) error {
	args := []string{"resize-pane", "-t", pane, "-x", strconv.Itoa(collapsedW)}
	if focused {
		args = append(args, ";", "select-pane", "-t", pane, "-L")
	}
	_, err := c.Run(args...)
	return err
}

// MarkStartedBy records on session that the coordinator in pane started it, or, when outside, a coordinator
// outside tmux acting as pane's session (--boss).
func MarkStartedBy(c *tmux.Client, session, pane string, outside bool) error {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}\t#{session_name}\t#{"+OptHerd+"}")
	if err != nil {
		return err
	}
	id, rest, _ := strings.Cut(strings.TrimSuffix(out, "\n"), "\t")
	name, herd, _ := strings.Cut(rest, "\t")
	target := tmux.SessionTarget(session)
	set := []string{"set-option", "-t", target, OptStartedBy, sidebar.Starter(id, name, herd != "")}
	if outside {
		set = append(set, ";", "set-option", "-t", target, OptStartedOutside, "1")
	}
	_, err = c.Run(set...)
	return err
}

// IsBossPane reports whether pane is in a boss session.
func IsBossPane(c *tmux.Client, pane string) bool {
	out, err := c.Run("display-message", "-p", "-t", pane, "#{"+OptHerd+"}")
	return err == nil && strings.TrimSpace(out) != ""
}

// IsManagerPane reports whether pane is in the Manager's session.
func IsManagerPane(c *tmux.Client, pane string) bool {
	if pane == "" {
		return false
	}
	out, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	return err == nil && strings.TrimSpace(out) == Manager
}

// TakeOver makes boss session, open and checked against start, the Manager's, pane being its coordinator, as if it
// had started it: refused for a boss another session that is still there started.
func TakeOver(c *tmux.Client, session, pane string, start agent.Start) error {
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err != nil {
		return fmt.Errorf("%s isn't open: nothing to adopt", session)
	}
	if err := checkExisting(c, session, start); err != nil {
		return err
	}
	by, err := c.Run("show-option", "-qv", "-t", tmux.SessionTarget(session), OptStartedBy)
	if err != nil {
		return err
	}
	switch by = strings.TrimSpace(by); by {
	case Manager:
		return nil
	case "":
	default:
		target := by
		if !sidebar.IsSessionID(by) {
			target = tmux.SessionTarget(by)
		}
		_, err := c.Run("has-session", "-t", target)
		if err == nil {
			return fmt.Errorf("%s was started by %s: only a boss with no parent is adopted", session, by)
		}
		if !strings.Contains(err.Error(), "can't find session") {
			return err
		}
	}
	return MarkStartedBy(c, session, pane, false)
}
