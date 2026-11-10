package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/tmux"
)

func newHerdCmd() *cobra.Command {
	var previews bool
	var start agent.Start
	cmd := &cobra.Command{
		Use:     "boss [project]",
		Aliases: []string{"herd"},
		Short:   "Open the coordinator view: a coordinator Claude next to live previews of its workers",
		Long: `Open or attach the boss session ("boss", or "boss-<project>"): a coordinator Claude started in the
project's main checkout (default: $HOME) that drives the other sessions with the dev-agents skill, next to
live previews of the workers it started. --model and --effort start a new boss's coordinator with them (with
a boss that exists they are an error unless they equal what it was started with); it keeps them when dev
restarts it.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkStart(cmd, start); err != nil {
				return err
			}
			c := tmux.New()
			if previews {
				pane := os.Getenv("TMUX_PANE")
				if pane == "" {
					return errors.New("Must run inside tmux")
				}
				return herd.Run(c, pane)
			}
			project, dir := "", os.Getenv("HOME")
			if len(args) == 1 {
				projects, _, err := loadProjects(cmd)
				if err != nil {
					return err
				}
				p, ok := config.Find(projects, args[0])
				if !ok {
					return errors.New("project " + args[0] + " not found")
				}
				if dir, err = p.Dir(); err != nil {
					return err
				}
				project = p.Name
			}
			// After the first-run restore, so a saved boss is attached rather than replaced
			if err := open.RestoreOnce(c, os.Getenv("HOME"), cmd.OutOrStdout()); err != nil {
				return err
			}
			return herd.Open(c, project, dir, start)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "run the previews pane (inside the boss session)")
	_ = cmd.Flags().MarkHidden("previews")
	addStartFlags(cmd, &start, "boss's coordinator")
	return cmd
}
