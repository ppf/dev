# dev-board

A Claude Code mod for a boss session in the Claude desktop app: a live board of the sessions it started (its workers), from `dev desktop board --json`.

- `/board` opens the **Workers** pane (the desktop app docks it in the right side panel), its tiles in sections by status, each with its count beside it, empty ones hidden: **Needs you** (red), **Working** (working, stuck, rate-limited; accent), **Waiting** (waiting, blocked, review ready; amber), **Idle** (idle, stopped; muted). A section's color marks its header, its open tiles' left rule and their status; colors are the app theme's, so light and dark both work.
- The first `/board` in a desktop session also asks it to act as boss: `dev-board: /board opened — load the dev-board:boss skill and act as boss for this session's workers.`, which starts a turn once it is idle (a prompt, not a command's hidden context, so the skill loads now rather than at the next message). It asks once per session: the mod keeps that in the session's state, so opening the pane again or a reload of the mod asks nothing; a prompt the app refuses shows as a toast and isn't retried.
- The mod bundles the boss rules as its `dev-board:boss` skill (`skills/boss/SKILL.md`): a copy of `internal/skill/desktop-boss/SKILL.md` named `boss`, which `go test ./internal/skill` checks and `go test ./internal/skill -update` rewrites. Edit the source, not the copy.
- A tile:
  - header row, never wrapping: ▾/▸ (fold), state dot (● busy, blocked, idle, stopped), title (cut to fit), and at the right task list progress (`3/7`) and time in its state (busy or idle; since its last activity once stopped: `12m`, `now` under a minute);
  - status row, in the section's color: status and what it says (`blocked · pick one`, `rate-limited · resumes 18:05`, `stuck · no progress for 17m`), the status left out where it repeats the section (`allow Bash: …` under Needs you, `Bash` under Working, `Auto-fix: PR #80` under Waiting);
  - meta row: branch · context use · PRs with their state (click to open) and an open PR's CI checks (✅ ❌ ⏳, from `gh pr checks`);
  - the app's verdict on its last turn (only a detail the status row lacks, when the status says the verdict) and what it needs (`needs: …`), health notes, the last two lines of its newest reply (code fences skipped) without list markers, backticks or bold, each line cut to one row; the task list once expanded; buttons.

  Idle tiles are folded to one row (title, progress, status where it doesn't repeat the section, time), unless they have a health note or a `needs: …`; the rest are open. **▸**/**▾** folds or unfolds one; that choice sticks across polls and status changes for the session. A folded tile with a note or a need ends in ⚠.
- Buttons per tile:
  - **Tasks** expands the task list above the buttons (✓ done, ▸ current, ○ pending).
  - **Open** opens the worker's session in the app (`claude://code/continue?session=<id>`); the main button for Needs you and Waiting tiles.
  - **Message…** opens a field; Enter asks the boss to relay its text: `dev-board: message <name> (<id>): <text>`.
  - **Nudge** asks the boss to relay `Continue where you left off`: `dev-board: nudge <name> (<id>): Continue where you left off`.
  - **Archive**, shown once `dev delivered <worktree>` exits 0 for a worker not busy with a PR and a worktree (else the tile says `not delivered: <why>`), checks it again and asks the boss to archive it: `dev-board: archive <name> (<id>): dev delivered exited 0`. A worker going busy drops its last check.
  - **Copy id**, last and plain, copies the worker's `local_…` id.

  The prompts wait for the boss to go idle; a toast says each was queued, or why not. A worker's permission prompt can't be answered here: answer it in its session (**Open**).
- Health notes (stuck, a decision unanswered, high context, rate-limited, resume due) show on their tile. A note the boss hasn't had yet goes to it as one prompt per poll for all new ones, e.g. `dev-board: fix-login stuck: no progress for 17m; docs-api resume due: rate limit reset 18:05: resume it`, which starts a boss turn once it is idle. The keys sent are kept in the session's state, so a reload of the mod sends none again; a prompt the app refuses shows as a toast and isn't retried.
- The band above the prompt sums them up, e.g. `1 needs you · 3 workers · 1 busy · 1 blocked · PR #78 open` (red while one needs you); hidden when there are none (or while a survey shows).

Every 2 s it runs `dev desktop board --json --boss $CLAUDE_CODE_HOST_SESSION_ID`; `gh pr checks` per open PR and `dev delivered` per worker not busy with a PR run at most once a minute each. Without `gh` on the app's `PATH` the PR's entry in the meta row says why. A failure (no `dev`, an old `dev` without the command, no app record for the session) shows its error in the pane; files `dev` could not read show as warnings above the tiles. Outside a desktop app session (no `CLAUDE_CODE_HOST_SESSION_ID`) it runs no `dev`; the pane says why.

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
