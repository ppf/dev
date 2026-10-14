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

The side-by-side tests run bash (`install-sidebar-keys.sh`'s bindings, `sidebar.sh` started by
`toggle-sidebar.sh`) on one server and `dev sidebar` (no bindings) on its twin, and compare the state after
every step. Keys and clicks are typed into the attached client: `send-keys` to a pane bypasses the key
tables the bash bindings live in. `TestSidebarViewParity` puts both sidebars side by side on one server
instead, so they read the same state.

- [x] Agent pane: hook-registered Claude, else `codex*`, else "no agent"; lazygit and nvim are no longer
  Claude (deliberate fix): `agent.TestDetect`, `sidebar.TestModelBuildSnapshot`; on screen in
  `TestSidebarViewParity` (registered panes, a `codex` binary, sessions without an agent). The branch is read
  only for an agent pane: `sidebar.TestModelBuildSnapshot`, `TestModelBranchCache`.
- [x] State trusted only when `@agent_cmd == pane_current_command`: `agent.TestDetect`,
  `sidebar.TestModelBuildSnapshot` (state left after claude exited).
- [x] Labels and title cleanup: `sidebar.TestModelCleanTitle` (goldens from sidebar.sh's perl and bash
  regex), `TestModelIsGenericTitle`, `TestModelAgentTitle`; `TestSidebarViewParity` ("Claude Code" blanked,
  Codex `a | b` shown as `b on a`).
- [x] Grouping by `@project`, else `${name%-[0-9]*}`, with a rule line per group and a blank line between:
  `sidebar.TestModelGroup`, `TestModelOrder`, `TestSidebarViewParity`.
- [x] `@sidebar_session_order` in the same order and format (each name newline-terminated):
  `sidebar.TestModelRefresh`; equal to sidebar.sh's after every step of `TestEnsureParity`; bash's
  `move_selection` reads the order a dev sidebar wrote (grouped, unlike `list-sessions`):
  `TestSessionOrderReadByBash`.
- [x] View: `capture-pane -e` byte-equal to sidebar.sh unfocused at widths 40, 30 and 11 (no buttons) and
  focused at 40 (at 30 and 11 the rows above the hint are equal and dev clips the hint, see below), with the
  selection background and red ×, the bold current session, the red waiting line, the faint "no agent" line,
  `…` truncation and the hint only while focused: `TestSidebarViewParity`;
  `sidebar.TestViewRenderGolden` (goldens rendered by sidebar.sh's own functions, also "No sessions" and a
  pane shorter than the list), `sidebar.TestViewWideRunes`.
- [ ] Colours and feel in a real terminal. Needs a person.
- [x] Selection sync: `sidebar.TestModelSyncSelection` (also an empty option taking the own session);
  `TestSidebarViewParity` (both follow `@sidebar_selected_session`), `TestSidebarKeysParity` (after `q`
  the unfocused sidebar of the viewed session adopts it, as bash does).
- [x] Keys: `j/k/↑/↓` wrap; `⏎` switches (creating the target's sidebar) or, on the current session, focuses
  its content; `q`; `x` on another session, on the current one (switches to the first other session in order
  first, as `kill_session_now`) and on the last one (refused, same message); `r` through the client's
  `command-prompt`; `d` opens the picker popup: `TestSidebarKeysParity`. Dev only: a name with a quote,
  ctrl+c: `TestActionKeys`, `sidebar.TestActionRename`, `sidebar.TestInputCtrlC`.
- [x] Mouse, real SGR clicks through the client: a row switches, a click below a name row selects that row's
  session, a click on the current session focuses its content, × kills, + on row 0 opens the picker:
  `TestSidebarMouseParity`, `TestActionMouse`, `sidebar.TestInputMouse`.
- [x] Kill of a worktree session no other session uses: the 70×8 popup, Esc cancels, `n` keeps the worktree,
  `y` removes it, also from the session's own sidebar (which dies while the popup carries on):
  `TestSidebarKillWorktreeParity`, `TestActionKillWorktree`. A shared worktree is closed plainly:
  `sidebar.TestActionKill`, `TestActionWorktreeShared`.
- [x] Ensure / toggle: `@sidebar_hidden`, ensure does nothing while hidden, focus given back, an existing
  sidebar focused, a compact zoom kept by ensure and by hiding: `TestEnsureParity` (13 steps against
  toggle-sidebar.sh), `TestCompactDev`, `sidebar.TestEnsure`, `TestToggleHide`, `TestToggleShow`.
- [x] A running bash sidebar is replaced: `TestEnsureReplacesBashSidebar`, with a stand-in pane whose start
  command contains `sidebar.sh` (a real one comes with toggle-sidebar.sh, which installs bash's root
  bindings on the server).
- [x] `prefix+1..9` goto N: `TestEnsureParity` (2, 3 and out of range against `sidebar-action.sh goto`),
  `sidebar.TestGoto`. `prefix+0` new: `TestSidebarMouseParity` (`dev new` and `sidebar-action.sh new` open
  the picker popup).
- [x] `dev <name>` layouts run `dev sidebar`, and an existing session gets one through ensure:
  `TestOpenParity`, `TestOpenPicker`, `TestResurrectDev`.
- [ ] Cutover (3e): `toggle-sidebar.sh`, `install-sidebar-keys.sh` and the `.tmux.conf` bindings live in
  ppf/terminal. Not done here. Install a dev with this phase only together with 3e: while
  install-sidebar-keys.sh's root bindings are active they take the Go sidebar's keys and clicks and resolve
  them through `@sidebar_row_map`, which dev never writes.

## Resurrect (Phase 4)

- [x] `save` / `prune`: `TestResurrectHooksParity` (same `.dev` file, exit status and warning as
  dev-resurrect.sh; a missing snapshot fails and keeps the old file; prune deletes only orphans),
  `TestResurrectDev` (save during a real resurrect save), `resurrect.TestSave`, `TestPrune`.
- [x] `pre-restore`: `TestResurrectHooksParity`, `resurrect.TestPreRestore`.
- [x] `restore`: `TestResurrectDev` (`TestRestore` with the hooks running dev: a cold start rebuilds a Go
  sidebar, `@project`, undoes the zoom, keeps focus, no warnings, removes the live list; a manual restore with
  hidden sidebars leaves live panes alone), `resurrect.TestRestore` (skipped live sessions, the slot check,
  warnings, focus and active window).
- [ ] The `.tmux.conf` hooks call the binary. Cutover in ppf/terminal (4a).

## Compact mode (Phase 4)

- [x] Client hooks, zoom below 140 columns, unzoom when wide, sidebar interplay: `TestCompactDev`
  (`TestCompact` with the client-* hooks running `dev compact`), `compact.TestDecide`, `TestZoomTarget`,
  `TestRunZoomsNarrow`, `TestRunWide`. Restore interplay: `TestResurrectDev`.
- [x] Warnings to the hook client and `@dev_compact_warnings`, with tmux's stderr as bash's `2>&1`:
  `compact.TestRunWarnsPerWindow`, `TestRunUnmarkWarns`, `TestRunStops`; none in `TestCompactDev`.
- [ ] The `.tmux.conf` hooks call the binary. Cutover in ppf/terminal (4a).

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

- The sidebar pane runs `exec '<dev binary>' 'sidebar'` instead of `exec $HOME/.tmux/sessions/sidebar.sh`;
  the parity snapshots leave the sidebar's start command out and check each separately.
- `dev <name>` returns 1 when the final `switch-client`/`attach-session` fails on an existing session;
  project.sh exits 0 there.
- `dev state` rejects an unknown state (exit 1); the bash hook recorded any word.
- Sessions are matched exactly (`=name:`). Bash's `has-session -t name` also took a unique prefix, so with
  only `app-feature-x` alive `dev app` attached to it, and the picker's force-new loop gave `app-2` where
  dev gives `app`. A dotted name (`app-v1.2`) was parsed as a pane target and never found; dev finds it.
- Layout creation stops at the first failed tmux step and exits 1, leaving the half-built session
  detached; project.sh ran every step and still attached.
- The picker's search supports fzf's extended operators (`'exact`, `^prefix`, `suffix$`, `!not`, `a | b`)
  and picks the same rows, but ranks them by a simpler score: tightest, earliest match, then the shorter
  row. fzf's word-boundary bonuses aren't ported, so when several rows match the order can differ
  (`api` lists `rapid tool` before `a-p-i`; fzf the reverse).
- `dev import-projects` refuses a `projects.conf` with duplicate names or names clashing with a
  subcommand, naming the line, since `dev` would then reject the whole file; bash accepted both (first
  duplicate wins).
- In the picker popup, an error waits for Enter before the popup closes. Bash had no such early exit
  (fzf listed nothing on a missing config, project.sh never stopped on a tmux failure).

Sidebar:

- Sessions are matched exactly (`=name:`) here too. Bash's bare `-t name` also resolved a window name in the
  current session: a session `b` beside windows named `bash` read the current session's `@sidebar_pane`, so a
  click on `b` switched without creating its sidebar.
- Keys are read by the sidebar instead of root-table bindings and a `sidebar` key table. A click focuses the
  sidebar first (tmux's default `MouseDown1Pane`), so after × or + focus is on the sidebar; bash's binding
  acted without focusing it. Ctrl+C quits the dev sidebar.
- Selection sync and every action use the most recently active client viewing the sidebar's session; bash
  took the first client listed.
- `@sidebar_session_order` and the selection are written only when they change (bash: every second);
  `@sidebar_row_map` is never written. The order is Go byte order (bash's `sort` followed the locale), and
  `dev goto` computes it fresh (bash read the option, stale while sidebars were hidden).
- The generic-title check compares with the host name (`os.Hostname`), not the literal `mac.home`; a
  leading symbol is anything but a Unicode letter or digit.
- The branch is cached per path and re-read when `<git-dir>/HEAD` changes; bash ran git per session every
  second.
- A failed refresh keeps the last screen with the error in red on the bottom row, on one line; before the
  first successful refresh the error shows alone on that row (`TestActionErrorBeforeFirstRefresh`,
  `sidebar.TestViewErrorBeforeFirstRefresh`). A pane working directory or `@project` containing `\x1f`
  fails every refresh while it exists (tmux refuses or strips it in titles and session names); bash drew
  the list with shifted fields.
- Text is printed as is (bash used `printf %b`, so a `\n` in a title was a newline); widths are display
  widths. A hint wider than the pane is clipped; bash wrapped it, scrolling the screen (`TestSidebarViewParity`
  checks dev's clipped hint at widths 30 and 11).
- tmux failures bash ignored are shown as `❌ …` on the client: a failed query while looking up a worktree
  (bash killed plainly), select-pane in focus-content, the close-worktree popup failing, tmux failing to open
  the picker popup (e.g. a client gone since the last refresh; `dev new --client` then fails). The picker's
  own exit status is no error: it showed its error in the popup (`sidebar.TestActionOpenPicker`).
- `dev sidebar ensure` with a client reads `list-clients` (on tmux 3.8 `display-message -c` ignores the
  client for formats).
- Replacing a bash sidebar re-zooms its window, which the split creating dev's sidebar undoes at once, as
  toggle-sidebar.sh's create path did.

Resurrect and compact:

- Their messages start `⚠ dev resurrect:` and `⚠ dev compact:` (bash: `⚠ dev-resurrect:`, `⚠ compact.sh:`);
  the text kept in `@dev_resurrect_warnings` and `@dev_compact_warnings` is the same.
- tmux commands bash didn't check (list-windows, zoom, kill-pane, select-window, select-pane, compact's
  unmark) are warned (restore: `<session>: <step> failed: <tmux stderr>`, `resurrect.TestRestore`,
  `TestRestoreUncheckedFailure`); restore goes on with the next session and fails at the end. New warnings
  for an unknown state dir, last snapshot or default-shell, and for unreadable or unwritable state files; a
  failing `last_resurrect_file` warns and fails where bash exited 0.
- `save` writes a temp file and renames it, so a failed write keeps the old `.dev`; a failed rename leaves
  `<snapshot>.dev.tmp`, which prune doesn't remove. Positions come from one `list-panes -a`, taken in the
  session's own context: a sidebar window linked into another session keeps its index in its session
  (bash's `display-message -t %pane` could report the other session's index).
- `dev compact` finds the hook client's width and session in `list-clients`; bash's `display-message -c`
  formats for the best client of the current session on tmux 3.8, so a narrow client could read a wide one's
  width. A failing `list-clients` warns; an unknown client is a detached one and does nothing.
- `#{@project}` in `list-sessions -F` falls back to a global `@project` (`show-option -qv -t` did not);
  nothing sets one.
