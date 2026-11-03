package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/ptywrap"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func newPtyWrapCmd() *cobra.Command {
	var closeOnExit bool
	cmd := &cobra.Command{
		Use:   "pty-wrap -- <command> [args...]",
		Short: "Run a command on its own pty, passing its kitty graphics through tmux (dev starts claude with it)",
		Long: `Run <command> on its own pty and wrap each kitty graphics APC (ESC _G … ESC \) in its output in tmux
passthrough, so Claude Code's images reach the terminal instead of being dropped by tmux (and set as the
pane title). Needs tmux's allow-passthrough on. Outside tmux, or without a terminal, <command> just runs.
Exits with <command>'s status. --close-on-exit: its clean exit (status 0) closes this pane's session for good,
as the sidebar's × does; a crash or a signal leaves it.`,
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var exited func(int)
			if pane := os.Getenv("TMUX_PANE"); closeOnExit && pane != "" {
				exited = func(status int) {
					if status != 0 {
						return
					}
					// The session's kill may end this process before Run's own exit
					if err := sidebar.Exited(tmux.New(), pane); err != nil {
						fmt.Fprintln(os.Stderr, "❌ "+err.Error())
					}
				}
			}
			return ptywrap.Run(args, exited)
		},
	}
	cmd.Flags().BoolVar(&closeOnExit, "close-on-exit", false, "close the session for good when <command> exits cleanly")
	return cmd
}
