package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/tmux"
)

func newHerdCmd() *cobra.Command {
	var previews bool
	var moveClients string
	cmd := &cobra.Command{
		Use:     "boss [project]",
		Aliases: []string{"herd"},
		Short:   "Open the coordinator view: a coordinator Claude next to live previews of its workers",
		Long: `Open or attach the boss session ("boss", or "boss-<project>"): a coordinator Claude started in the
project's main checkout (default: $HOME) that drives the other sessions with the dev-agents skill, next to
live previews of the workers it started.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			if moveClients != "" {
				return herd.MoveClients(c, moveClients)
			}
			if previews {
				pane := os.Getenv("TMUX_PANE")
				if pane == "" {
					return errors.New("Must run inside tmux")
				}
				return herd.Run(c, pane)
			}
			if len(args) == 0 {
				return herd.Open(c, "", os.Getenv("HOME"))
			}
			projects, _, err := loadProjects(cmd)
			if err != nil {
				return err
			}
			p, ok := config.Find(projects, args[0])
			if !ok {
				return errors.New("project " + args[0] + " not found")
			}
			dir, err := p.Dir()
			if err != nil {
				return err
			}
			return herd.Open(c, p.Name, dir)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "run the previews pane (inside the boss session)")
	_ = cmd.Flags().MarkHidden("previews")
	cmd.Flags().StringVar(&moveClients, "move-clients", "", "move the clients off the ending boss session `id`")
	_ = cmd.Flags().MarkHidden("move-clients")
	return cmd
}
