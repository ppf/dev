# Plan: `dev` Go CLI migration (parity, phased)

Status: planned 2026-10-14, build later. Revised after a subagent review against the bash source.
Decisions: new repo `ppf/dev` · strict parity first, features later · `projects.toml` with one-time import · public repo, `go install @tag` · runtime stays tmux (own multiplexer, herdr and zellij considered and rejected 2026-10-14).

## Goal
Replace the `dev` bash glue (7 session scripts plus the Claude hook, ~1,490 lines) with one tested Go binary on top of tmux. Behaviour must match exactly; each phase ships alone and has a rollback.

**Non-goals (later):** Codex state, notifications, cost info, PR/Linear links, layouts beyond today's default, a `dev open <name> <worktree>` form, shell completion, CI, lint.
**Stays as is:** tmux, `.tmux.conf` popups and keys (`g d f p P N e`), tmux-resurrect/continuum, Claude hooks config shape.

## Architecture
- **Repo:** `ppf/dev`, binary `dev`. Stack copied from `dbmanager`: Cobra, Bubble Tea, bubbles, lipgloss, plus `BurntSushi/toml`. Reuse `dbmanager/internal/tmux/popup.go` (`MaybePopup`: env guard, self re-exec, exit code passthrough).
- **Install:** public repo, module `github.com/ppf/dev` (must match the repo path, unlike dbm's `github.com/storm/dbm`). Terminal `manifest.json` gets entry `dev-cli` with `post_export: go install github.com/ppf/dev@vX.Y.Z`; rollback = previous tag. Hooks and tmux bindings call the binary by absolute path (`$HOME/go/bin/dev`), never through PATH. Add `go` to `brew/packages.txt`. Nothing personal in the repo — paths/projects live in local `projects.toml`.
- **Packages (`internal/`):**
  | Package | Owns |
  |---|---|
  | `tmux` | exec wrapper with a mandatory socket override for tests (`-L`, plus child `TMUX` env); formats joined with `\x1f` and parsed into structs; session-option targets always `=name:` |
  | `config` | `~/.config/dev/projects.toml` load/validate; `import` from `projects.conf` |
  | `agent` | `@agent_state` / `@agent_cmd` write (one chained tmux call, `@agent_cmd` set with `set-option -pF '#{pane_current_command}'`), stale check, subagent and interrupt rules |
  | `worktree` | create / reuse / existing-branch; prune; base from `origin/HEAD` with warnings; `.worktreeinclude` via the same git → check-ignore → `rsync -a -r` pipeline (shelled out), per-stage status check, cleanup |
  | `open` | session creation and the fixed default layout (no separate layout package) |
  | `sidebar` | Bubble Tea model, view, input, actions |
  | `picker` | Bubble Tea list with filter, project then worktree (replaces fzf) |
  | `resurrect` | save/prune/pre-restore/restore; resurrect paths taken by sourcing its `helpers.sh` (shell out), not reimplemented |
- **Shared tmux state stays compatible** so bash and Go can coexist:
  - `@sidebar_pane`, `@sidebar_selected_session`, `@sidebar_hidden`, `@project`, `@agent_*`, `@dev_restored`, `@dev_resurrect_warnings`.
  - `@sidebar_session_order` keeps being written (grouped `(group, name)` byte order); `dev goto` and kill-replacement compute it with the same function.
  - `@sidebar_row_map` is dropped once no bash sidebar remains (it only feeds bash click handling).
  - `status-right` reads `@agent_*`; the format is unchanged.
- **Client choice:** the sidebar reads its own keys, so tmux doesn't say which client pressed them. It acts on the client viewing its session; if several are, the most recently active (`#{client_activity}`). Exact with one attached client, like bash. Bindings that already pass `#{client_tty}` (`goto`, `new`) keep passing it.
- **Focus and size come from the same per-tick `list-panes` call** (`pane_active`, `pane_width`), exactly like bash, so there's no `FocusMsg` spike. Branch lookups are cached per path, which is the real cost today (git and perl forks per tick).

## Parity checklist (`docs/parity.md` in the new repo)
Every item is verified side by side with bash before its phase ships.

**Picker / `dev` (`select.sh`, `project.sh`)**
- `dev` opens the picker. Inside tmux it re-execs into an 80%×60% rounded popup, guarded by `SIDEBAR_SELECT_IN_POPUP`; `dev <name>` never opens a popup.
- `dev <name>` is positional (Cobra `ArbitraryArgs`; names clashing with subcommands are rejected at config load). An unknown name prints the available projects and exits 1. Existing sessions are attached, not recreated. The picker path always forces a new session (`-2`, `-3` suffix).
- `dev list` prints `name\tdescription\tpath`.
- Project rows show `name description` with a `Path:` preview.
- Worktree step:
  - Skipped silently when the path isn't a git repo.
  - Rows: `main repo`, `+ new worktree`, `wt: <nested/name>`, taken from `worktree list --porcelain`.
  - A typed name that matches nothing creates a worktree; `+ new` with an empty query asks for a name; Esc exits 0; the list wraps.
  - Name filtered to `A-Za-z0-9._/-`.
- Worktree creation:
  - Prune first.
  - An existing branch is checked out as-is with a notice; otherwise fetch, `remote set-head --auto`, branch from `origin/HEAD`, falling back to HEAD with a warning.
  - Warnings are echoed and sent via `display-message`; a failure waits for Enter if there's a TTY.
  - `.worktreeinclude` is copied only on fresh creation. On failure the worktree is removed, the branch is deleted only if it was just created, and the run fails.
- Session name is `name-${wt//\//-}`; `@project` is the un-suffixed name.
- Size chain: `client_width/height` → `stty size </dev/tty` → `COLUMNS/LINES` → `tput` → 80×24.
- First `dev` per server: `start-server ; set @dev_restored 1 ; run-shell restore.sh`, then print and unset `@dev_resurrect_warnings` (pause on a TTY). This runs before the force-new suffix and the has-session check.
- Terminal title `\033]2;<session>\007`.
- Layout (assumes `base-index 1`):
  - Window 1 `Agent`: `send-keys claude Enter` (in a shell); lazygit `status --screen-mode=half` in a `-h -l 50%` split; a terminal below it; sidebar `-hb -l 40` running the binary with `exec`.
  - Window 2 `Files`: `nvim .` with a right column `-l 80` split in two.
  - Finish on window 1 with focus on the claude pane.

**Sidebar (`sidebar.sh`, `sidebar-action.sh`, `toggle-sidebar.sh`)**
- Agent pane (deliberate fix vs bash): the window-1 non-sidebar pane whose `pane_current_command == @agent_cmd` (hook-registered Claude), else one whose command is `codex*`; otherwise "no agent". Bash labelled any non-shell pane (lazygit, nvim) as Claude after claude exited. The branch is read only when an agent pane exists (`branch --show-current`, else short HEAD).
- State is trusted only when `@agent_cmd == pane_current_command`.
- Labels:
  - Codex title `a | b` → `b on a` (kept).
  - Title cleanup: strip ANSI/control characters, drop a leading symbol plus space, blank generic titles.
- Grouping by `@project`, falling back to `${name%-[0-9]*}`; a rule line per group; a blank line between groups.
- View:
  - `btn_col = width-4`, buttons hidden below 12 columns.
  - Selected row gets a full-width background and a red `×`; the current session is bold; a waiting line is red; a `○` "no agent" line is faint.
  - "No sessions" empty state; the hint row only while focused, with the exact text.
  - `…` truncation by display width.
- Selection sync: a background sidebar adopts the client's current session only when it isn't the active pane and its session is the client's session; fallbacks as in bash.
- Keys:
  - `j/k/↑/↓` wrap.
  - `⏎` switches; on the current session it focuses the content pane instead.
  - `q` focuses the first non-sidebar pane of the active window.
  - `d` / `+` open the picker popup.
  - `x` kills the selected session; `r` renames it via tmux `command-prompt -I <name>`.
- Mouse:
  - Clicking any line selects the row at or above it.
  - The `×` zone `[w-4, w-1)` kills that row's session; `+` on row 0 opens the picker.
  - Clicking the current session focuses its content pane.
- Kill:
  - Refuses the last session.
  - When killing the current session, switches to the first other session in order, then kills.
  - For a session in a worktree no other session uses, the 70×8 popup with `y` (close + remove worktree) / `n` (close, keep) / Esc. The popup must own the work, because the sidebar dies with its session: hidden subcommand `dev sidebar close-worktree …`.
- Ensure / toggle:
  - `prefix+S` toggles all sidebars and records `@sidebar_hidden`.
  - Ensure does nothing while hidden.
  - Ensure creates the sidebar in the target session's active window and gives focus back to the pane it came from.
  - After a switch, the target session's sidebar is focused and the selection set.
- `prefix+1..9` goto N (in session order), `prefix+0` new.

**Resurrect (`dev-resurrect.sh`)**
- Snapshot-adjacent `.dev` state: `save <snapshot>` (fails loudly, keeps the old file); `prune` of orphans.
- `pre-restore` writes the live list to `$XDG_STATE_HOME/dev/live-before-restore`.
- `restore`:
  - Skips sessions that were live before.
  - Sets `@project`.
  - Checks that the slot runs `basename(default-shell)` before killing it.
  - Preserves the active window and focus.
  - Warns via display-message and appends to `@dev_resurrect_warnings`.

**Compact mode (`compact.sh`, ppf/terminal tmux/tests/compact-test.sh)**
- Synchronous client hooks (attached/resized/active/session-changed) pass `#{hook_client}`; width and session are looked up from that client.
- Below 140 cols: zoom the claude pane in `Agent` and the nvim pane in `Files` of that session only; marks `@dev_compact_zoomed` with the pane id. Zooms once per narrow period (a manual unzoom stays); skips single-pane windows.
- Wide: unzoom only if still zoomed on the marked pane; always clear the mark.
- Sidebar ensure doesn't focus a sidebar hidden by zoom; hiding sidebars re-zooms a window the kill unzoomed; restore unzooms restored dev windows.
- Problems via display-message to the hook client + `@dev_compact_warnings`.

**Hook (`tmux-agent-state.sh`)**
- Gated on `TMUX_PANE`; no-op without it.
- Hook stdin parsed as JSON (deliberate fix vs bash substring match): skip working/tool-failed when the top-level `agent_id` field is present; `is_interrupt: true` → idle. Malformed JSON → no-op with stderr message (Claude shows it, non-blocking).

## Phases
Each phase ends with: `go test ./...` green; the local integration suite green (build tag `integration`, isolated `-L` server, temp resurrect dir and `XDG_STATE_HOME`; needs real tmux 3.8 and the plugins, so local only, no CI); the parity items for that phase checked; tag; manifest/binding switch. Bash files are removed only in Phase 4.

### Phase 0: Scaffold (1 day)
- **0a** (parallel) Repo, Cobra root, `go test` setup.
- **0b** (parallel) `internal/tmux` with tests: `\x1f` parsing, empty middle fields, `=name:` targets, socket override, error wrapping.
- **0c** (parallel) Integration harness. Port `tmux/tests/restore-test.sh` from ppf/terminal and the `.worktreeinclude` temp-repo cases from ppf/terminal#8. The harness refuses to run without a socket override.

**Done when:** the harness creates and kills only its own server; the unit tests pass.

The bash reference test lives in ppf/terminal: `tmux/tests/restore-test.sh`.

### Phase 1: `dev state` (0.5 day)
- **1a** `internal/agent` + `dev state`, with the same rules as the hook.
- **1b** Switch `claude/settings.json` hooks to `$HOME/go/bin/dev state …`; export; live-test idle → working → idle, subagent, waiting, crash leftover. Measure startup latency (target < 20 ms; keep `dev state` free of heavy `init()`).

**Rollback:** settings back to the `.sh` (kept until Phase 4).

### Phase 2: Picker + `dev <name>` (2 days)
Phase 2 comes before the sidebar, so the Go sidebar can open the Go picker.
- **2a** (parallel) `internal/config` + `dev import-projects` + `dev list`.
- **2b** (parallel) `internal/worktree` + tests (temp repos).
- **2c** (needs 2a) `internal/open`: size chain, force-new suffix, first-run restore, title, layout. In this phase it still spawns bash `sidebar.sh` in the layout.
- **2d** (needs 2a, 2b) `internal/picker` + the popup re-exec.
- **2e** Cutover: zsh alias `dev` → `$HOME/go/bin/dev`; `sidebar-action.sh` `new` and `.tmux.conf` popups call `dev` instead of `select.sh`.

**Rollback:** alias back to `select.sh`.

### Phase 3: Sidebar (3–4 days)
- **3a** Model: sessions, agent detection, titles, grouping, order (writes `@sidebar_session_order`), selection sync, branch cache.
- **3b** (needs 3a) View to the pixel spec above.
- **3c** (needs 3a) Input and actions: keys, mouse zones, switch / focus-content, kill (replacement and last-session refusal), `dev sidebar close-worktree` popup, rename via command-prompt, picker via `dev`.
- **3d** (needs 3a) `dev sidebar toggle|ensure`, `dev goto N`, `dev new`.
- **3e** Cutover (two files, so rollback = revert two files):
  - `toggle-sidebar.sh` becomes `exec $HOME/go/bin/dev sidebar ensure|toggle "$@"`, so every remaining bash caller (`project.sh`, `dev-resurrect.sh`, `sidebar-action.sh`) follows.
  - `install-sidebar-keys.sh` becomes an unbind script: `unbind -T root j k Up Down Enter q d x r`, unbind the `sidebar` table, restore `bind -n MouseDown1Pane select-pane -t = \; send-keys -M`.
  - `.tmux.conf`: `prefix+S` / `1..9` / `0` call `dev`.
  - `ensure` detects a running bash sidebar (`#{pane_start_command}` contains `sidebar.sh`) and replaces it.
  - `internal/open` spawns `dev sidebar`.

**Rollback:** revert those two scripts and `.tmux.conf`, then rerun the original `install-sidebar-keys.sh`.

### Phase 4: Resurrect + removal (1 day)
- **4a** `dev resurrect …` and `dev compact`; switch the `.tmux.conf` hooks to the absolute binary path; run the full restore suite (cold start, hidden sidebars, prune, focus).
- **4b** Delete the 8 session scripts plus `tmux-agent-state.sh` and both `tmux/tests/*-test.sh`, their manifest entries, the stale `tmux/sessions/README.md`; replace `projects.conf.example` with `projects.toml.example` (`projects.toml` itself stays local, untracked); `./export.sh`; drift-check.

**Done when:** an export into a temp `$HOME` installs everything and `dev` works end to end.

**Total: about 7.5–9.5 days**, front-loaded by the sidebar.

## Risks
| Risk | Mitigation |
|---|---|
| Leftover root-table key bindings swallow the Go sidebar's keys | The 3e unbind script + `ensure` replacing bash sidebars; verified in the parity checklist |
| Kill-worktree dies with its own session | The work runs in a client-owned popup subcommand, same as bash |
| Tests touch the user's tmux | The wrapper requires a socket override under test; the harness asserts it |
| Hook latency or a missing binary breaks Claude hooks | No-op outside tmux; latency measured in Phase 1; `.sh` fallback kept until Phase 4 |

## Unresolved questions
None. Answered 2026-10-14: JSON hook parsing; keep the Codex title rewrite, fix the lazygit-as-Claude quirk; `projects.toml` untracked with only an example; restore test committed.
