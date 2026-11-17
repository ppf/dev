package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/tmux"
)

func newHerdCmd() *cobra.Command {
	var previews, background bool
	var timeout time.Duration
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
restarts it.
--background creates the boss without attaching or switching a client, marks it started by the boss session
dev runs in, prints its name and waits until its Claude registered (as dev agent start does); an existing
boss is only printed.`,
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
			if background && len(args) == 0 {
				return errors.New("--background needs a project: dev boss <project> --background")
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
			// A background boss's stdout is its name only
			restoreOut := cmd.OutOrStdout()
			if background {
				restoreOut = cmd.ErrOrStderr()
			}
			if err := open.RestoreOnce(c, os.Getenv("HOME"), restoreOut); err != nil {
				return err
			}
			if background {
				return openBackground(cmd, c, p.Name, dir, start, timeout)
			}
			return herd.Open(c, p.Name, dir, start)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "run the previews pane (inside the boss session)")
	_ = cmd.Flags().MarkHidden("previews")
	cmd.Flags().BoolVar(&background, "background", false, "create the boss detached and wait until its Claude registered")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "with --background, give up when no Claude registered within this")
	addStartFlags(cmd, &start, "boss's coordinator")
	return cmd
}

// openBackground is dev boss <project> --background: the boss exists detached, started by the boss session this
// runs in, and is printed once its Claude registered.
func openBackground(cmd *cobra.Command, c *tmux.Client, project, dir string, start agent.Start, timeout time.Duration) error {
	session, created, err := herd.OpenBackground(c, project, dir, start)
	if err != nil {
		return err
	}
	if created {
		if pane := os.Getenv("TMUX_PANE"); pane != "" && herd.IsBossPane(c, pane) {
			if err := herd.MarkStartedBy(c, session, pane, false); err != nil {
				return err
			}
		}
		if _, err := control.AwaitAgent(c, control.RealClock, session, timeout); err != nil {
			return err
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), session)
	return nil
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
