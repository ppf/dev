# dev-board

A Claude Code mod for a boss session in the Claude desktop app: a live board of the sessions it started (its workers), from `dev desktop board --json`.

- `/board` opens the **Workers** pane (the desktop app docks it in the right side panel), its tiles in sections by status, each with its count beside it, empty ones hidden: **Needs you** (red), **Working** (working, stuck, rate-limited; accent), **Waiting** (waiting, blocked, review ready; amber), **Idle** (idle, stopped; muted). A section's color marks its header, its open tiles' left rule and their status; colors are the app theme's, so light and dark both work.
- The first `/board` in a desktop session also asks it to act as boss: `dev-board: /board opened — load the dev-board:boss skill and act as boss for this session's workers.`, which starts a turn once it is idle (a prompt, not a command's hidden context, so the skill loads now rather than at the next message). It asks once per session: the mod keeps that in the session's state, so opening the pane again or a reload of the mod asks nothing; a prompt the app refuses shows as a toast and isn't retried.
- The mod bundles the boss rules as its `dev-board:boss` skill (`skills/boss/SKILL.md`): a copy of `internal/skill/desktop-boss/SKILL.md` named `boss`, which `go test ./internal/skill` checks and `go test ./internal/skill -update` rewrites. Edit the source, not the copy.
- A tile, its rows all starting under its title, the fold button and state dot beside them:
  - beside the rows: ▾/▸ (fold) and state dot (● busy, blocked, idle, stopped);
  - header row, never wrapping: title (cut to fit) and, at the right, its time in its state (busy or idle; since its last activity once stopped: `12m`, `now` under a minute);
  - status row, in the section's color, one line cut to fit, without backticks or bold: status and what it says (`blocked · pick one`, `rate-limited · resumes 18:05`, `stuck · no progress for 17m`), the status left out where it repeats the section (`allow Bash: …` or `waiting for you` under Needs you, `Bash` under Working, `Auto-fix: PR #80` under Waiting);
  - meta row: branch · context use · PRs with their state (click to open) and an open PR's CI checks (✅ ❌ ⏳, from `gh pr checks`);
  - the app's verdict on its last turn (only a detail the status row lacks, when the status says the verdict) and what it needs (`needs: …`), health notes, the last two lines of its newest reply (code fences and a line saying what the status row does skipped) without list markers, backticks or bold, each line cut to one row; the task list; buttons.
  - task list, from the worker's update_plan: **Tasks ▸**/**▾** (folds or unfolds it), a progress bar (done steps green on a grey track) and the count (`2/5`); collapsed, the step at hand under it; expanded, every step, a mark and its text cut to one row: ● done (green, dimmed), ◐ current (in the section's color, its text stronger), ○ pending. With more than 7 steps, the leading done steps but the last fold into `● n done`, and the steps past the room into `○ n more` (the current step always shows).

  Idle tiles are folded to one row (title, task progress, status where it doesn't repeat the section, time; title and status cut to fit), unless they have a health note or a `needs: …`; the rest are open. **▸**/**▾** folds or unfolds one; that choice sticks across polls and status changes for the session. A folded tile with a note or a need ends in ⚠.
- Buttons per tile:
  - **Open** opens the worker's session in the app (`claude://code/continue?session=<id>`); the main button for Needs you and Waiting tiles.
  - **Message…** opens a field with **Send** and **Cancel**; Enter or Send asks the boss to relay its text: `dev-board: message <name> (<id>): <text>`; a message the app refuses opens the field again, holding its text.
  - **Nudge** asks the boss to relay `Continue where you left off`: `dev-board: nudge <name> (<id>): Continue where you left off`.
  - **Archive**, shown once `dev delivered <worktree>` exits 0 for a worker not busy with a PR and a worktree (else the tile says `not delivered: <why>`), checks it again and asks the boss to archive it: `dev-board: archive <name> (<id>): dev delivered exited 0`. A worker going busy drops its last check.
  - **Copy id**, last and plain, copies the worker's `local_…` id.

  The prompts wait for the boss to go idle; a toast says each was queued, or why not. A worker's permission prompt can't be answered here: answer it in its session (**Open**).
- A worker's question (AskUserQuestion) shows on its tile in place of the status row: each question's header and text, each option a button with its description dim beside it. One question with one pick: pressing an option answers it. A multi-select (☐/☑) or several questions (○/◉ each): pick, then **Send**, which says which question still lacks a pick. The answer is the person's pick, as the dialog would send it (a multi-select's labels comma-joined), and the engine aborts the worker's own dialog; a pick there first wins instead, and the board's answer is refused. The tile then says `sent: …; waiting for the worker` until the question is gone.

  How: every desktop session loads this mod, so a worker's own copy sees its AskUserQuestion. While the dialog is open it writes the question to `questions/<app session id>.json` in dev's state directory (`~/.local/state/dev/desktop/`); the board runs `dev desktop answer <id> <tool use id>`, which writes `<id>.answer.json` (atomically, only for the question still open), and the worker's copy answers with it. Both files go once it is answered, either way. `dev desktop board` removes those its mod left behind (a reload, a crash): of a worker no longer running (once the file is a minute old: one started since the board read the live files may have just written it), or running no turn, or whose transcript has the answer; and anyone's older than a week. A question read only from the transcript (a worker whose mod is older, or could not write the file) can't be answered here: its options open its session, and the tile says so. A permission prompt still can't be answered here: **Open**.
- A worker whose last turn ended on a wrap_up question (`ask`) shows it the same way, its reply buttons the options (app actions such as Commit left out), and its tile is open even when idle. Pressing one asks the boss to message the worker that label (`dev-board: message …`, as **Message…** does); without reply buttons it is answered with **Message…**. It goes once the worker gets a message or starts a turn; a busy worker's open AskUserQuestion is newer and shows instead.
- Health notes (stuck, a decision unanswered, high context, rate-limited, resume due) show on their tile. A note the boss hasn't had yet goes to it as one prompt per poll for all new ones, e.g. `dev-board: fix-login stuck: no progress for 17m; docs-api resume due: rate limit reset 18:05: resume it`, which starts a boss turn once it is idle. The keys sent are kept in the session's state, so a reload of the mod sends none again; a prompt the app refuses shows as a toast and isn't retried.
- The band above the prompt sums them up, e.g. `1 needs you · 3 workers · 1 busy · 1 blocked · PR #78 open` (red while one needs you); hidden when there are none (or while a survey shows).

Every 2 s it runs `dev desktop board --json --boss $CLAUDE_CODE_HOST_SESSION_ID`; `gh pr checks` per open PR and `dev delivered` per worker not busy with a PR run at most once a minute each. Without `gh` on the app's `PATH` the PR's entry in the meta row says why. A failure (no `dev`, an old `dev` without the command, no app record for the session) shows its error in the pane; files `dev` could not read show as warnings above the tiles, as does a live session status it doesn't know (that worker shows busy). Outside a desktop app session (no `CLAUDE_CODE_HOST_SESSION_ID`) it runs no `dev`; the pane says why.

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
