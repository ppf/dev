# dev

tmux workspace launcher for Claude Code agents: project picker, git worktrees, agent-state sidebar, session restore. Single Go binary on top of tmux.

Status: not built yet. It replaces the bash `dev` scripts in [ppf/terminal](https://github.com/ppf/terminal) (`tmux/sessions/`), phase by phase with strict parity. See [docs/plan.md](docs/plan.md).

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

Set Claude Code's `"preferredNotifChannel": "notifications_disabled"` (in `~/.claude/settings.json`) to avoid duplicate notifications. Hook errors go to stderr, which Claude Code hides for a successful hook; run `claude --debug` to see them.

## Session restore

The `dev resurrect` [tmux-resurrect](https://github.com/tmux-plugins/tmux-resurrect) hooks bring back each session's `@project` and sidebar, and each agent's own Claude conversation: `dev state` records the hook's `session_id` and transcript on the pane, the save keeps them next to the snapshot, and the restore types `claude --resume <id>` into each agent pane it brought back as a bare shell (100 ms apart; sessions that were still running are left alone). A pane restored into another directory, or whose transcript is gone, gets a fresh `claude` and a warning.

```tmux
set -g @resurrect-hook-post-save-layout '$HOME/go/bin/dev resurrect save'
set -g @resurrect-hook-post-save-all '$HOME/go/bin/dev resurrect prune'
set -g @resurrect-hook-pre-restore-all '$HOME/go/bin/dev resurrect pre-restore'
set -g @resurrect-hook-post-restore-all '$HOME/go/bin/dev resurrect restore'
```

Keep claude out of `@resurrect-processes` and `@resurrect-default-processes` (and off `:all:`): resurrect would start its own claude in the same pane, so dev warns and resumes nothing.

## Development

```bash
go test ./...                                              # unit tests, never start tmux
go test -tags integration -count=1 ./internal/integration/ # isolated tmux servers; local only, needs tmux 3.8 + exported ~/.tmux
```

## Sidebar PR links

When the [GitHub CLI](https://cli.github.com) (`gh`) is installed and signed in, the sidebar looks up each agent session's PR (`gh pr view` in the agent pane's directory, so fork PRs count; in the background, only while the sidebar's session is on screen, cached for a minute per directory and branch) and leads the session's second line with it, e.g. `#12 open`. State is one of `open`, `draft`, `merged`, `closed`. No `gh`, no remote, or no PR shows nothing.

`o` on the selected session (else the current one), or a click on its tag, opens the PR in the browser (`gh pr view --web`); when the lookup failed, `o` shows why.

## License

Apache-2.0
