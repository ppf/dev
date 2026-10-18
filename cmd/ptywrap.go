package cmd

import (
	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/ptywrap"
)

func newPtyWrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pty-wrap -- <command> [args...]",
		Short: "Run a command on its own pty, passing its kitty graphics through tmux (dev starts claude with it)",
		Long: `Run <command> on its own pty and wrap each kitty graphics APC (ESC _G … ESC \) in its output in tmux
passthrough, so Claude Code's images reach the terminal instead of being dropped by tmux (and set as the
pane title). Needs tmux's allow-passthrough on. Outside tmux, or without a terminal, <command> just runs.
Exits with <command>'s status.`,
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return ptywrap.Run(args)
		},
	}
}
