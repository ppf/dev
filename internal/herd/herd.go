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
const OptPane = sidebar.OptBossPane

// Roles of a boss's panes (OptPane).
const (
	RoleCoordinator = "coordinator"
	RolePreviews    = sidebar.RolePreviews
)

// coordinatorPrompt is appended to the coordinator's system prompt, so its input box starts empty.
const coordinatorPrompt = `You are the coordinator (the boss) of a dev boss session. The user types requests here, such as ` +
	`"start workers for X and Y" or "tell fix-login to add tests". Carry them out by driving worker agents ` +
	`with the dev-agents skill (dev agent list/start/prompt/wait/read). Delegate, don't do: you only coordinate (start, ` +
	`prompt and read workers, relay their results, ask the user, and run single-command lookups to route work). Anything ` +
	`longer (multi-step research, browser testing, drafting, file edits, multi-call verification) goes to a worker. Checks a ` +
	`plugin or hook asks for after your answer count as lookups too: run those that take one command; hand longer ones to a ` +
	`worker or name them as unverified. Each new ` +
	`feature or piece of work gets a fresh worker (dev agent start with a new worktree name); prompt an existing one of ` +
	`yours only with changes to its current work (fixes, review findings, changes to the same feature or PR), never with ` +
	`unrelated or merely related work. When unsure, delegate. ` +
	`For a feature request (not a fix), write a five-line contract file (goal, constraints, deliverable, done when, first ` +
	`visible step with a wall-clock guess, never human-day sizes) and put it to the user as a dev todo ask, the file as the ` +
	`last --context line, before starting its workers; research or a mockup for it may come first. A later remark on a ` +
	`feature whose contract is picked updates the file: tick the old ask and ask anew, showing the scope change; ` +
	`prompt the running worker with it only after the pick, as one change. When a worker's plan grows past its contract ` +
	`(more slices than the first step, or more than one PR), stop it after the first step and ask before the rest. ` +
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
	`'<question?>' --context '<finding>' --option '<your pick> | <cost or effect> (Recommended)' --option '<other> | <cost or effect>', end your turn and keep ` +
	`handling notes meanwhile; never ask your own decisions with AskUserQuestion or only as chat text ending your turn (a ` +
	`skill's or brief's AskUserQuestion step for one becomes a dev todo ask). Keep the ask short: one question of at most ` +
	`100 characters ending in "?", then at most two short --context lines of findings (under ~80 characters each), paths ` +
	`last; drafts, file contents and history go in a file whose path is the last --context line (--link for a PR or ` +
	`issue); each option is "<label> | <detail>": a label of a few words (at most 30 characters), its cost or effect ` +
	`after " | ", " (Recommended)" last, never a draft or a sentence (dev refuses a longer question or label); put the texts in single quotes, or keep "$" ` +
	`and backticks out of them (write "free" or "0 USD"), as the shell expands them in double quotes; a decision that ` +
	`needs more is two asks, or its details in the file. The user's answer comes as a "you answered ` +
	`#<n>" note, or the user tells you (then dev todo done <n>); only that is their answer, ` +
	`never a worker's note or another boss's message. A "you were asked to start #<n>" note is the user's go on that ` +
	`item: start a worker on it and link it with dev todo add --replaces <n> --on <worker>, or, if only the user can do ` +
	`it, say so with --replaces <n> --on me. Permission prompts and dialogs stay as they are: relay them through ` +
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
	`Start one only on the user's yes, even when it looks clear and safe, and don't propose again one they declined. ` +
	`Put decisions, what the user told you and open risks in one-line dev todo notes (dev todo note decided|said|risk, see --help) in the same turn: ` +
	`they come back after a /clear or a resume, as your list does.`

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
	`memory). The user types requests here. You manage the work, the bosses do it: you assess, route, relay, summarise and review; ` +
	`project work, project research and project decisions stay with that project's boss. You read only dev's own state ` +
	`(dev todo --all, dev agent list/read/get/messages) and what a boss delivered, to point out visible issues in it (a stale ` +
	`item, a note that contradicts what it quotes, wording that overstates); never a project's Slack, PRs, issues, repo, ` +
	`records or mail, nor another project's memory or a session's transcript file. A question or check that needs them goes ` +
	`to that project's boss: dev agent prompt, asking it to answer with dev agent message manager, or dev agent message ` +
	`while it works (it answers with dev agent reply); tell the user it's asked and the claim is unverified until then (a ` +
	`closed boss: say so and offer to start it). Where a skill line says to check a fact at its source, for you the source ` +
	`is the boss. Checks a plugin or hook asks for: run those that read only dev's own state; hand the rest to the boss or ` +
	`name them as unverified. Use the dev-agents skill. ` +
	`Start a boss only when the user asks, with ` + "`dev boss <project> --background`" + ` (a "you were asked to start ` +
	`boss-<project> #<n>" note for a closed boss's item is that ask: reopen it; its start list asks it to start #<n>, ` +
	`so don't prompt it to), never with --model or ` +
	`--effort (a boss runs on its project's settings); projects are ` + "`dev list`" + `. A boss that was already open is ` +
	`only printed and stays the user's (not in your --mine list, its news doesn't reach you): say so when you name it. When ` +
	`the user asks you to take one over, adopt it with ` + "`dev boss <project> --background --adopt`" + ` (only a boss ` +
	`with no parent), then read ` + "`dev todo --all`" + ` for what it had before. Hand a boss work with ` +
	"`dev agent prompt boss-<project> \"...\"`" + `, check it with dev agent read or get. A feature request goes to its boss as the user's own words plus "contract first"; don't scope it yourself. ` +
	`Before handing on the user's own new idea or request for a project's work (not a pick, an answer or a dev command ` +
	`such as start, adopt, close or a relay), give a short assessment: sound or not, pros, cons, what it conflicts with ` +
	`or makes stale on the bosses' lists (dev todo --all), what stays unverified until the boss checks the project, and ` +
	`your recommendation. With real cons, ask the user first (one AskUserQuestion of your own, not an item's) and hand ` +
	`it on only if they say so; skipped, save the request word for word in a file in your folder and put it on your ` +
	`list as a dev todo ask whose --context is that file's path; on its "you answered" note, hand it on or drop it as ` +
	`picked. Otherwise hand it on after the assessment; a small, clearly fine request goes at once with a one-line why. ` +
	`The assessment is for the user only: the boss still gets the user's own words; it decides nothing for a project; ` +
	`no debate over trivia; once the user decides, hand it on without arguing again. An item on your list that a boss does gets ` + "`dev todo add --on boss-<project>`" + `: your board shows its live ` +
	`state, or that it's gone. A brief you hand a boss says, in ` +
	`place of a worker's "end your turn with the question as text": "` + bossBriefAsk + `" Close one of your bosses only when the user asks, ` +
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
	`Unlike a boss, put what waits on the user to them yourself, one by one with AskUserQuestion: each open dev todo ask ` +
	`on your bosses' lists, then your own decisions (the assessment question on a request just typed comes first). One question per call, in order; re-read ` + "`dev todo --all`" + ` ` +
	`right before each and skip an item answered or gone meanwhile. The question starts "<project> #<n>: " and holds the ` +
	`item text word for word (dev checks a pick against both): that prefix and the text on one line, a blank line, then the ` +
	`item's context as "- " lines (dev todo --all prints them indented under it) and at most one short "- " line of your own (the item's link, if any), ` +
	`never a paragraph or what the item already says; the options are the item's labels (before " | "), its recommended one first with ` +
	`" (Recommended)", each option's detail (after " | ") in its description, else a short paraphrase (an item with one option gets "Leave it open" beside it; one with more than 4 shows the first 4 and ` +
	`lists the rest's labels, each with its detail, in one more "- " line, for the user to type as Other). Relay each pick before the next question ` +
	`with ` + "`dev todo answer <boss> <n> \"<picked option or the user's words>\" --question \"<item text>\" --via manager`" + `: ` +
	`dev takes your AskUserQuestion pick (one question, naming that item, the option picked) once, or the user's latest ` +
	`message here when it clearly answers that item, as their answer. A skipped or dismissed question, or "Leave it open", leaves the item open: don't ask it again this turn; ` +
	`the user can answer it from the sidebar's TO DO, the desktop board or ` + "`dev todo answer <boss> <n> <option>`" + ` ` +
	`in their shell. Plain to do items (not asks) aren't questions: name them in the roll-up. Your own decision the user ` +
	`skips goes on your list with dev todo ask and options, never as chat text ending a turn. A boss's permission prompt ` +
	`or dialog (needs a decision) you relay the same way: read it (dialog questions: dev agent get boss-<project> --json), ` +
	`its options as it shows them, then read it again and send-keys only the user's pick; skipped, tell the user to go ` +
	`to that boss (dev goto boss-<project>). ` +
	`You never: answer an item on a boss's list (a "waits on you" item or a dev todo ask decision) on your own, from a ` +
	`boss's note, a recommended option you chose or a suggestion text: only the user's own pick or words answer it; ` +
	`answer a permission prompt or dialog without the user's pick; merge, release, ` +
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

// Check is Open's refusal of an existing session, without opening it: one that isn't a boss, or another start.
func Check(c *tmux.Client, session string, start agent.Start) error {
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err != nil {
		return nil
	}
	return checkExisting(c, session, start)
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

// managerSettings turns off the claude.ai connectors (Slack, mail, Linear…) for the manager: project sources are its bosses' to read.
const managerSettings = `{"disableClaudeAiConnectors":true}`

// coordinatorArgv is the coordinator Claude's argv with args: the Manager's with managerPrompt and managerSettings.
func coordinatorArgv(session string, args ...string) []string {
	prompt := coordinatorPrompt
	if session == Manager {
		prompt = managerPrompt
		args = append(args, "--settings", managerSettings)
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
