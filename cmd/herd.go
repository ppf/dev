package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
		Long: `Open or attach the project's boss session ("boss-<project>"): a coordinator Claude started in the
project's main checkout that drives the other sessions with the dev-agents skill, next to live previews of
the workers it started. Without a project it opens the project picker (as bare dev); with no terminal it
fails, listing the projects. --model and --effort start a new boss's coordinator with them (with
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
			if len(args) == 0 {
				if !hasTerminal() {
					return noTerminal(cmd)
				}
				return runPicker(cmd, start)
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
			// After the first-run restore, so a saved boss is attached rather than replaced
			if err := open.RestoreOnce(c, os.Getenv("HOME"), cmd.OutOrStdout()); err != nil {
				return err
			}
			return herd.Open(c, p.Name, dir, start)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "run the previews pane (inside the boss session)")
	_ = cmd.Flags().MarkHidden("previews")
	addStartFlags(cmd, &start, "boss's coordinator")
	return cmd
}

// hasTerminal reports whether the picker can run: stdin and stdout are a terminal.
var hasTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// noTerminal is bare dev boss's error without a terminal: never a boss nobody picked.
func noTerminal(cmd *cobra.Command) error {
	projects, _, err := loadProjects(cmd)
	if err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	fmt.Fprintln(stderr, "❌ No terminal for the project picker: run dev boss <project>")
	printAvailable(stderr, projects)
	return fmt.Errorf("%w: dev boss without a project needs a terminal", open.ErrShown)
}
