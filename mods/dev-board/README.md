# dev-board

A Claude Code mod for a boss session in the Claude desktop app: a live board of the sessions it started (its workers), from `dev desktop board --json`.

- `/board` opens the **Workers** pane (docked in the right side panel): one tile per worker with its state (● busy, blocked, idle, stopped), title, branch, PRs with their state (click to open), the app's verdict on its last turn and what it needs from you, the tool in flight, its context use and the last lines of its newest reply.
- The band above the prompt sums them up, e.g. `3 workers · 1 busy · 1 blocked · PR #78 open`; hidden when there are none.

Every 2 s it runs `dev desktop board --json --boss $CLAUDE_CODE_HOST_SESSION_ID`. A failure (no `dev`, an old `dev` without the command, no app record for the session) shows its error in the pane; files `dev` could not read show as warnings above the tiles. Outside a desktop app session (no `CLAUDE_CODE_HOST_SESSION_ID`) it runs nothing.

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
