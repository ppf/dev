package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func newStatuslineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "statusline [-- <command> [args...]]",
		Short: "Claude status line: record the session cost on its tmux pane, then run <command> on the same input",
		Long: `Claude status line: records cost.total_cost_usd from the status line input on the pane (@agent_cost)
for the sidebar and dev agent, then runs <command> with the same input and passes its output through.
Without a command it prints the cost.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The status line must still render, so failures are reported and the wrapped command runs
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev statusline: "+err.Error())
			}
			usd, ok := agent.StatusCost(input)
			if pane := os.Getenv("TMUX_PANE"); pane != "" {
				c := tmux.New()
				if ok {
					err = agent.RecordCost(c, pane, usd)
				} else {
					// No cost in the input: the last one would show as current
					err = agent.ClearCost(c, pane)
				}
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev statusline: "+err.Error())
				}
			}
			if len(args) == 0 {
				if ok {
					fmt.Fprintln(cmd.OutOrStdout(), sidebar.FormatCost(usd))
				}
				return nil
			}
			wrapped := exec.Command(args[0], args[1:]...)
			wrapped.Stdin = bytes.NewReader(input)
			wrapped.Stdout, wrapped.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := wrapped.Run(); err != nil {
				if _, exited := errors.AsType[*exec.ExitError](err); exited {
					// Its own stderr already said why
					return open.ErrShown
				}
				return err
			}
			return nil
		},
	}
	// The wrapped command's own flags are its arguments, with or without "--"
	cmd.Flags().SetInterspersed(false)
	return cmd
}
