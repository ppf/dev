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
dev                         # picker: project, then main repo or a worktree
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

`dev state` (the Claude hook) sends a macOS notification through [terminal-notifier](https://github.com/julienXX/terminal-notifier) (`brew install terminal-notifier`) when Claude needs input, finishes or fails. It skips a pane you're looking at: its window is current, not zoomed onto another pane, and a focused tmux client shows it. That needs `set -g focus-events on`; without it every pane counts as not in view. A click switches to the pane and raises the terminal app that started tmux.

```bash
tmux set -g @dev_notify off                                # turn off (or per session with -t)
tmux set -g @dev_notify_activate com.mitchellh.ghostty     # app a click raises
```

dev needs terminal-notifier 3.0 or later (native, built on UserNotifications). If notifications stop appearing, `terminal-notifier -diagnose` reports why (permission, alerts, alert style). macOS's Rosetta warning ("will not open in macOS 28") about terminal-notifier comes from an Intel copy another tool installed through RubyGems (`terminal-notifier` 2.0.0 gem), not from the Homebrew one dev runs.

Set Claude Code's `"preferredNotifChannel": "notifications_disabled"` (in `~/.claude/settings.json`) to avoid duplicate notifications. Hook errors go to stderr, which Claude Code hides for a successful hook; run `claude --debug` to see them.

## Codex state

Codex CLI hooks give Codex panes the same sidebar state and notifications (needs approval, done). In `~/.codex/hooks.json`, call `dev state --codex <state>`:

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

## Session restore

The `dev resurrect` [tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect) hooks bring back each session's `@project` and sidebar, and each agent's own Claude conversation: `dev state` records the hook's `session_id` and transcript on the pane, the save keeps them next to the snapshot, and the restore types `claude --resume <id>` into each agent pane it brought back as a bare shell (100 ms apart; sessions that were still running are left alone). A pane restored into another directory, or whose transcript is gone, gets a fresh `claude` and a warning. Codex panes aren't resumed.

```tmux
set -g @resurrect-hook-post-save-layout '$HOME/go/bin/dev resurrect save'
set -g @resurrect-hook-post-save-all '$HOME/go/bin/dev resurrect prune'
set -g @resurrect-hook-pre-restore-all '$HOME/go/bin/dev resurrect pre-restore'
set -g @resurrect-hook-post-restore-all '$HOME/go/bin/dev resurrect restore'
```

Keep claude out of `@resurrect-processes` and `@resurrect-default-processes` (and off `:all:`): resurrect would start its own claude in the same pane, so dev warns and resumes nothing.

## Driving agents from an agent

`dev agent` lets one Claude session (a coordinator) drive the agents in other sessions. Targets are session names or pane ids (`%12`), never sidebar positions.

```bash
dev agent list [--json]                        # session, pane, agent, state, branch, path, cost
dev agent list --mine                          # only the workers this session started (its child sessions)
dev agent start <project> [<worktree>]         # open detached, return once its Claude registered
dev agent prompt <target> --wait "Fix the test" # paste + Enter; --wait returns when the turn ends
dev agent wait <target> [--until idle|waiting|settled] [--timeout 90s]
dev agent read <target> [--lines 50]           # the pane's screen (plus scrollback, if any)
dev agent send-keys <target> esc|enter|ctrl+c|1|…
dev agent skill > ~/.claude/skills/dev-agents/SKILL.md   # the coordinator skill
```

Exit status: 0 ok, 1 error, 2 timeout, 3 stalled (the prompt was never picked up). State comes from the `dev state` hooks, so `wait` and `prompt --wait` refuse a Codex pane without them. `prompt` refuses an agent showing a permission prompt or question (`@agent_waiting_for`, the Notification type), and `prompt --wait` counts prompts through the `UserPromptSubmit` hook (`@agent_prompts`), so the hook must run this version of `dev`. `agent` is a subcommand, so no project can be named `agent`.

## Coordinator view

```bash
dev boss [<project>]    # open or attach the boss session: "boss", or "boss-<project>"
```

One window: a coordinator Claude on the left, started in the project's main checkout (default `$HOME`) and told to use the `dev-agents` skill, so its prompt is the one input field ("start workers for X and Y", "tell fix-login to add tests"). On the right, live previews of every other agent session: a tile per session with the sidebar's header (state dot, agent, state, cost, PR tag, branch; red while waiting) over the last lines of its agent pane. The grid reflows with the count and pane size, and pages past 12 tiles. It refreshes once a second, only while its window is on screen.

Keys in the previews: `j`/`k` select, `⏎` (or a click on the selected tile) switches to that worker, `o` opens its PR, `x` kills a worker this boss's coordinator started (`dev agent start` from it marks the new session with `@dev_started_by`), after a `y`. Closing either pane ends the boss session, and it has no sidebar. Session restore doesn't bring a boss back: a restored `boss` session is a plain one, which `dev boss` refuses, like any session of that name it didn't create (kill it first). `boss` (and the old name `herd`) can't be a project name. In the sidebar, each worker a coordinator started sits indented (`└`) under the boss session, in its group; `dev goto` numbers follow the same order.

## Session cost

The sidebar shows each Claude session's estimated cost (`Claude · idle · $1.24 · title`), and `dev agent list --json` reports it as `cost_usd`. The number is Claude Code's own `cost.total_cost_usd`, the list-price estimate `/cost` shows. It covers every API call of the conversation, and resets on `/clear`. It isn't your bill on a subscription plan. `dev statusline` records it from the status line input, so run your status line through it:

```json
"statusLine": { "type": "command", "command": "$HOME/go/bin/dev statusline -- $HOME/.claude/statusline.sh" }
```

Without a command, `dev statusline` prints the cost as the status line. Codex panes show no cost.

## Development

```bash
go test ./...                                              # unit tests, never start tmux
go test -tags integration -count=1 ./internal/integration/ # isolated tmux servers; needs tmux 3.8, tmux plugins + a ppf/terminal clone (CI skips its parity tests)
```

## Sidebar PR links

When the [GitHub CLI](https://cli.github.com) (`gh`) is installed and signed in, the sidebar looks up each agent session's PR (`gh pr view` in the agent pane's directory, so fork PRs count; in the background, only while the sidebar's session is on screen, cached for a minute per directory and branch) and leads the session's second line with it, e.g. `#12 open`. State is one of `open`, `draft`, `merged`, `closed`. No `gh`, no remote, or no PR shows nothing.

`o` on the selected session (else the current one), or a click on its tag, opens the PR in the browser (`gh pr view --web`); when the lookup failed, `o` shows why.

## License

Apache-2.0
