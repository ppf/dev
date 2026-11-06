# dev

tmux workspace launcher for Claude Code agents: project picker, git worktrees, agent-state sidebar, session restore. Single Go binary on top of tmux.

Status: in daily use. It replaced the bash `dev` scripts with strict parity, then added notifications, Codex agent state, Claude resume after restore, session cost and the `dev agent` control CLI.

## Install

```bash
go install github.com/ppf/dev@latest
```

## Usage

Projects live in `~/.config/dev/projects.toml` (`[[project]]` entries with `name`, `path`, `description`).

```bash
dev                         # picker: ⏎ opens or attaches the project's boss (see Coordinator view), ctrl-o its plain session
dev <project>               # open the project's main checkout; attaches if the session exists
dev <project> <worktree>    # open <repo>/.claude/worktrees/<worktree>, creating it if missing
dev list                    # name<TAB>description<TAB>path
```

`dev <project> <worktree>` names the session `<project>-<worktree>` (`/` becomes `-`) and attaches when it already exists. A new worktree checks out an existing branch of that name as-is, otherwise branches from `origin/HEAD`, and copies the gitignored files listed in `.worktreeinclude`. Names may only contain `A-Za-z0-9._/-`.

Shell completion covers subcommands, project names and a project's worktrees:

```bash
dev completion zsh > "${fpath[1]}/_dev"   # zsh; see dev completion --help for bash, fish, powershell
```

If `dev` is a zsh alias rather than the binary on `PATH`, also `setopt completealiases`.

## Notifications

`dev state` (the Claude hook) sends a macOS notification through [terminal-notifier](https://github.com/julienXX/terminal-notifier) (`brew install terminal-notifier`) when Claude needs input or a decision (permission prompt, question) or fails; a finished turn doesn't notify. It skips a pane you're looking at: its window is current, not zoomed onto another pane, and a focused tmux client shows it. That needs `set -g focus-events on`; without it every pane counts as not in view. A click switches to the pane and raises the terminal app that started tmux.

```bash
tmux set -g @dev_notify off                                # turn off (or per session with -t)
tmux set -g @dev_notify_activate com.mitchellh.ghostty     # app a click raises
```

dev needs terminal-notifier 3.0 or later (native, built on UserNotifications). If notifications stop appearing, `terminal-notifier -diagnose` reports why (permission, alerts, alert style). macOS's Rosetta warning ("will not open in macOS 28") about terminal-notifier comes from an Intel copy another tool installed through RubyGems (`terminal-notifier` 2.0.0 gem), not from the Homebrew one dev runs.

Set Claude Code's `"preferredNotifChannel": "notifications_disabled"` (in `~/.claude/settings.json`) to avoid duplicate notifications. Hook errors go to stderr, which Claude Code hides for a successful hook; run `claude --debug` to see them.

A claude the pane's Claude runs itself (`claude -p` from its Bash tool) inherits the pane's `TMUX_PANE`. `dev state` records the hook's Claude process (`CLAUDE_PID`, `@agent_pid`); it and `dev question` ignore hooks from a claude running below it: they change no state and send no notification or boss note. A new Claude in the pane (after `/exit` or a crash) registers as before.

## Codex state

Codex CLI hooks give Codex panes the same sidebar state and notifications (needs approval). In `~/.codex/hooks.json`, call `dev state --codex <state>`:

| Event | State |
|---|---|
| `SessionStart` (matcher `startup\|resume\|clear`) | `idle` |
| `UserPromptSubmit`, `PostToolUse` | `working` |
| `PermissionRequest` | `waiting` |
| `Stop`, `Interrupt` | `idle` |
| `SessionEnd` | `clear` |

```json
{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "$HOME/go/bin/dev state --codex idle"}]}]}}
```

Codex runs hooks in its shared app-server daemon, whose `TMUX_PANE` is the pane that started it, so `--codex` picks the only pane running `codex` (or `codex-*`) whose directory is the hook's `cwd`. Nothing is recorded when no pane matches (Codex's background memory agent, a session started with `codex -C <other dir>`) or two or more Codex panes share a directory. A Codex session outside tmux (the desktop app) in a directory with a Codex pane counts as that pane's. Codex asks you to trust new hooks once (`/hooks`). With `approvals_reviewer = "guardian_subagent"` a reviewer, not you, answers approvals, so leave out `PermissionRequest`. Turn off Codex's own notifications (`[tui] notifications = false`) to avoid duplicates.

## Grok state

Grok CLI hooks give Grok panes the same sidebar state, notifications and boss notes. Grok runs hooks in the pane's own process (its default shell route; its `grok host-server` daemon route isn't supported), so plain `dev state` finds the pane through `TMUX_PANE`; it knows a Grok hook by `GROK_HOOK_EVENT` and then leaves out subagent events (`subagentType`) but their permission prompts and questions, and the extra `Stop` at session end, records no session (Grok panes aren't resumed) and never answers as a boss. In `~/.grok/hooks/dev.json`:

| Event | State |
|---|---|
| `SessionStart` | `idle` |
| `UserPromptSubmit`, `PostToolUse`, `PostToolUseFailure` | `working` |
| `Notification` (matcher `permission_prompt\|elicitation_dialog\|idle_prompt`) | `waiting` |
| `Stop`, `StopFailure`, `StopCancelled` | `idle` |
| `SessionEnd` | `clear` |

```json
{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "$HOME/go/bin/dev state idle", "timeout": 10}]}]}}
```

Grok also runs the hooks in `~/.claude/settings.json`, so turn that off (`[compat.claude] hooks = false` in `~/.grok/config.toml`) to keep Claude's hooks from recording twice, and turn off Grok's own notifications (`[ui.notifications] condition = "never"`) to avoid duplicates. A Grok pane is one running `grok` (or `grok-*`, its versioned binary). Grok can report an interrupted turn after the next prompt started, so `dev state` keeps the `promptId` of the pane's latest `UserPromptSubmit` and ignores a `Stop`, `StopFailure` or `StopCancelled` for another turn.

## Session restore

The `dev resurrect` [tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect) hooks bring back each session's `@project` and sidebar, and each agent's own Claude conversation: `dev state` records the hook's `session_id` and transcript on the pane, the save keeps them next to the snapshot, and the restore types `claude --resume <id> --remote-control=<session>` (a worker's without Remote Control, see [Remote Control names](#remote-control-names)) into each agent pane it brought back as a bare shell (100 ms apart; sessions that were still running are left alone). A pane restored into another directory, or whose transcript is gone, gets a fresh agent and a warning. Codex panes aren't resumed. A boss comes back as a boss (only a session the save recorded as one, by the `@dev_herd` mark and its panes' `@dev_boss_pane` roles; a boss made before dev recorded those comes back plain, as does one whose panes didn't come back as bare shells, with a warning and its coordinator resumed as a plain agent): marked again, its coordinator resumed into its conversation (fresh otherwise) with the coordinator prompt, its previews and sidebar back, so `dev boss` and the picker attach it.

```tmux
set -g @resurrect-hook-post-save-layout '$HOME/go/bin/dev resurrect save'
set -g @resurrect-hook-post-save-all '$HOME/go/bin/dev resurrect prune'
set -g @resurrect-hook-pre-restore-all '$HOME/go/bin/dev resurrect pre-restore'
set -g @resurrect-hook-post-restore-all '$HOME/go/bin/dev resurrect restore'
```

Keep claude out of `@resurrect-processes` and `@resurrect-default-processes` (and off `:all:`): resurrect would start its own claude in the same pane, so dev warns and resumes nothing.

A session you close with the sidebar's `x` or `×`, the boss previews' `x`, or `dev agent close` stays closed: once it's gone, dev saves a snapshot without it (`dev resurrect snapshot`; dev's saves take turns, each in a second of its own, as resurrect names snapshots by the second), so a restore after a reboot leaves it out. A save in the second of another (tmux-continuum's autosave or `prefix + C-s` right after one of dev's) rewrites that second's file, and resurrect would find it identical to `last` (itself) and delete it, leaving `last` dangling and a restore before the next save empty: the `dev resurrect save` hook above, which every save runs, unlinks such a `last` (after a dev snapshot in progress has linked it), so resurrect links it again. A save that waited there for dev's snapshot leaves `last` on it when it is the later one, so an autosave begun before a close can't bring the closed session back. No other config needed. Resurrect has no hook before it writes a snapshot, so two saves started at the same moment (both writing one file) or two saves outside dev ending together can still clash. The sidebar closes the last session too: dev turns tmux's `exit-empty` off until that save, then back on, so the server exits and the next `dev` restores nothing (with `exit-empty` already off, the server stays up empty). A session you only detach from (closing the terminal tab) stays in the snapshot; one that ends by itself (`exit` in its last pane) leaves it at the next save (tmux-continuum's autosave, every 15 minutes by default). A session whose Claude exits cleanly (`/exit`, Ctrl-D: status 0; dev starts it under `dev pty-wrap --close-on-exit`) closes like `×`, asking about its worktree on a client attached to it (with none attached, the worktree stays); a crash or a shutdown leaves it for the restore. Every close (the sidebar's `x`/`×`, the previews' `x`, `dev agent close`, such an exit) writes an archive record, `$XDG_STATE_HOME/dev/archive/<session>[.N]/session.json` (`.2`, `.3`, … for a reused name): project, path, the last Claude conversation and transcript (to resume by hand with `claude --resume`), the worktree and whether it was removed.

## Driving agents from an agent

`dev agent` lets one Claude session (a coordinator) drive the agents in other sessions. Targets are session names or pane ids (`%12`), never sidebar positions.

```bash
dev agent list [--json]                        # session, pane, agent, state, branch, path, cost
dev agent list --mine                          # only the workers this session started (its child sessions)
dev agent start <project> [<worktree>]         # open detached, return once its Claude registered
dev agent start <project> [<worktree>] --agent codex  # a new session runs codex; returns once codex runs
dev agent start <project> [<worktree>] --agent grok   # same with grok
dev agent prompt <target> --wait "Fix the test" # paste + Enter; --wait returns when the turn ends
dev agent prompt <target> --wait /clear        # reset its context; --wait returns once the new session started
dev agent wait <target> [--until idle|waiting|settled] [--timeout 90s]
dev agent read <target> [--lines 50]           # the pane's screen (plus scrollback, if any)
dev agent send-keys <target> esc|enter|ctrl+c|1|…
dev agent close <target> [--json]              # close a finished worker: session, worktree, branch
dev delivered <worktree> [--json]              # close's delivered checks only, for a desktop-app boss: exit 0 if delivered
dev agent skill > ~/.claude/skills/dev-agents/SKILL.md   # the coordinator skill
dev agent skill desktop-boss > ~/.claude/skills/desktop-boss/SKILL.md   # its desktop-app twin (start_session, SendMessage)
```

`start`, `list --mine` and `close` act for the session they run in (`TMUX_PANE`). A coordinator outside tmux (e.g. a Claude desktop-app session) passes `--boss <session>`, naming a `dev boss` session to act as: its workers are the same as the ones its coordinator starts (nested under it in the sidebar, previewed, no Remote Control, notes to its inbox), and `close` runs the same checks. Any other session is refused. Such a worker is the outside coordinator's, not the boss's own coordinator's: `start` marks it `@dev_started_outside` (`outside: true` in `list --json`), each note the boss's inbox gets about it ends in "started by a coordinator outside tmux: leave it to that coordinator", and `close` refuses it unless run with `--boss` outside tmux. Its new session takes the boss's window size (a desktop app's Bash tool has no terminal to size it from); without `--boss`, a caller with no tmux client or tty takes the widest attached client's.

Exit status: 0 ok, 1 error, 2 timeout, 3 stalled (the prompt was never picked up). State comes from the `dev state` hooks, so `wait` and `prompt --wait` refuse a Codex pane without them. `prompt` refuses an agent showing a permission prompt or question (`@agent_waiting_for`, the Notification type), and `prompt --wait` counts prompts through the `UserPromptSubmit` hook (`@agent_prompts`), so the hook must run this version of `dev`. `/clear` (and its aliases `/reset`, `/new`) starts no turn, so `prompt --wait /clear` waits instead for the `SessionStart` of the new conversation (a new `@agent_session`, which Codex and Grok panes don't record, so they refuse it). `agent` is a subcommand, so no project can be named `agent`.

Some changes run no Claude hook, so `dev agent` reads them off the screen:
- A prompt to a working Claude is queued: `prompt` prints `queued: …`, and `get`/`list --json` show `"queued": true` while the input box says `Press up to edit queued messages`. The `UserPromptSubmit` hook fires at queue time, so `@agent_prompts` can't tell when it's read.
- A one-line slash command Claude runs itself (`/effort`, `/model`) counts no prompt. `prompt` watches the screen for up to 3 s (an error if it isn't seen running): the dialog both open mid-conversation (`This conversation is cached …`) is confirmed with `1` once it shows; any other dialog is an error (read it, answer with `send-keys`). `--wait` returns once the input box is back, idle, without the command; a command that starts a turn (a skill's) waits like any prompt.
- Esc ending a turn: the working state is recorded idle once the screen shows `⎿ Interrupted` above an idle input box. `send-keys esc` to a working Claude waits up to 3 s for it, and `prompt`, `wait` (every second) and the boss inbox check for it too.
- Claude's agent panel under its status lines (`⏺ main`, a `◯` row per teammate or background agent) isn't taken for the end of its input box.

`close` ends a worker whose work is delivered: the caller's own worker only (its `@dev_started_by` names the calling session), idle or waiting only because it sat idle, in a `.claude/worktrees` worktree no other session has a pane in, with no changes to tracked files and its HEAD in origin's default branch (local `origin/HEAD`, after a fetch; checked with `git merge-base --is-ancestor`, as `git branch -d` refuses when the branch's own remote ref is stale). Otherwise HEAD must be the head commit of a PR/MR that is open, or merged into that branch (a squash or rebase merge leaves its commits out of it): looked up by commit in origin's repository (`gh api repos/<owner>/<repo>/commits/<HEAD>/pulls` for a github.com origin or one on a host `gh` is signed in to, else `glab api …/repository/commits/<HEAD>/merge_requests`), so on GitHub a PR from origin, a fork, to its upstream counts. A commit after the PR's head is refused. Without `gh`/`glab`, or when it fails, such a worker is refused with its error. Anything else is refused, naming the failed check, before it touches the worker. It copies the worktree's untracked, not ignored files (e.g. `RESULT.md`) to `$XDG_STATE_HOME/dev/results/<session>/` (`<session>.2`, … when that exists) and prints where; ignored files go with the worktree. It moves clients watching the worker to the caller's session, kills the worker's session, removes its worktree and force-deletes its branch. Claude Code's auto mode blocks a coordinator's raw `tmux kill-session` and `git worktree remove`; allow `Bash(dev agent close:*)` instead.

`dev delivered <worktree>` runs close's checks of the work alone (HEAD in the default branch or at an open or merged PR/MR, no changes to tracked files) on a `.claude/worktrees` worktree, with no tmux, touching nothing but the fetch of origin, e.g. for the desktop-boss skill before `archive_session`. Delivered: exit 0 and `delivered: <head> (PR #12 open|PR #12 merged|in origin/main)` (`MR #12 …` on GitLab); else exit 1 and the failed check. `--json` prints `{delivered, head, reason, pr}` either way (`pr` only when a PR/MR delivers it, `head` once read). `delivered` is a subcommand, so no project can be named `delivered`.

## Coordinator view

```bash
dev boss [<project>]    # open or attach the boss session: "boss", or "boss-<project>"
```

Bare `dev` (and the sidebar's picker) is a project picker for `dev boss <project>`: `⏎` opens or attaches the boss, `ctrl-o` the project's plain session instead (`dev <project>`, main checkout); a worktree is `dev <project> <worktree>`. Inside tmux it runs in a popup and switches your client to the chosen session.

One window: a coordinator Claude on the left, started in the project's main checkout (default `$HOME`) and told to use the `dev-agents` skill, so its prompt is the one input field ("start workers for X and Y", "tell fix-login to add tests"). It's told to delegate, not do: it only starts, prompts and reads workers, relays their results, asks you and runs single-command lookups to route work; anything longer (multi-step research, browser testing, drafting, file edits, multi-call verification) goes to a worker: a fresh one (new session and worktree) for each new feature or piece of work, an existing one of its own only for changes to that worker's current work (fixes, review findings, changes to the same feature or PR), never for unrelated or merely related work, and when unsure it delegates. On the right, 40% of the window (at least 60 columns, at most half; it fits itself when it starts), a board of this boss's workers (the agent sessions its coordinator started, see `@dev_started_by` below; other bosses' workers and your own sessions don't show): a card per worker in sections, each headed with its count, empty ones hidden: **NEEDS YOU** (red: a permission prompt or question waits on you, or its turn ended on a question, an `asks` note), **WORKING** (gold), **WAITING** (green: its last reply's newest turn-end marker is `WAITING (…)`), **IDLE** (muted). A card, in a box (faint; the selected one in its state's colour, its name row on the selection band; a red or green bar left of a Needs you or Waiting card), shows ▾/▸, the state dot, the name and at the right its time in its state (`@agent_since`: `now`, `12m`); under the name its title where it differs, what it says (the dialog it waits on, `allow Bash: rm -rf build`, or its question; what its tool call does, a Bash, Agent or Task call's `description`, else the tool; what it waits on; how its last turn ended, `finished`), the branch (cut at its start) with its cost and context use (`@agent_context`, from `dev statusline`) at the right, its PR (○ open, ◌ draft, ◆ merged, ✕ closed) and an open or draft one's checks (`gh pr view --json statusCheckRollup`, read in the background at most once a minute per PR while the board is on screen, every 10 s while it has none yet for 2 minutes after it shows up, and at once after the worker runs a gh command that changes the PR): ✓ passed, ✗ failed or cancelled, ● running, with the checks that make it (the failed ones, else the running or stale ones, else all; each once), or after a failed read why, in place of the names, the last two lines of its newest reply (markdown backticks and bold stripped, a marker line left out), a ⚠ row per health note (`failed`, `stuck`, `high context`) and for a transcript it couldn't read or whose rows didn't parse, then the last 5 lines of its agent pane, and its buttons: **Open** (switches to the worker, as `g`/`⏎`; it leads on a Needs you or Waiting card without a dialog), **Message…** (opens its reply box, as `m`), its PR (opens it, as `o`) and **×** at the right (asks to close it, as `x`, closing a reply box first). A Claude worker's reply and tool call come from its transcript (`@agent_transcript`, which `dev state` records; its last 1 MB, read again only once it changed). An idle card is folded to one row (name, how its turn ended, PR and its checks' icon, `?` after it when the last read failed, ⚠ for a warning, time; the selected one on the selection band) unless it has a warning; a Needs you card is never folded. The board scrolls to keep the selected card whole. It refreshes once a second, only while its window is on screen, capturing at most 12 panes: the selected card's (open or with a dialog), the ones with a dialog open, then the other open cards', nearest the selected one first.

A card's screen leaves out its agent's own input box and status lines (Claude Code's, Codex's `›` prompt and footer, Grok's rounded box, shortcut lines and opt-in banner; they take nothing typed in the previews), and a Codex or Grok card's collapses the long blank gaps those leave on screen; a waiting worker's screen stops above its dialog's question, which its card shows with options to answer (below). A click on a card (off its buttons and options) selects it, closing its reply box (its draft kept); the keys in the previews act on the selected card: `j`/`k` select, `g` (or `⏎`) switches to that worker, `m` (or `i`, or its **Message…**) opens the reply box for it (below), `1`–`9` answer its dialog (through its card's options, below, when they show), `o` opens its PR, `v` shows half the pane of its screen (again: 5 lines; per card while its session lasts, unfolding a folded one), `f` (or a click on its ▸/▾) folds or unfolds it while its session lasts (not a Needs you card), `x` closes the worker like the sidebar's `×` (`dev agent start` from it marks the new session with `@dev_started_by`, the boss's name, which a session restore keeps: a boss reopened under that name owns its workers again, and the sidebar's `r` won't rename a boss), after a `y`. Closing the coordinator (`/exit`) or the previews cleanly closes the boss session for good (a crash or a shutdown leaves it for the restore), like the sidebar's `×` (archive record included), moving the first attached client to the sidebar's next session (others go to the most recently used one; a client detaches only with the last session); it has a sidebar like any session (`prefix+S` hides it). Session restore brings a boss back (see [Session restore](#session-restore)); `dev boss` refuses any other session of that name, also a boss that came back plain (kill it first). `boss` (and the old name `herd`) can't be a project name. In the sidebar, each worker a coordinator started sits indented (`└`) under the boss session, in its group; `dev goto` numbers follow the same order.

A card's `finished`, `failed`, `asks` or other note comes from its agent pane's `@dev_boss_note` (`<kind>: <text>`), its latest boss inbox note, cleared by its next prompt; `finished`, `failed`, `needs a decision` and `asks` are recorded also with the inbox off, `stuck`, `high context` and repeated `asks` only with it on.

### Reply box

`m` (or `i`, or a card's **Message…**) binds the reply box at the bottom to the selected worker: a rule in the worker's state colour (gold working, red waiting, green idle) naming it with its agent, state and branch, over the draft behind a bar of the same colour. Its card is the selected one, framed in that colour too. The draft grows to 8 rows (fewer in a short pane): `⏎` sends it as a prompt (as `dev agent prompt`, so a worker waiting on a decision refuses it), `alt+⏎` or `ctrl+j` adds a line (so does `⇧⏎` in a terminal sending it as `alt+⏎`, as Ghostty's `keybind = shift+enter=text:\x1b\r` does; tmux passes a bare `⇧⏎` as `⏎`), a bracketed paste keeps its lines, `←`/`→`/`home`/`end`/`ctrl+a`/`ctrl+e` move, `ctrl+u` and `ctrl+w` delete back to the line start or a word, `↑` on an empty draft recalls the last 50 replies (`↓` back), `esc` closes. A draft typed before a dialog comes stays a draft.

When the worker waits on a numbered dialog (Claude's questions and permission prompts, Codex's and Grok's menus), its card shows it in place of its status: for a permission the tool with its command, file or URL, the question tabs, the question, its options side by side (wrapping when narrow; the highlighted one, the box's or else the dialog's own cursor, bracketed in the worker's colour, `[1 Yes]`; a multi-select's ☐/☑ and a `Submit ⏎` after them), then a question's described options as `label: description` (a permission's option shows its whole text); its screen stops above the dialog's tabs or question, keeping what it shows above them (a command, a file's contents, a folder, the answers to review). The board scrolls to keep the box's highlighted option in view. A click on an option opens the card's box and picks it (toggles a multi-select's), on `Submit ⏎` goes on, as the keys do; while an answer is out the options dim and a click says so; over a typed reply it says to send or clear it first. With the box open on the card, the box is the picker's keys: `←`/`→` (or `↑`/`↓`) move, `⏎` or `1`–`9` pick, `space` (or a digit) toggles a multi-select option and `⏎` goes on to the next question or the review, the free-text option ("Type something") takes typed text in the box (`esc` back to the options), `esc` closes; one answer goes out at a time. Each answer first captures the worker's screen and sends nothing if another dialog shows than the one drawn (another question, command, file or checked box); then it presses the keys the dialog takes, 200 ms apart (a digit; Claude's free text: its number, the text, `⏎`; a multi-select's free text: arrows to it, the text; its Submit: arrows to the Submit row, `⏎`), and polls until the dialog changes, reporting a failure after 2 s (and a picker still showing the answered dialog three refreshes later comes back, saying so). The question and options come from the screen; the tabs, full option descriptions and a permission's tool with its command, file or URL come from the worker's `@agent_question` when it matches the screen, which the `dev question` hook records:

```json
{"hooks": {
  "PreToolUse": [{"matcher": "AskUserQuestion", "hooks": [{"type": "command", "command": "$HOME/go/bin/dev question", "timeout": 5}]}],
  "PermissionRequest": [{"hooks": [{"type": "command", "command": "$HOME/go/bin/dev question", "timeout": 5}]}]
}}
```

Claude Code sends `PermissionRequest` (with the tool's input) for its questions and permission prompts, also for a tool auto mode then approves, so the hook only records what a dialog would ask and the picker shows it only while the screen shows that dialog; the `dev state` of the turn's end (or the next turn's start) clears it. Answering `No` or `esc` runs no Claude hook, so the worker stays recorded as waiting. Its screen settles that: once a permission prompt or question has been recorded for 2 s, Claude's own input box back at the bottom (no dialog in its place) has a reply, `dev agent prompt`, `dev agent close`, the previews, the inbox's stuck check and its delivery to the boss record it idle (working while it shows `esc to interrupt`) and clear its question; a dialog still on screen keeps refusing a reply. Claude records a dialog only seconds after drawing it (not while you type in it), so a prompt to a working Claude whose screen shows no input box is refused too. `dev question` prints nothing (Claude reads a `PermissionRequest` hook's stdout as its decision). Without the hook (Codex, Grok, or an older `dev`), the picker shows the screen's options alone.

### Boss inbox

The coordinator hears of its workers on its own: a worker's `dev state` hook queues a one-line note for the boss that started it when the worker finishes (with its last message), fails, or needs a decision (permission prompt or question, saying what it asks: `Which color? (Red / Blue)` or `allow Bash: <command>` from the `dev question` record while the screen shows that dialog, else Claude's own message; a Codex worker's `PermissionRequest`, with the command's description), or, instead of `finished`, ends its turn on a question (`asks: <the question>`, from the last message's last few lines: a line ending in `?`, not a heading, followed only by list items, its options). The previews (every 15 s) add:

- `stuck: no progress for 15m`: a working worker that ran no hook (a finished tool call, its own or a subagent's: `dev state` stamps `@agent_active`, also for a subagent's events) and whose screen didn't change (spinners, timers, clocks, token counts, costs, and Claude's input box and status lines aside) for 15 minutes, however long its turn; a single command running that long without output counts too. One stretch without progress gives one note. The minutes are those when it was queued; the card's note keeps them current. A Claude worker whose turn ends only to wait on background subagents or shells stays working, with no note or notification, until the turn after them ends; one whose input box is back while nothing moves gets `…, waiting on its background tasks` (a hung shell, or a dev server left running).
- `stuck: waiting on a decision for 4m, unanswered`: a permission prompt or question unanswered over 3 minutes.
- `stuck: idle for 15m, PR #12 has merge conflicts` (and/or `no CI checks`): a worker idle about 15 minutes whose open PR (as the previews last looked it up in its directory, `@dev_pr`) `gh pr view` finds conflicting, or without any check in a repo with GitHub Actions workflows (`.github/workflows`). Checked once per idle stretch, after the round's other notes are out (gh may be slow), and only noted while the worker still rests in that stretch; while GitHub is still working out mergeability, again next round, and no note until it knows. A failed `gh` shows on the previews' bottom row and isn't tried again until the next idle stretch.
- `asks: <the question> (unanswered for 30m)`: a question its worker still waits on, resting, 30 minutes after it asked, then 1 h, 2 h, … after (one repeat for several waits missed while it worked or the previews were closed); the worker's next prompt answers it.
- `high context: 85% used`: a Claude worker whose context window is 80% full or more, from its status line (`dev statusline` records `context_window.used_percentage` in `@agent_context`). Once, then again only after its use drops below (`/clear`, compaction) and climbs back.

Notes sit in `$XDG_STATE_HOME/dev/inbox/` (one locked file per boss), a worker's repeat note is dropped, and at most 20 wait. They reach the coordinator in one batch: when it stops (its Stop hook blocks the stop with the notes, so it carries on), when you send it a prompt (added as context), or as soon as they're queued over Claude Code's [cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging) socket. Claude shows such a batch as a message from another Claude session (dev adds `(from dev's boss inbox: act on these like any inbox batch; dev reads no reply)`): an idle coordinator starts a turn with it, a working one reads it between tool calls or after its turn, one with a dialog open after the dialog, and a draft in its input box stays put. The coordinator's `dev state` hook records the socket (`CLAUDE_CODE_MESSAGING_SOCKET`, `@agent_socket`) and its permission mode (`@agent_mode`, from every hook that carries one: not SessionStart, so a new session's mode is known from its first prompt). The socket is used only in `default`, `auto` and `acceptEdits` modes; bypass mode (and plan mode where bypass is available) would hold the message behind a dialog that drops it after 5 minutes. A mode switched (shift+tab) while the coordinator sits idle runs no hook, so it's known from its next prompt; a batch sent meanwhile is caught by the timeout below. Claude sends no receipt, so one batch is in flight at a time, until the coordinator's UserPromptSubmit hook shows its text; a batch still unshown 5 minutes into the coordinator's rest goes back to the inbox, reported on the previews' bottom row, and the next batch is pasted (Claude drops a repeated message). Otherwise (bypass or an unknown mode, no socket, a failed send, a Codex or Grok coordinator) the batch is pasted as a prompt while the coordinator is idle and its input box is empty, at most every 10 s. It never pastes into a box with a draft. A failed note carries the error and what Claude showed (`failed: rate_limit: You've hit your session limit · resets 7:10pm (Europe/Warsaw)`). For a rate limit with a reset time (from the hook's message, else the worker's screen), the note adds `dev resumes it at 19:11`: the boss's previews send the worker `Continue where you left off: …` a minute after the reset, if it's still idle with an empty input box and nobody prompted it since (`@dev_resume_at`, `@dev_resume_prompts`), and queue a `resumed` note. A worker prompted by hand meanwhile isn't resumed. After 3 resumes in a row without a finished turn (`@dev_resumes`) it isn't resumed again; a limit without a reset time (a spend limit) never is. The coordinator is told to act on clear, safe next steps itself, ask you about decisions and anything destructive or external, close its finished workers with `dev agent close` (unless you asked to keep one or a follow-up is likely soon), and roll up finished batches. In a roll-up, or idle with nothing pending, it may propose one or two improvements backed by what it saw (worker friction, flaky tests, recurring review findings), each with why and a rough size, and is told to start none without your yes.

```bash
tmux set -g @dev_boss_inbox off      # turn off (or per boss session with -t)
```

## Desktop worker board

```bash
dev desktop board [--json] [--boss <local_id>]   # a desktop app boss session's workers
```

Lists the unarchived sessions a Claude desktop app session started (default: `$CLAUDE_CODE_HOST_SESSION_ID`, the session it runs in): state (busy, blocked, idle, stopped), title, branch, PRs and context use; `--json` adds the app's verdict on the last turn and what it needs, the tool in flight and the last lines of the newest reply. It joins the app's session records (`~/Library/Application Support/Claude/claude-code-sessions`), `~/.claude/sessions` and the transcripts; the context window comes from Claude Code's model catalog (`~/.claude/cache/model-catalog`). These are private app formats: a file that fails to parse is reported on stderr. So is a dev-board mod (`CLAUDE_CODE_PLUGIN_DIRS`) changed on disk since the boss's Claude started: it loaded the mod then and runs the older copy (not checked with `CLAUDE_CODE_PLUGIN_DIR_WATCH=1`, which reloads it). The [dev-board mod](mods/dev-board/README.md) shows it live in a pane of the boss session.

Each worker in `--json` carries `notes`, the health notes `dev boss`'s inbox has for tmux workers, each `{key, kind, text}` with a key that stays the same while its condition holds: `stuck` (busy with no progress for 15 min: no hook, transcript write or live status change), `needs a decision` (a dialog shown, by its `Notification`, or an `AskUserQuestion`, unanswered for 3 min while busy), `high context` (80% or more, once per conversation), `rate-limited` (its reset time, or `no reset time read`) and `resume due` (reset passed a minute ago, not busy). A stopped worker has none. The hook-based ones come from the Claude hooks above: in a desktop app session (`CLAUDE_CODE_ENTRYPOINT=claude-desktop`, no `TMUX_PANE`) `dev state` and `dev question` write the session's state, its dialog's question and a rate limit's reset to `desktop/<session id>.json` in dev's state directory (`$XDG_STATE_HOME/dev`, else `~/.local/state/dev`) instead of a pane's options; `SessionEnd` removes it, and a session's start removes those unwritten for a week. A running worker without one is reported on stderr: its hooks run a `dev` without this.

It also carries `status`, finer than `state`, the first that holds: `stopped` (`statusText` saying so when a dialog was open as the app quit), `needs you` (a dialog waits on the person while busy, from that hook file, or, not busy, one was open when the app quit: `interruptedAwaitingPerson`), `rate-limited`, `stuck`, `working`, `waiting` (a line `WAITING (…)` among its last few, a short sentence after it allowed: CI or a background job will wake it), `idle` on such a line `IDLE` (the newer of the two wins), `blocked`, `review ready`, else `idle`; `statusText` says what (the question, the tool, what it waits on, the reset time). `since` is how long it has been busy or idle (not running: since the app's last activity), e.g. `12m`; `ask` the question it waits on (a busy worker's AskUserQuestion, or, its turn over, the `ask` its last `wrap_up` ended on with its reply labels as options, `reply: true`, until a prompt comes); and `plan` its task list, `{content, status}` per step, replayed from the transcript's `update_plan` calls (`items` sets it, `advance`, `done` and `start` move it, `wrap_up` checks it off; a call whose result is an error changes nothing).

## Session cost

The sidebar shows each Claude session's estimated cost (`Claude · idle · $1.24 · title`), and `dev agent list --json` reports it as `cost_usd`. The number is Claude Code's own `cost.total_cost_usd`, the list-price estimate `/cost` shows. It covers every API call of the conversation, and resets on `/clear`. It isn't your bill on a subscription plan. `dev statusline` records it from the status line input, so run your status line through it:

```json
"statusLine": { "type": "command", "command": "$HOME/go/bin/dev statusline -- $HOME/.claude/statusline.sh" }
```

Without a command, `dev statusline` prints the cost as the status line. Codex panes show no cost.

On a subscription plan, hide it from the sidebar, boss previews and `dev agent` text output (`--json` keeps `cost_usd`). Set on one session, it hides that session's cost wherever it shows. `dev statusline` without a command still prints it:

```bash
tmux set -g @dev_cost off           # or per session with -t
```

## Images in tmux

Claude Code draws images (pasted image thumbnails) with kitty graphics, but writes them to tmux without the passthrough wrapping tmux needs: tmux drops them and sets them as the pane title (`Ga=d,d=I,i=2,q=2`). So dev starts claude (new sessions, the boss, `dev resurrect` resumes) under `dev pty-wrap`, which runs it on its own pty and wraps each graphics sequence for passthrough; everything else passes through unchanged. Images show in a terminal with kitty graphics (Ghostty, kitty) given:

```bash
tmux set -g allow-passthrough on
```

and `"CLAUDE_CODE_FORCE_TERMINAL_IMAGES": "1"` in the `env` of Claude's `settings.json` (inside tmux Claude Code turns images off otherwise). A claude you start by hand runs unwrapped; wrap it with `dev pty-wrap -- claude`. In the wrapped pane, tmux's `pane_current_command` is `dev`.

## Remote Control names

dev starts claude (new sessions, the boss, `dev resurrect` resumes) with `--remote-control=<session>`, so in Remote Control each shows as its tmux session (`dev-flaky-tests`, `boss-dev`) rather than a title made from its first message; a resume keeps the name. This turns Remote Control on for those sessions even without `remoteControlAtStartup`. Workers (sessions `dev agent start` opens from a tmux pane, marked `@dev_started_by`, and their `dev resurrect` resumes) start without it, so a busy boss doesn't flood your phone with Remote Control notifications: with `--settings '{"remoteControlAtStartup":false}'`, which outranks that setting in your user settings; `/remote-control` still turns it on in one. A worker started by a plain session (not a boss) loses its `@dev_started_by` across `dev resurrect`, so it comes back with Remote Control. dev doesn't use `--name`: it would pin the pane title to the session name, and the sidebar and boss previews would lose the task title Claude sets there. Codex and Grok panes start unnamed.

## Development

```bash
go test ./...                                              # unit tests, never start tmux
go test -tags integration -count=1 ./internal/integration/ # isolated tmux servers; needs tmux 3.8, tmux plugins + a ppf/terminal clone (CI skips its parity tests)
```

## Sidebar width

Every sidebar shows the same width, kept in the global tmux option `@sidebar_width` (default 40). Drag any sidebar's border (or `resize-pane` it) and every other sidebar follows; new sidebars open at that width. When tmux rescales panes because a window resizes (another client size, a terminal resize), each sidebar takes the stored width back instead of drifting. Compact mode's zoom leaves it alone.

## Sidebar PR links

When the [GitHub CLI](https://cli.github.com) (`gh`) is installed and signed in, the sidebar looks up each agent session's PR (`gh pr view` in the agent pane's directory, so fork PRs count; in the background, only while the sidebar's session is on screen, cached for a minute per directory and branch in the agent pane's `@dev_pr` option, which every sidebar and the boss previews share; the `dev state` `PostToolUse` hook bumps the pane's `@dev_pr_gen` after a `gh pr create`, `merge`, `close`, `reopen` or `ready`, so the tag shows as soon as gh answers) and leads the session's second line with it, e.g. `#12 open`. State is one of `open`, `draft`, `merged`, `closed`. No `gh`, no remote, or no PR shows nothing.

`o` on the selected session (else the current one), or a click on its tag, opens the PR in the browser (`gh pr view --web`); when the lookup failed, `o` shows why.

## License

Apache-2.0
