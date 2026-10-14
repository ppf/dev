package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/resurrect"
	"github.com/ppf/dev/internal/tmux"
)

func newResurrectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resurrect",
		Short: "tmux-resurrect hooks: save and rebuild dev session state",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("usage: dev resurrect save <snapshot>|prune|pre-restore|restore")
		},
	}
	hook := func(use, short string, args cobra.PositionalArgs, run func(c *tmux.Client, args []string) error) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  args,
			RunE: func(_ *cobra.Command, a []string) error {
				return shown(run(tmux.New(), a))
			},
		}
	}
	cmd.AddCommand(
		hook("save <snapshot>", "post-save-layout: write <snapshot>.dev", cobra.ExactArgs(1),
			func(c *tmux.Client, a []string) error { return resurrect.Save(c, a[0]) }),
		hook("prune", "post-save-all: delete .dev files of deleted snapshots", cobra.NoArgs,
			func(c *tmux.Client, _ []string) error { return resurrect.Prune(c) }),
		hook("pre-restore", "pre-restore-all: record the live sessions", cobra.NoArgs,
			func(c *tmux.Client, _ []string) error { return resurrect.PreRestore(c) }),
		hook("restore", "post-restore-all: rebuild @project and sidebars", cobra.NoArgs,
			func(c *tmux.Client, _ []string) error { return resurrect.Restore(c) }),
	)
	return cmd
}

// shown marks a hook's error as already reported in tmux: printing it too would put hook output in
// front of the user, which bash never did.
func shown(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", open.ErrShown, err)
}
