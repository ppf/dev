package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/tmux"
)

func newQuestionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "question",
		Short: "Claude hook (PermissionRequest, PreToolUse of AskUserQuestion): record the dialog's question on its tmux pane (or a desktop app session's hook file)",
		Long: `Claude hook for PermissionRequest and PreToolUse (matcher AskUserQuestion): records what the dialog asks
(AskUserQuestion's questions, or a permission's tool with its command, file or URL) on the pane (@agent_question),
so dev boss's picker and inbox decision notes can show it; in a desktop app session, in its hook file for dev
desktop board (an AskUserQuestion also as a decision). It never decides: nothing goes to stdout, where Claude reads a decision.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" && !desktop.InApp() {
				return nil
			}
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if pane == "" {
				dir, err := desktop.HookDir()
				if err == nil {
					err = desktop.RecordQuestion(dir, input, time.Now())
				}
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev question: "+err.Error())
				}
				return nil
			}
			q, ok := agent.HookQuestion(input)
			if !ok {
				return nil
			}
			c := tmux.New()
			nested, err := agent.Nested(c, pane, os.Getenv("CLAUDE_PID"))
			if err == nil && !nested {
				err = agent.RecordQuestion(c, pane, q)
			}
			if err != nil {
				// A failing hook would show as an error on every dialog
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev question: "+err.Error())
			}
			return nil
		},
	}
}
