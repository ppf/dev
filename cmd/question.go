package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

func newQuestionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "question",
		Short: "Claude hook (PermissionRequest, PreToolUse of AskUserQuestion): record the dialog's question on its tmux pane",
		Long: `Claude hook for PermissionRequest and PreToolUse (matcher AskUserQuestion): records what the dialog asks
(AskUserQuestion's questions, or a permission's tool with its command, file or URL) on the pane (@agent_question),
so dev boss's picker and inbox decision notes can show it. It never decides: nothing goes to stdout, where Claude reads a decision.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" {
				return nil
			}
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			q, ok := agent.HookQuestion(input)
			if !ok {
				return nil
			}
			if err := agent.RecordQuestion(tmux.New(), pane, q); err != nil {
				// A failing hook would show as an error on every dialog
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev question: "+err.Error())
			}
			return nil
		},
	}
}
