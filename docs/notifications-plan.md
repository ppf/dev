# Plan: Claude notifications

Status: implemented 2026-10-15. Approved defaults: notify on done too; sound only on needs input; done previews the last message; in view = window current and not zoomed onto another pane (not pane-only). Spike: new tmux clients start `focused`, focus-out (`\e[O`) clears it.

## Goal
macOS notification when a Claude agent in a dev session needs input, finishes or fails, unless you're looking at it. A click switches tmux to that pane and raises the terminal.

## Trigger
Runs inside the existing `dev state` hook; no new hooks. The decision uses the stdin field `hook_event_name`.
| Event | Note |
|---|---|
| Notification (permission/elicitation, limited by the hook matcher) | subtitle = `title` (else "Needs input"), text = `message` (else "Needs input"), sound |
| Stop | subtitle "Done", text = `last_assistant_message` (else "Finished") |
| StopFailure | subtitle "Failed", text = `error` (else "Stopped on an error") |
| anything else | none; no tmux call or fork on the hot path |

Text is whitespace-collapsed, truncated to 100 runes, and loses a leading `[({"<-`. terminal-notifier reads arguments through NSUserDefaults, which parses the first as a property list and drops a value starting with `-`.

## Suppress when watching
One `c.Run` chain, only for the three events above: `display -p -t $pane` and `list-clients -F`, with formats from `tmux.Format[T]`. The pane fields are `@dev_notify`, `@dev_notify_activate`, `focus-events`, `session_name`, `window_active`, `pane_active`, `window_zoomed_flag`, `socket_path` and `pid`.
- In view means `focus-events` is on, the pane's window is current and not zoomed onto another pane, and a client on that session has the `focused` token. Otherwise notify.
- `tmux set -g @dev_notify off` (or per session) turns it off. A value other than on or off is an error.

## Delivery: `internal/notify` (agent-agnostic, `agent` package untouched)
- terminal-notifier comes from PATH. A missing one is a stderr error that says how to fix it.
- Args: `-title <session> -subtitle <subtitle> -message <text> -group dev-<server pid>-<pane_id>` (pane ids restart per server).
- `-execute` is `tmux.ShellJoin(<abs tmux>, -S, <socket>, switch-client, -t, <pane_id>)`. The pane target survives renames and picks the right pane.
- `-activate` comes from `@dev_notify_activate`, else `$__CFBundleIdentifier`. It's skipped when both are empty.
- The notifier starts with `Setpgid` and nil stdio, then `Release`; the hook doesn't wait. Its exit status and click failures go unseen (accepted).
- No `-remove` in v1: banners dismiss themselves, and `-group` replaces the older note.

## Machine setup (outside this repo)
- Install terminal-notifier 3.x (`brew install terminal-notifier`) and remove any older one earlier on PATH.
- Set Claude Code's `preferredNotifChannel` to `notifications_disabled`, or every prompt notifies twice.
- Keep `set -g focus-events on` in tmux.

## Tests
- **Unit** (`internal/notify`): the decide table (events, fallbacks, truncation, plist and dash stripping) and the buildArgs visibility table (focus, zoom, focus-events, on/off). Also covered: a bad `@dev_notify` value, exact arguments with quoting of a quote and a space in the socket path, no tmux query on non-notifying events, and `tmux.Format`.
- **Integration** (isolated `-L` sockets; the PATH holds only a tmux link, a fake notifier and /usr/bin:/bin). Cases:
  - An unwatched pane notifies; non-notifying events and off don't, with sentinels.
  - StopFailure, the activate override, and a missing notifier.
  - A focused client suppresses; focus-out notifies.
  - A notifier that sleeps doesn't hold the hook.
  - The click command switches the attached client to the pane.

## Release
Tag, then the terminal PR, then a live check: waiting, done, failed, click-through, watching suppression.
