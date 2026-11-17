package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/wait"
)

func newAgentWaitUntilCmd() *cobra.Command {
	var on string
	var cancel bool
	cmd := &cobra.Command{
		Use:   "wait-until <HH:MM|+duration> <reason>",
		Short: "Plan a wait for a known time (a limit reset): no stuck notes meanwhile, and dev wakes the agent then",
		Long: `Plan a wait: the Claude agent (this one, or --on's worker) waits until a time, for a reason, e.g.
dev agent wait-until 16:30 "Codex limit resets". End the turn after it: no background sleep is needed. It needs a
boss with its inbox on (dev boss) that started the worker: its previews wake it.

While it holds, its boss gets no stuck note for it while it rests or waits on background tasks, nor its idle PR
check or a repeat of its question; its card and sidebar row show "until 16:30 · Codex limit resets", a to do item on
it "waiting until 16:30", and dev agent list/get --json carry it as "wait". A turn ending on it sends the boss one
waiting note in place of finished. When the time comes, the boss's previews prompt it to continue (if it's idle with
an empty input box) and tell the boss with one resumed note; on a dialog or still working then, the wait just ends.
A dialog still reports as needs a decision, and a prompt ends the wait early.

The time is a local HH:MM (the next one: 00:30 at 23:00 is tomorrow's) or +<duration> (+90m, +1h30m), at most 4h
away. A new wait replaces the one held; --cancel ends it without a prompt. In a Claude desktop app session the
worker sets its own (no --on): the boss's board shows it and gets a wake due note at its time.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cancel {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var w wait.Wait
			if !cancel {
				until, err := wait.Parse(args[0], time.Now())
				if err != nil {
					return err
				}
				if w, err = wait.New(until, args[1]); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			pane := os.Getenv("TMUX_PANE")
			if on == "" && pane == "" {
				if !desktop.InApp() {
					return errors.New("not in a tmux agent pane: pass --on <worker>")
				}
				// The hook file the session's own hooks keep (desktop.RecordState)
				id := os.Getenv("CLAUDE_CODE_SESSION_ID")
				if id == "" {
					return errors.New("no CLAUDE_CODE_SESSION_ID: run it from the worker's own session")
				}
				dir, err := desktop.HookDir()
				if err != nil {
					return err
				}
				if err := desktop.SetWait(dir, id, w); err != nil {
					return err
				}
				fmt.Fprintln(out, said("this session", w))
				return nil
			}
			c := tmux.New()
			a, err := control.Resolve(c, cmp.Or(on, pane))
			if err != nil {
				return err
			}
			if cancel {
				if a.Wait == nil {
					return fmt.Errorf("%s has no planned wait", a.Session)
				}
				err = wait.Unset(c, a.Pane)
			} else {
				if a.Label != "Claude" {
					return fmt.Errorf("%s runs %s: only a Claude agent is woken", a.Session, a.Label)
				}
				if _, _, ok, err := inbox.BossOf(c, a.Pane); err != nil || !ok {
					return cmp.Or(err, fmt.Errorf("%s has no boss with its inbox on (dev boss): nothing would wake it", a.Session))
				}
				err = wait.Set(c, a.Pane, w)
			}
			if err == nil {
				fmt.Fprintln(out, said(a.Session, w))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&on, "on", "", "the worker (session name or pane id) that waits; default: this agent")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "end the planned wait without prompting the agent")
	return cmd
}

// said is what dev agent wait-until prints for whose wait w, the zero Wait for a cancelled one.
func said(who string, w wait.Wait) string {
	if !w.Held() {
		return who + ": planned wait ended"
	}
	return who + " waits " + w.Text(time.Now()) + "; dev wakes it then"
}
