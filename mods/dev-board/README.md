# dev-board

A Claude Code mod for a boss session in the Claude desktop app: a live board of the sessions it started (its workers), from `dev desktop board --json`.

- `/board` opens the **Workers** pane (the desktop app docks it in the right side panel): one tile per worker with its state (● busy, blocked, idle, stopped), title and task list progress (`3/7`), its status and what it says (`needs you · allow Bash: …`, `working · Bash`, `waiting 4m · Auto-fix: PR #80`, `rate-limited · resumes 18:05`, `stuck · no progress for 17m`, `blocked`, `review ready`, `idle`), how long (`busy 12m`, `idle 3m ago`), branch, context use, PRs with their state (click to open) and an open PR's CI checks (✅ ❌ ⏳, from `gh pr checks`), the app's verdict on its last turn and the last lines of its newest reply. Tiles that need you come first.
- Buttons per tile:
  - **Tasks** expands the task list (✅ done, 🔄 current, ◌ pending).
  - **Open** opens the worker's session in the app (`claude://code/continue?session=<id>`).
  - **Message…** opens a field; Enter asks the boss to relay its text: `dev-board: message <name> (<id>): <text>`.
  - **Nudge** asks the boss to relay `Continue where you left off`: `dev-board: nudge <name> (<id>): Continue where you left off`.
  - **Copy id** copies the worker's `local_…` id.
  - **Archive**, shown once `dev delivered <worktree>` exits 0 for an idle worker with a PR, asks the boss to archive it: `dev-board: archive <name> (<id>): dev delivered exited 0`.

  The prompts wait for the boss to go idle; a toast says each was queued, or why not. A worker's permission prompt can't be answered here: answer it in its session (**Open**).
- Health notes (stuck, a decision unanswered, high context, rate-limited, resume due) show on their tile. A note the boss hasn't had yet goes to it as one prompt per poll for all new ones, e.g. `dev-board: fix-login stuck: no progress for 17m; docs-api resume due: rate limit reset 18:05: resume it`, which starts a boss turn once it is idle. The keys sent are kept in the session's state, so a reload of the mod sends none again; a prompt the app refuses shows as a toast and isn't retried.
- The band above the prompt sums them up, e.g. `1 needs you · 3 workers · 1 busy · 1 blocked · PR #78 open` (red while one needs you); hidden when there are none (or while a survey shows).

Every 2 s it runs `dev desktop board --json --boss $CLAUDE_CODE_HOST_SESSION_ID`; `gh pr checks` per open PR and `dev delivered` per idle worker with a PR run at most once a minute each. Without `gh` on the app's `PATH` the PR row says why. A failure (no `dev`, an old `dev` without the command, no app record for the session) shows its error in the pane; files `dev` could not read show as warnings above the tiles. Outside a desktop app session (no `CLAUDE_CODE_HOST_SESSION_ID`) it runs no `dev`; the pane says why.

## Load it

1. A `dev` build with `dev desktop board` on the app's `PATH`. The desktop app's `PATH` may lack `~/go/bin`: if the pane says `dev` can't start, set `DEV_BIN` at the top of `hooks/register.tsx` to its absolute path.
2. Name this folder in `CLAUDE_CODE_PLUGIN_DIRS` in the `env` block of `~/.claude/settings.json` (absolute paths, `:`-separated; never a project's settings):

   ```json
   { "env": { "CLAUDE_CODE_PLUGIN_DIRS": "/path/to/dev/mods/dev-board" } }
   ```

3. Start a new desktop session (or restart the app) and run `/board` in the boss session.

## Develop

```bash
claude plugin validate mods/dev-board
claude plugin test mods/dev-board
```

The engine writes the API types into `.claude-plugin/types/` (ignored by git) once it loaded the mod; `tsc -p mods/dev-board` type-checks it then.
