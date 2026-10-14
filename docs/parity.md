# Parity checklist

The plan's checklist (`docs/plan.md`), ticked where a test verifies it. Side-by-side tests run the bash
script and `dev` on identical isolated tmux servers and compare the result; they live in
`internal/integration` (build tag `integration`). Unit tests are named `package.TestName`.

Unticked items need a person, or belong to a later phase.

## Picker / `dev` (Phase 2)

- [x] `dev` opens the picker. Inside tmux it re-execs into an 80%×60% rounded popup guarded by
  `SIDEBAR_SELECT_IN_POPUP`: `TestPickerPopup` (160×30 border on a 200×50 client, the picker runs in it,
  its choice opens the session, exit status passed back). A failure in the popup stays on screen until
  Enter and its exit status 1 comes back: `TestPickerPopupFailure`.
- [x] `dev <name>` never opens a popup: `TestOpenParity` (run with `TMUX` set, opens directly).
- [x] `dev <name>` is positional; names clashing with subcommands are rejected at config load:
  `cmd.TestReservedNames`, `config.TestLoad`.
- [x] An unknown name prints the available projects and exits 1: `TestListParity` (same text as
  `select.sh` apart from the config path).
- [x] Existing sessions are attached, not recreated: `TestOpenParity` (second run, same layout and
  sessions as bash, the client switches).
- [x] The picker path always forces a new session (`-2`, `-3` suffix): `TestOpenPicker`.
- [x] `dev list` prints `name\tdescription\tpath`: `TestListParity` (byte-equal to `select.sh list`).
- [x] Project rows show `name description` with a `Path:` preview: `picker.TestProjectView`, seen on screen
  in `TestPickerPopup`.
- [x] Search is fzf's smart-case, with space-separated terms that must all match in any order:
  `picker.TestFilterSmartCaseAndRanked`, `picker.TestWorktreeStep` (`Main`, `PROJ-12` create worktrees,
  `fix wt` opens `proj-12-fix`, as `fzf --filter` does).
- [ ] The picker looks and feels like the fzf one (colours, cursor, preview border). Needs a person.
- Worktree step:
  - [x] Skipped silently when the path isn't a git repo: `TestOpenPicker` (one Enter opens `app-2`).
  - [x] Rows `main repo`, `+ new worktree`, `wt: <nested/name>` from `worktree list --porcelain`:
    `picker.TestWorktreeRows`, `worktree.TestList`.
  - [x] A typed name that matches nothing creates a worktree: `TestOpenPicker`, `TestWorktreeInclude`.
  - [x] `+ new` with an empty query asks for a name: `picker.TestWorktreeStep`.
  - [x] Esc exits 0: `TestPickerEsc` (both steps, nothing opened).
  - [x] The list wraps (the project list doesn't, as fzf without `--cycle`): `picker.TestWorktreeStep`,
    `picker.TestProjectStepNoCycle`.
  - [x] Name filtered to `A-Za-z0-9._/-`: `worktree.TestSanitize`.
- Worktree creation:
  - [x] Prune first: `worktree.TestEnsurePrunesDeletedWorktree`.
  - [x] An existing branch is checked out as-is with a notice; otherwise fetch, `remote set-head --auto`,
    branch from `origin/HEAD`, falling back to HEAD with a warning: `worktree.TestEnsureBase`.
  - [x] Warnings are echoed: `worktree.TestEnsureBase` (notices reach the callback),
    `open.TestWorktreeFailureShownOnce` (callback output printed before the failure).
  - [ ] Warnings are also sent via `display-message`. Not asserted: the message only flashes on the
    client's status line.
  - [x] A failure waits for Enter if there's a TTY: `TestWorktreeInclude` (dev cases answer
    "Press enter to close…" in a pane).
  - [x] `.worktreeinclude` is copied only on fresh creation: `worktree.TestEnsureReuseDoesNotCopyAgain`.
  - [x] On a failed copy the worktree is removed, the branch is deleted only if it was just created, and
    the run fails: `TestWorktreeInclude` (every case against both `project.sh` and the dev picker),
    `worktree.TestEnsureFailedCopy`.
- [x] Session name is `name-${wt//\//-}`; `@project` is the un-suffixed name: `TestOpenPicker`
  (`repo-feature-x`, snapshot equal to bash).
- Size chain:
  - [x] `client_width/height` inside tmux: `TestOpenParity` (same 200×50 geometry as bash).
  - [x] Chain order (tmux, stty, `COLUMNS`/`LINES`, tput) and the 80×24 fallback: `open.TestSizeSourcesOrder`,
    `open.TestTerminalSize`, `open.TestEnvSizeNeedsBoth`, `open.TestTmuxSizeOutsideTmuxDefers`.
  - [ ] `stty size </dev/tty` and `tput` outside tmux against a real terminal. Needs a person.
- First `dev` per server:
  - [x] `start-server ; set @dev_restored 1 ; run-shell restore.sh`, then print and unset
    `@dev_resurrect_warnings`; later runs skip it: `TestOpenFirstRunRestore` (a saved one-pane `app` is
    restored, output equal to bash).
  - [ ] It pauses on a TTY after printing warnings. Not tested.
  - [x] It runs before the has-session check: `TestOpenFirstRunRestore` (`dev app` attaches to the restored
    `app` instead of building the layout). Before the force-new suffix by reading the code.
- [x] Terminal title `\033]2;<session>\007`: `TestOpenParity` (output equal to bash).
- [x] Layout: windows `Agent` and `Files`, pane geometry, the stub command each pane ran (`claude`,
  `lazygit status --screen-mode=half`, `nvim .`), the sidebar's start command, `@project`,
  `@sidebar_pane`, the active window and pane: `TestOpenParity`, `TestOpenPicker`.

## Sidebar (Phase 3)

Not started; `dev <name>` still spawns bash `sidebar.sh`.

- [ ] Agent pane: hook-registered Claude, else `codex*`, else "no agent" (`agent.TestDetect` covers the
  rule; the sidebar doesn't use it yet).
- [ ] State trusted only when `@agent_cmd == pane_current_command`.
- [ ] Labels and title cleanup.
- [ ] Grouping by `@project`.
- [ ] View.
- [ ] Selection sync.
- [ ] Keys.
- [ ] Mouse.
- [ ] Kill.
- [ ] Ensure / toggle.
- [ ] `prefix+1..9` goto N, `prefix+0` new.

## Resurrect (Phase 4)

- [ ] `save` / `prune`.
- [ ] `pre-restore`.
- [ ] `restore`.

## Compact mode (Phase 4)

- [ ] Client hooks, zoom below 140 cols, unzoom when wide, sidebar and restore interplay, warnings.

## Hook (Phase 1)

- [x] Gated on `TMUX_PANE`; no-op without it: `TestState`, `cmd.TestState`.
- [x] Skip working/tool-failed when the top-level `agent_id` field is present: `TestState` (same result
  as the bash hook), `agent.TestResolve`.
- [x] `is_interrupt: true` → idle; otherwise tool-failed → working: `TestState`.
- [x] Only top-level fields count (deliberate fix: bash matched substrings anywhere): `TestState`.
- [x] Malformed JSON → no-op with a stderr message, exit 0: `TestState`, `cmd.TestState`.
- [x] `@agent_state` and `@agent_cmd` (the pane's current command) set in one call; `clear` unsets both:
  `TestState`.
- [ ] Live test with a real Claude session (idle → working → idle, subagent, waiting, crash leftover)
  after switching `settings.json`. Phase 1b, needs a person.

## Known differences from bash

- The sidebar pane's start command is `exec $HOME/.tmux/sessions/sidebar.sh` with `$HOME` left for the
  pane's shell; project.sh expands it first. Same program runs; `TestOpenParity` compares the two after
  expanding `$HOME`.
- `dev <name>` returns 1 when the final `switch-client`/`attach-session` fails on an existing session;
  project.sh exits 0 there.
- `dev state` rejects an unknown state (exit 1); the bash hook recorded any word.
- Sessions are matched exactly (`=name:`). Bash's `has-session -t name` also took a unique prefix, so with
  only `app-feature-x` alive `dev app` attached to it, and the picker's force-new loop gave `app-2` where
  dev gives `app`. A dotted name (`app-v1.2`) was parsed as a pane target and never found; dev finds it.
- Layout creation stops at the first failed tmux step and exits 1, leaving the half-built session
  detached; project.sh ran every step and still attached.
- The picker's search has no fzf extended operators (`'exact`, `^prefix`, `suffix$`, `!not`, `a | b`);
  they are matched as plain characters. A sanitized worktree name can't contain them.
- `dev import-projects` refuses a `projects.conf` with duplicate names or names clashing with a
  subcommand, naming the line, since `dev` would then reject the whole file; bash accepted both (first
  duplicate wins).
- In the picker popup, an error waits for Enter before the popup closes. Bash had no such early exit
  (fzf listed nothing on a missing config, project.sh never stopped on a tmux failure).
