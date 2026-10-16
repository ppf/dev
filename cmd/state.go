package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/notify"
	"github.com/ppf/dev/internal/tmux"
)

var states = []string{agent.Working, agent.Waiting, agent.Idle, agent.Clear, agent.ToolFailed}

func newStateCmd() *cobra.Command {
	var codex bool
	cmd := &cobra.Command{
		Use:   "state <working|waiting|idle|clear|tool-failed>",
		Short: "Claude or Codex hook: record the agent state on its tmux pane",
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
			if !ok {
				return nil
			}
			c := tmux.New()
			session := agent.HookSession(input)
			if codex {
				if pane, ok, err = agent.CodexPane(c, input); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if !ok {
					return nil
				}
				// Restore resumes Claude conversations only; a Codex id would make it type `claude --resume`
				session = agent.Session{}
			}
			if err := agent.Record(c, pane, state, session, agent.ParseHook(input)); err != nil {
				return err
			}
			if err := notify.Run(c, pane, input, os.Getenv("__CFBundleIdentifier")); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&codex, "codex", false, "Codex hook: find the pane by the hook's cwd instead of TMUX_PANE")
	return cmd
}
