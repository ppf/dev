---
name: dev-agents
description: Coordinate other Claude Code agents running in dev tmux sessions — list them, start workers in git worktrees, send prompts, wait for them to finish, read their output. Use when asked to delegate, fan out, or drive other agent sessions with dev.
---

# Driving agent sessions with `dev agent`

You are a coordinator. Other Claude Code agents run in their own tmux sessions, opened by `dev`. You drive them with `dev agent`; you never type into their panes any other way.

A coordinator usually lives in `dev boss`: the user types requests into your prompt while the pane beside you previews every worker, so they see the workers you start without you pasting their screens.

## Rules

- **Target by session name or pane id** (`%12`), exactly as `dev agent list` prints them. Never by sidebar position or number.
- **Only close, kill or clean up what you started.** Never kill sessions, panes or worktrees you didn't create in this task, and never kill a session at all unless the user asked.
- **Never target your own pane.** `dev agent` refuses it.
- **Keep every call under your Bash tool timeout** (2 minutes by default, 10 at most). The `wait` and `prompt --wait` defaults (5m) exceed it: pass `--timeout` below it, or run long waits with `run_in_background`.
- Exit status: `0` ok, `1` error (bad target, no agent, agent busy or awaiting a decision, agent exited), `2` timeout, `3` stalled (the prompt wasn't picked up).

## Discover

```bash
dev agent list --json          # [{session, pane, agent, state, project, branch, path, cost_usd?, started_by?, boss?}]
dev agent list --mine --json   # only your workers: the sessions you started with dev agent start
dev agent get <target> --json
```

`state` is `working`, `waiting` (a permission prompt or question, or idle for a while), `idle`, or `""` (a Codex pane without the `dev state --codex` hooks). Claude panes without the `dev state` hook aren't listed.

Your workers ("child sessions") are the ones `--mine` lists; `started_by` names the session that started a worker, and `boss: true` marks a coordinator, never a worker. Every other session is the user's own: drive it only when asked. Use `dev agent` for dev sessions, not Claude's own session or agent tools.

## Start a worker

```bash
dev agent start <project> <worktree> --json   # creates the worktree and session if missing, never attaches
```

Give each worker its own worktree (a short branch-like name: `fix-login`, `docs/api`). It returns once the worker's Claude registered (default `--timeout 1m`); a timeout prints the worker's screen — often a trust or login prompt for the user. An existing session is reused, not recreated. `--agent codex` starts a Codex worker instead (only when the user asks for Codex). It returns once codex runs, before it may show a trust or login screen: `read` it and answer with `send-keys` (or ask the user) before the first prompt. `prompt --wait`/`wait` work only if the user set up its Codex hooks, else use plain `prompt` and `read`. A reused session running another agent than `--agent` asks for is an error.

## Prompt and wait

Always send with `--wait`: it confirms the worker picked the prompt up before it waits, so it never mistakes the idle state from before the prompt for the end of the task.

```bash
dev agent prompt <target> --wait --timeout 100s "Fix the failing test in internal/foo. Reply DONE when green."
```

Exit 0: the turn ended (prints `idle` or `waiting`). Exit 2: picked up but still working — keep waiting in a loop, checking progress in between:

```bash
dev agent wait <target> --timeout 100s     # exit 2 = still working; read, then wait again
dev agent read <target> --lines 40
```

Plain `prompt` (no `--wait`) returns at once without that check; use it only for a Codex pane without hooks, or to queue a follow-up you won't wait on.

- Multi-line prompts: pass them on stdin (`dev agent prompt <target> <<'EOF' … EOF`); they submit as one prompt. Text starting with `-` goes after `--` or on stdin, or it's read as a flag.
- `--wait` needs the worker idle (or `waiting` only because it sat idle). If it is `working`, wait for it.
- **Awaiting a decision:** `prompt` refuses a worker showing a permission prompt or question, since the text would answer it. `read` the pane, then answer with `send-keys` (`1`, `2`, `enter`, `esc`), or ask the user when the decision is theirs. `waiting` after a prompt usually means exactly this.
- **Exit 3 (stalled):** the text may still sit in the worker's input box. `dev agent read` it; press `dev agent send-keys <target> enter` if the prompt is there, or clear it before sending another. Don't stack prompts. If every `--wait` stalls, the worker's hook runs an older `dev`: tell the user to update it.

## Read results

```bash
dev agent read <target> --lines 80
```

This is the worker's screen: Claude Code usually draws full-screen, so there is no scrollback, and long replies are cut off. For anything longer than a few lines, ask the worker to write its result to a markdown file and reply only with the file's path, then read that file:

> When done, write your findings to `<worktree>/RESULT.md` and reply with just that path.

## Interrupt

```bash
dev agent send-keys <target> esc       # stop the current turn
```

Keys: `esc`, `enter`, `tab`, `shift+tab`, `space`, `backspace`, `up`, `down`, `left`, `right`, `ctrl+c`, `ctrl+d`, or a single character. Avoid `ctrl+c` twice and `ctrl+d`: they quit Claude.

## Boss inbox

In `dev boss`, you hear of your workers without polling: a prompt or extra context starting `[dev boss inbox]` lists one line per worker event (`<worker> finished: <its last message>`, `failed: <error>`, `needs a decision: <question>`, `stuck: working for 25m` or `waiting on a decision for 4m, unanswered`). It arrives when you stop, when the user types, or as a prompt while you're idle with an empty input box. On each batch:

- **Act unasked when the next step is clear and safe:** prompt the worker with it, start a follow-up worker, or hand finished work to review or a PR. Report what you did in one or two lines.
- **Ask the user only for real decisions:** permission prompts and dialogs (never answer one yourself), anything destructive or external (merging, pushing, posting), unclear goals. Batch the questions into one message.
- **Stuck:** `read` the worker; nudge it if the fix is obvious, otherwise tell the user.
- **When a batch of work is done,** give a short roll-up: what finished, what waits on the user, what's next.
- **Propose improvements sparingly:** in a roll-up, or when idle with nothing pending (never mid-task), at most one or two, only ones really worth doing and grounded in what you saw coordinating (repeated worker friction, flaky tests, recurring review findings, missing tooling, rough UX the user hit). One line each: why it's worth it and a rough size. Start one only on the user's yes, even when it looks clear and safe; don't propose again one they declined.

## Codex panes

With its hooks (`dev state --codex`), a Codex pane has a state and works like Claude; an approval request shows as `waiting` and refuses prompts. Without them (`state` is `""`), `wait` and `prompt --wait` refuse it: send with plain `prompt`, then poll `read` until its output stops changing.
