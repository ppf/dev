---
name: dev-agents
description: Coordinate other Claude Code agents running in dev tmux sessions — list them, start workers in git worktrees, send prompts, wait for them to finish, read their output. Use when asked to delegate, fan out, or drive other agent sessions with dev.
---

# Driving agent sessions with `dev agent`

You are a coordinator. Other Claude Code agents run in their own tmux sessions, opened by `dev`. You drive them with `dev agent`; you never type into their panes any other way.

## Rules

- **Target by session name or pane id** (`%12`), exactly as `dev agent list` prints them. Never by sidebar position or number.
- **Only close, kill or clean up what you started.** Never kill sessions, panes or worktrees you didn't create in this task, and never kill a session at all unless the user asked.
- **Never target your own pane.** `dev agent` refuses it.
- **Keep every call under your Bash tool timeout** (2 minutes by default, 10 at most). The `wait` and `prompt --wait` defaults (5m) exceed it: pass `--timeout` below it, or run long waits with `run_in_background`.
- Exit status: `0` ok, `1` error (bad target, no agent, agent busy or awaiting a decision, agent exited), `2` timeout, `3` stalled (the prompt wasn't picked up).

## Discover

```bash
dev agent list --json          # [{session, pane, agent, state, project, branch, path}]
dev agent get <target> --json
```

`state` is `working`, `waiting` (a permission prompt or question, or idle for a while), `idle`, or `""` (a Codex pane without the `dev state --codex` hooks). Claude panes without the `dev state` hook aren't listed.

## Start a worker

```bash
dev agent start <project> <worktree> --json   # creates the worktree and session if missing, never attaches
```

Give each worker its own worktree (a short branch-like name: `fix-login`, `docs/api`). It returns once the worker's Claude registered (default `--timeout 1m`); a timeout prints the worker's screen — often a trust or login prompt for the user. An existing session is reused, not recreated.

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

## Codex panes

With its hooks (`dev state --codex`), a Codex pane has a state and works like Claude; an approval request shows as `waiting` and refuses prompts. Without them (`state` is `""`), `wait` and `prompt --wait` refuse it: send with plain `prompt`, then poll `read` until its output stops changing.
