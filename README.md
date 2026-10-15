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

## Development

```bash
go test ./...                                              # unit tests, never start tmux
go test -tags integration -count=1 ./internal/integration/ # isolated tmux servers; local only, needs tmux 3.8 + exported ~/.tmux
```

## License

Apache-2.0
