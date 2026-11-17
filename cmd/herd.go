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
	var previews, background, adopt bool
	var timeout time.Duration
	var start agent.Start
	cmd := &cobra.Command{
		Use:     "boss [project]",
		Aliases: []string{"herd"},
		Short:   "Open the coordinator view: a coordinator Claude next to live previews of its workers",
		Long: `Open or attach the project's boss session ("boss-<project>"): a coordinator Claude started in the
project's main checkout that drives the other sessions with the dev-agents skill, next to live previews of
the workers it started. Without a project it opens or attaches the manager (session "manager", in
~/.config/dev/manager), a boss whose workers are bosses, on sonnet and medium effort unless --model or
--effort say otherwise; with no terminal (stdin or stdout not one) it starts the manager detached when
missing, waits until its Claude registered, and prints how to reach it. --model and --effort start a new
boss's coordinator with them (with a boss that exists they are an error unless they equal what it was started
with); it keeps them when dev restarts it.
--background creates the boss without attaching or switching a client, marks it started by the boss session
dev runs in, prints its name and waits until its Claude registered (as dev agent start does); an existing
boss is only printed. With --adopt, run in the manager's pane, an existing boss no live session started becomes
the manager's as if it had started it: its news goes up to the manager, it shows in the manager's --mine list and
previews, and it keeps its workers and list.`,
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
			if adopt && !background {
				return errors.New("--adopt needs --background: dev boss <project> --background --adopt")
			}
			if background && len(args) == 0 {
				return errors.New("--background needs a project: dev boss <project> --background")
			}
			if len(args) == 0 {
				return openManager(cmd, c, start, timeout)
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
			// A detached boss skips the first-run restore, which switches clients (as dev agent start does)
			if background {
				return openBackground(cmd, c, p.Name, dir, start, timeout, adopt)
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
	cmd.Flags().BoolVar(&background, "background", false, "create the boss detached and wait until its Claude registered")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "with --background in the manager's pane: make an existing boss no live session started the manager's")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "with --background or no terminal, give up when no Claude registered within this")
	addStartFlags(cmd, &start, "boss's coordinator")
	return cmd
}

// openBackground is dev boss <project> --background: the boss exists detached, started by the boss session this
// runs in, and is printed once its Claude registered; adopt makes an existing one the manager's (herd.TakeOver).
func openBackground(cmd *cobra.Command, c *tmux.Client, project, dir string, start agent.Start, timeout time.Duration, adopt bool) error {
	pane := os.Getenv("TMUX_PANE")
	if adopt && !herd.IsManagerPane(c, pane) {
		return errors.New("--adopt runs only in the manager's pane")
	}
	session, created, err := herd.OpenBackground(c, project, dir, start)
	if err != nil {
		return err
	}
	if !created && adopt {
		if err := herd.TakeOver(c, session, pane); err != nil {
			return err
		}
	}
	if created {
		if pane != "" && herd.IsBossPane(c, pane) {
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

// openManager is bare dev boss: the manager attached, or without a terminal started detached when missing and
// printed, never switching a client.
func openManager(cmd *cobra.Command, c *tmux.Client, start agent.Start, timeout time.Duration) error {
	dir, err := config.ManagerDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if hasTerminal() {
		if err := open.RestoreOnce(c, os.Getenv("HOME"), cmd.OutOrStdout()); err != nil {
			return err
		}
		return herd.OpenManager(c, dir, start)
	}
	if _, err := herd.OpenManagerBackground(c, dir, start); err != nil {
		return err
	}
	// An existing one too: "running" only once its Claude is
	if _, err := control.AwaitAgent(c, control.RealClock, herd.Manager, timeout); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "manager running: dev goto "+herd.Manager)
	return nil
}

// hasTerminal reports whether bare dev boss can attach: stdin and stdout are a terminal.
var hasTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
