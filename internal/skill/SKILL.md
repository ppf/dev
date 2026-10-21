---
name: dev-agents
description: Coordinate other Claude Code agents running in dev tmux sessions — list them, start workers in git worktrees, send prompts, wait for them to finish, read their output. Use when asked to delegate, fan out, or drive other agent sessions with dev.
---

# Driving agent sessions with `dev agent`

You are a coordinator. Other Claude Code agents run in their own tmux sessions, opened by `dev`. You drive them with `dev agent`; you never type into their panes any other way.

A coordinator usually lives in `dev boss`: the user types requests into your prompt while the pane beside you previews every worker, so they see the workers you start without you pasting their screens.

## Rules

- **In `dev boss`, delegate, don't do.** You only coordinate: start, prompt and read workers, relay their results, ask the user, and run single-command lookups to route work. Anything longer (multi-step research, browser testing, drafting, file edits, multi-call verification) goes to a worker. Each new feature or piece of work gets a fresh worker (`dev agent start` with a new worktree name), or a reset one (see [A stream of tasks in one area](#a-stream-of-tasks-in-one-area)); prompt an existing one of yours only with changes to its current work (fixes, review findings, changes to the same feature or PR), never with unrelated or merely related work. When unsure, delegate.
- **Target by session name or pane id** (`%12`), exactly as `dev agent list` prints them. Never by sidebar position or number.
- **Close only your own workers, and only with `dev agent close`.** Never kill sessions, panes or worktrees any other way (no raw `tmux kill-session` or `git worktree remove`), and never close or kill ones you didn't start. If `close` refuses and the user still wants a worker gone, tell them why; don't work around it.
- **Never target your own pane.** `dev agent` refuses it.
- **Keep every call under your Bash tool timeout** (2 minutes by default, 10 at most). The `wait` and `prompt --wait` defaults (5m) exceed it: pass `--timeout` below it, or run long waits with `run_in_background`.
- Exit status: `0` ok, `1` error (bad target, no agent, agent busy or awaiting a decision, agent exited), `2` timeout, `3` stalled (the prompt wasn't picked up).

## Discover

```bash
dev agent list --json          # [{session, pane, agent, state, queued?, project, branch, path, cost_usd?, started_by?, boss?}]
dev agent list --mine --json   # only your workers: the sessions you started with dev agent start
dev agent get <target> --json
```

`state` is `working`, `waiting` (a permission prompt or question, or idle for a while), `idle`, or `""` (a Codex pane without the `dev state --codex` hooks). `queued: true` marks a working Claude holding a prompt it hasn't read yet. Claude panes without the `dev state` hook aren't listed.

Outside tmux (e.g. the Claude desktop app), pass `--boss <session>` (a `dev boss` session) to `start`, `list --mine` and `close`: you act as that boss.

Your workers ("child sessions") are the ones `--mine` lists; `started_by` names the session that started a worker, and `boss: true` marks a coordinator, never a worker. Every other session is the user's own: drive it only when asked. Use `dev agent` for dev sessions, not Claude's own session or agent tools.

## Start a worker

```bash
dev agent start <project> <worktree> --json   # creates the worktree and session if missing, never attaches
```

Give each worker its own worktree (a short branch-like name: `fix-login`, `docs/api`). It returns once the worker's Claude registered (default `--timeout 1m`); a timeout prints the worker's screen — often a trust or login prompt for the user. An existing session is reused, not recreated. `--agent codex` starts a Codex worker instead (only when the user asks for Codex). It returns once codex runs, before it may show a trust or login screen: `read` it and answer with `send-keys` (or ask the user) before the first prompt. `prompt --wait`/`wait` work only if the user set up its Codex hooks, else use plain `prompt` and `read`. `--agent grok` works the same for a Grok worker (only when the user asks for Grok; its Grok hooks). A reused session running another agent than `--agent` asks for is an error.

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

Plain `prompt` (no `--wait`) returns at once (a slash command within 3 s) without that check; use it only for a Codex pane without hooks, or to queue a follow-up you won't wait on. To a working worker it prints `queued: …`: Claude reads the prompt at its turn's next step, and `get --json` shows `queued: true` until then. A following `wait` covers the queued work too.

- Multi-line prompts: pass them on stdin (`dev agent prompt <target> <<'EOF' … EOF`); they submit as one prompt. Text starting with `-` goes after `--` or on stdin, or it's read as a flag.
- `--wait` needs the worker idle (or `waiting` only because it sat idle). If it is `working`, wait for it.
- **Slash commands** (`/effort high`, `/model sonnet`) go through `prompt` like any text, `--wait` included. Mid-conversation they open a "Change effort level?"/"Switch model?" dialog: `prompt` confirms it with `1` itself once it shows, and fails if the command isn't seen running within 3 s. Never send `1` ahead or after: it would stay in the input box. A command opening any other dialog is an error: `read` it and answer with `send-keys`.
- **Awaiting a decision:** `prompt` refuses a worker showing a permission prompt or question, since the text would answer it. `read` the pane, then answer with `send-keys` (`1`, `2`, `enter`, `esc`), or ask the user when the decision is theirs. `waiting` after a prompt usually means exactly this.
- **Reset a worker's context:** `dev agent prompt <target> --wait /clear` returns once its new conversation started (exit 0, `idle`); `/clear` starts no turn, so this waits for the new session instead. Exit 3: it wasn't confirmed, `read` the pane. Codex and Grok panes refuse it with `--wait`.
- **Exit 3 (stalled):** the text may still sit in the worker's input box. `dev agent read` it; press `dev agent send-keys <target> enter` if the prompt is there, or clear it before sending another. Don't stack prompts. If every `--wait` stalls, the worker's hook runs an older `dev`: tell the user to update it.
- **Waiting on CI:** `gh pr checks --watch` and hand-rolled `until` loops hang or end early, leaving a worker shown as working for many minutes. A brief that ends in waiting for CI tells the worker to poll `gh pr checks <n>` every 30 s in a bounded loop, run with `run_in_background` (exit `0` all passed, `8` pending, `1` a failure; right after a push it may report no checks yet: run it again):

  ```bash
  for i in $(seq 30); do gh pr checks <n> >/dev/null 2>&1; [ $? = 8 ] || break; sleep 30; done; gh pr checks <n>
  ```

## Read results

```bash
dev agent read <target> --lines 80
```

This is the worker's screen: Claude Code usually draws full-screen, so there is no scrollback, and long replies are cut off. For anything longer than a few lines, ask the worker to write its result to a markdown file and reply only with the file's path, then read that file:

> When done, write your findings to `<worktree>/RESULT.md` and reply with just that path.

## Close finished workers

```bash
dev agent close <target> [--json]   # → {session, worktree, branch?, results?, files?}
```

Once a worker's work is delivered (its PR merged, or its result reported with nothing left for it) and the user hasn't asked to keep it, close it without asking and mention it in one line ("closed fix-login: PR #12 merged"). Keep it when a follow-up for it is likely soon.

It closes only a worker you started, idle (or waiting only because it sat idle), whose worktree has no uncommitted changes to tracked files and whose commits are all in origin's default branch (it fetches first), or whose PR `gh` reports merged into that branch at the worker's HEAD: so squash- and rebase-merged PRs count, while a commit after the merge is refused. Otherwise it refuses, naming the failed check, and touches nothing in the worker: fix the cause, or leave the worker. Untracked files (e.g. `RESULT.md`) are copied to `$XDG_STATE_HOME/dev/results/<session>/` first (`<session>.2`, … when a closed worker of that name already left one), and it prints where; ignored files are discarded, so a result goes in a file git doesn't ignore. Then it kills the session (moving clients watching it to yours), removes the worktree and force-deletes the branch.

## A stream of tasks in one area

For a series of items in one area (one PR each, item after item), keep each worker's context short: a worker that runs for hours reaches a full context, and auto-compaction loses detail.

- **Default: a fresh worker per item.** Once an item's PR is merged, `close` the worker and `start` the next item in a new worktree (`<area>-<item>`). It starts from the current default branch with an empty context and nothing left over, and its `RESULT.md` is archived.
- **Reset in place** (`dev agent prompt <w> --wait /clear`) only when the worktree is worth keeping between items: installed dependencies, warm build caches, a running dev server, local setup. Begin the next brief with switching to a new branch off the fresh default branch (`git fetch origin && git switch -c <item> origin/<default>`), and remove the last item's `RESULT.md`, or the next `close` archives it with the new item's. Leave the old branch: `close` deletes only the current one, and `git branch -D` would skip close's merge check.
- **Either way, only between items:** never reset or replace a worker with undelivered work (open PR, review fixes pending, uncommitted changes). After a reset the worker remembers nothing, so the brief is self-contained: goal, constraints, where things are.
- **Context getting high:** when a `high context` inbox note comes, a long-lived worker's screen shows little context left, or it compacted, reset or replace it at the next item boundary rather than starting another item in it.

## Interrupt

```bash
dev agent send-keys <target> esc       # stop the current turn
```

Esc to a working worker returns once its screen shows the turn interrupted, and records it idle (Claude runs no hook for that), so `prompt --wait` works right after. A worker interrupted by hand is caught the same way at the next `prompt` or `wait`.

Keys: `esc`, `enter`, `tab`, `shift+tab`, `space`, `backspace`, `up`, `down`, `left`, `right`, `ctrl+c`, `ctrl+d`, or a single character. Avoid `ctrl+c` twice and `ctrl+d`: they quit Claude.

## Boss inbox

In `dev boss`, you hear of your workers without polling: a prompt or extra context starting `[dev boss inbox]` lists one line per worker event (`<worker> finished: <its last message>`, `failed: <error>`, `needs a decision: <what it asks>`, `asks: <the question its turn ended on>`, `stuck: …`, `high context: 85% used`, `resumed: …`). It arrives when you stop, when the user types, or as a prompt while you're idle with an empty input box. On each batch:

- **Act unasked when the next step is clear and safe:** prompt the worker with it, start a follow-up worker, or hand finished work to review or a PR. Report what you did in one or two lines.
- **Ask the user only for real decisions:** permission prompts and dialogs (never answer one yourself), anything destructive or external (merging, pushing, posting), unclear goals. Batch the questions into one message. Closing a finished worker with `dev agent close` isn't one of them.
- **Rate limits:** a `failed: rate_limit: … resets 7:10pm (…); dev resumes it at 19:11` note needs nothing from you: dev sends the worker "continue" after the reset and tells you with a `resumed` note. Don't prompt it meanwhile (that cancels the resume). Only a note with `not resuming it` (no reset time read, or 3 resumes in a row) is yours: tell the user, or resume it yourself once the limit is back. Your own turns may fail on the same limit; carry on with the batch when you're prompted again.
- **Asks:** the worker's turn ended on a question to you and it waits (the note has only the question: `read` it for the rest of its last message). Answer it with `prompt` when you can, else ask the user. Unanswered, it comes again 30m, 1h, 2h, … after it asked, while the worker rests.
- **Stuck:** `read` the worker; nudge it if the fix is obvious, otherwise tell the user. The kinds:
  - `no progress for 15m`: working, but no finished tool call (its own or a subagent's) and no change on its screen for that long: a hung or long silent command, or a stalled turn. A long turn that keeps finishing tool calls doesn't show as stuck.
  - `no progress for 15m, waiting on its background tasks`: its turn ended but waits on a background shell or subagent that does nothing visible, e.g. a dev server left running or a hung `--watch`. Stuck waiting on CI: run `gh pr checks <n>` yourself and nudge it with the result.
  - `waiting on a decision for 3m, unanswered`: a permission prompt or question nobody answered.
  - `idle for 15m, PR #12 has merge conflicts` (and/or `no CI checks`): its open PR can't move on while it sits; tell it to rebase or find out why CI didn't run.
- **High context:** the worker's context window is that full; auto-compaction will soon lose detail. At its next item boundary, reset or replace it (see [A stream of tasks in one area](#a-stream-of-tasks-in-one-area)).
- **When a batch of work is done,** give a short roll-up: what finished, what waits on the user, what's next.
- **Propose improvements sparingly:** in a roll-up, or when idle with nothing pending (never mid-task), at most one or two, only ones really worth doing and grounded in what you saw coordinating (repeated worker friction, flaky tests, recurring review findings, missing tooling, rough UX the user hit). One line each: why it's worth it and a rough size. Start one only on the user's yes, even when it looks clear and safe; don't propose again one they declined.

## Codex panes

With its hooks (`dev state --codex`), a Codex pane has a state and works like Claude; an approval request shows as `waiting` and refuses prompts. Without them (`state` is `""`), `wait` and `prompt --wait` refuse it: send with plain `prompt`, then poll `read` until its output stops changing.
