package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newHerdCmd() *cobra.Command {
	var previews, background, adopt, noServe bool
	var timeout time.Duration
	var start agent.Start
	cmd := &cobra.Command{
		Use:     "boss [project]",
		Aliases: []string{"herd"},
		Short:   "Open the coordinator view: a coordinator Claude next to live previews of its workers",
		Long: `Open or attach the project's boss session ("boss-<project>"): a coordinator Claude started in the
project's main checkout that drives the other sessions with the dev-agents skill, next to live previews of
the workers it started. Without a project it opens or attaches the manager (session "manager", in
~/.config/dev/manager), a boss whose workers are bosses, on fable and medium effort unless --model or
--effort say otherwise; with no terminal (stdin or stdout not one) it starts the manager detached when
missing, waits until its Claude registered, and prints how to reach it. --model and --effort start a new
boss's coordinator with them (with a boss that exists they are an error unless they equal what it was started
with); it keeps them when dev restarts it.
--background creates the boss without attaching or switching a client, marks it started by the boss session
dev runs in, prints its name and waits until its Claude registered (as dev agent start does); an existing
boss is only printed. With --adopt, run in the manager's pane, an existing boss no live session started becomes
the manager's as if it had started it: its news goes up to the manager, it shows in the manager's --mine list and
previews, and it keeps its workers and list.
Every dev boss (the manager, a project's boss, --background; not --adopt) starts
dev serve, the web board, detached with --awake=false when none answers: on 127.0.0.1:8417 (this Mac only), or DEV_SERVE_ADDR; only on tmux's default server unless DEV_SERVE_ADDR is set, not when its launchd
agent is installed, never with --no-serve or DEV_NO_SERVE=1. A failed start is one ⚠ line; the boss opens anyway.`,
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
				exe, err := os.Executable()
				if err != nil {
					return fmt.Errorf("boss: binary path for reload: %w", err)
				}
				return herd.Run(c, pane, exe, os.Args)
			}
			if adopt && !background {
				return errors.New("--adopt needs --background: dev boss <project> --background --adopt")
			}
			if background && len(args) == 0 {
				return errors.New("--background needs a project: dev boss <project> --background")
			}
			// A refused boss starts no dev serve: a detached one starts it once it exists, an attached one before the attach
			if len(args) == 0 {
				if hasTerminal() {
					if err := herd.Check(c, herd.Manager, start); err != nil {
						return err
					}
					autoServe(cmd.ErrOrStderr(), noServe)
					return openManager(cmd, c, start, timeout)
				}
				if err := openManager(cmd, c, start, timeout); err != nil {
					return err
				}
				autoServe(cmd.ErrOrStderr(), noServe)
				return nil
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
				if err := openBackground(cmd, c, p.Name, dir, start, timeout, adopt); err != nil {
					return err
				}
				if !adopt {
					autoServe(cmd.ErrOrStderr(), noServe)
				}
				return nil
			}
			// After the first-run restore, so a saved boss is attached rather than replaced
			if err := open.RestoreOnce(c, os.Getenv("HOME"), cmd.OutOrStdout()); err != nil {
				return err
			}
			if err := herd.Check(c, herd.SessionName(p.Name), start); err != nil {
				return err
			}
			autoServe(cmd.ErrOrStderr(), noServe)
			return herd.Open(c, p.Name, dir, start)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "run the previews pane (inside the boss session)")
	_ = cmd.Flags().MarkHidden("previews")
	cmd.Flags().BoolVar(&background, "background", false, "create the boss detached and wait until its Claude registered")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "with --background in the manager's pane: make an existing boss no live session started the manager's")
	cmd.Flags().BoolVar(&noServe, "no-serve", false, "don't start dev serve (also DEV_NO_SERVE=1)")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "with --background or no terminal, give up when no Claude registered within this")
	addStartFlags(cmd, &start, "boss's coordinator")
	return cmd
}

// openBackground is dev boss <project> --background: the boss exists detached, started by the boss session this
// runs in, and is printed once its Claude registered; adopt only makes an open one the manager's (herd.TakeOver).
func openBackground(cmd *cobra.Command, c *tmux.Client, project, dir string, start agent.Start, timeout time.Duration, adopt bool) error {
	pane := os.Getenv("TMUX_PANE")
	if adopt {
		if !herd.IsManagerPane(c, pane) {
			return errors.New("--adopt runs only in the manager's pane")
		}
		session := herd.SessionName(project)
		if err := herd.TakeOver(c, session, pane, start); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), session)
		return nil
	}
	session, created, err := herd.OpenBackground(c, project, dir, start)
	if err != nil {
		return err
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
	// An existing one too: the user may have opened it before the manager got to the start
	if err := askStarts(c, session, cmd.ErrOrStderr()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "⚠ dev boss: %s not asked to start: %v; ask it in its session\n", session, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), session)
	return nil
}

// askStarts takes over for boss session each open item's start sent to the manager while it was closed and sends it
// the start note: its SessionStart reminder takes no turn and nothing else prompts a detached boss.
// Starts a boss got itself are skipped: it may already have a worker on them.
func askStarts(c *tmux.Client, session string, warn io.Writer) error {
	s, err := todo.ForPane(c, tmux.SessionTarget(session))
	if err != nil {
		return err
	}
	l, err := s.Read(time.Now())
	if err != nil {
		return err
	}
	var to inbox.Boss
	var errs []error
	for _, it := range l.Items {
		if it.Done || it.Started == nil || it.Started.To != inbox.Manager {
			continue
		}
		if to.Name == "" {
			if to, err = inbox.Recipient(c, session); err != nil {
				return err
			}
		}
		// Claimed first: an overlapping call, or an item set under the same number since, sends nothing
		moved, err := s.Handed(it.ID, it.Started.At, inbox.Manager, session, time.Now())
		if !moved {
			if err != nil {
				errs = append(errs, fmt.Errorf("#%d: %w", it.ID, err))
			}
			continue
		}
		sh, err := placed(c, s, session, it.ID, false, time.Now(), warn)
		queued := false
		if err == nil {
			queued, err = to.Send(c, control.RealClock, inbox.Started(session, sh, false))
		}
		switch {
		case !queued:
			_, uerr := s.Handed(it.ID, it.Started.At, session, inbox.Manager, time.Now())
			errs = append(errs, fmt.Errorf("#%d: %w", it.ID, errors.Join(err, uerr)))
		case err != nil:
			fmt.Fprintf(warn, "⚠ dev boss: #%d queued in %s's inbox, delivered when it next rests: %v\n", it.ID, session, err)
		}
	}
	return errors.Join(errs...)
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
