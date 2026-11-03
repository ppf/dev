package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func newSidebarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sidebar",
		Short: "Session sidebar (runs in its own tmux pane)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" {
				return errors.New("Must run inside tmux")
			}
			return sidebar.Run(tmux.New(), pane)
		},
	}
	cmd.AddCommand(newSidebarToggleCmd(), newSidebarEnsureCmd(), newCloseWorktreeCmd(), newExitedCmd())
	return cmd
}

func newSidebarToggleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "toggle",
		Short: "Hide every sidebar, or show the current session's (prefix+S)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c := tmux.New()
			from, err := sidebar.ResolveOrigin(c, "")
			if err != nil {
				return err
			}
			return sidebar.Toggle(c, from)
		},
	}
}

func newSidebarEnsureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ensure <session>",
		Short: "Show a session's sidebar unless sidebars are hidden",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c := tmux.New()
			// Like bash's 2>/dev/null: without an origin only the focus give-back is skipped.
			from, _ := sidebar.ResolveOrigin(c, "")
			return sidebar.Ensure(c, args[0], from)
		},
	}
}

func newCloseWorktreeCmd() *cobra.Command {
	var a sidebar.CloseArgs
	cmd := &cobra.Command{
		Use:    "close-worktree <current> <target> <main-root> <worktree>",
		Short:  "Close a worktree session and ask about its worktree (runs in the sidebar's popup)",
		Hidden: true,
		Args:   cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.Current, a.Target, a.MainRoot, a.Worktree = args[0], args[1], args[2], args[3]
			return sidebar.CloseWorktree(tmux.New(), a, cmd.OutOrStdout(), func() (byte, error) {
				return sidebar.ReadKey(os.Stdin)
			})
		},
	}
	cmd.Flags().StringVar(&a.Client, "client", "", "client tty to act on (default: tmux's current client)")
	return cmd
}

func newExitedCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "exited <status> <target>",
		Short:  "After a clean exit (status 0) of a pane: close target's session (target: a pane or session id) for good, like the sidebar's ×",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			// A crash or a server going down leaves the session for the next restore
			if args[0] != "0" {
				return nil
			}
			return sidebar.Exited(tmux.New(), args[1])
		},
	}
}
