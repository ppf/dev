package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/notify"
	"github.com/ppf/dev/internal/tmux"
)

var states = []string{agent.Working, agent.Waiting, agent.Idle, agent.Clear, agent.ToolFailed}

func newStateCmd() *cobra.Command {
	var codex bool
	cmd := &cobra.Command{
		Use:   "state <working|waiting|idle|clear|tool-failed>",
		Short: "Claude, Codex or Grok hook: record the agent state on its tmux pane",
		Args: cobra.MatchAll(cobra.ExactArgs(1), func(_ *cobra.Command, args []string) error {
			if !slices.Contains(states, args[0]) {
				return fmt.Errorf("unknown state %q, want one of %v", args[0], states)
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" && !codex {
				return nil
			}
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			state, ok, err := agent.Resolve(args[0], input)
			if err != nil {
				// A failing hook would show as an error on every Claude event; report and leave the pane alone.
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
				return nil
			}
			// Grok sets GROK_HOOK_EVENT on every hook, also on the Claude hooks it imports from ~/.claude
			grok := os.Getenv("GROK_HOOK_EVENT") != ""
			if !ok || grok && agent.GrokSkip(input) {
				// A subagent's tool call (or Grok's session end): progress for the boss inbox, though not the pane's state
				if pane != "" && !codex {
					if err := agent.Touch(tmux.New(), pane); err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					}
				}
				return nil
			}
			c := tmux.New()
			hook := agent.ParseHook(input)
			if grok {
				stale, err := agent.GrokStale(c, pane, hook)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if stale {
					return nil
				}
			}
			// Restore resumes Claude conversations only: a Codex or Grok id would make it type `claude --resume`
			var session agent.Session
			if !codex && !grok {
				session = agent.HookSession(input)
			}
			if codex {
				if pane, ok, err = agent.CodexPane(c, input); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if !ok {
					return nil
				}
			}
			var out string
			busy := false
			// Grok reads a Stop hook's stdout as a reply, and is never a boss
			if !codex && !grok {
				// Before Record: a Stop blocked with inbox notes keeps the boss working
				if out, busy, err = inbox.BossHook(c, pane, input); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
				}
				// So does a worker's Stop waiting on background tasks: Claude carries on by itself when they end,
				// and @agent_since stays put, so a long wait still makes a stuck note. A coordinator stays idle,
				// as Deliver pastes only into an idle boss and its background waits can outlast its workers
				if !busy && agent.Backgrounded(input) {
					_, boss, err := inbox.Self(c, pane)
					if err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
					}
					busy = !boss
				}
				if busy {
					state = agent.Working
				}
			}
			recErr := agent.Record(c, pane, state, session, hook)
			if out != "" {
				// The notes are already out of the inbox: print them even if Record failed. Claude reads a
				// hook's stdout as JSON, so nothing else may go there
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			if recErr != nil {
				return recErr
			}
			if busy {
				// A blocked or backgrounded Stop isn't a finished turn, for notify or for a boss above this one
				return nil
			}
			if err := notify.Run(c, pane, input, os.Getenv("__CFBundleIdentifier")); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
			}
			if err := inbox.WorkerHook(c, control.RealClock, pane, input); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&codex, "codex", false, "Codex hook: find the pane by the hook's cwd instead of TMUX_PANE")
	return cmd
}
