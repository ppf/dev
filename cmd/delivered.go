package cmd

import (
	"cmp"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
)

func newDeliveredCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "delivered <worktree>",
		Short: "Check a worker's worktree is delivered (dev agent close's checks, without tmux): exit 0 if so",
		Long: `Check a worker's worktree under .claude/worktrees (e.g. a desktop-app session's) is delivered, as dev agent
close checks it before closing, without its tmux checks and without touching the worktree: its HEAD in origin's
default branch (fetched first; without an origin remote, the main checkout's branch), or the head commit of a PR/MR
that is open, or merged into that branch, and no changes to tracked files. Exit status 0 when delivered, else 1 with the failed check.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := control.Delivered(args[0])
			out := cmd.OutOrStdout()
			if asJSON {
				return cmp.Or(printJSON(out, d), err)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "delivered: %s (%s)\n", d.Head, d.Reason)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object: delivered, head, reason, pr")
	return cmd
}
