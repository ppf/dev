package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoStartCmd(boss *string) *cobra.Command {
	var question string
	var fromBoard bool
	cmd := &cobra.Command{
		Use:   "start <boss> <n> --question <text>",
		Short: "Ask for item n on a dev boss's list to be started, as the user: its boss, or the manager for a closed boss",
		Long: `Ask, as the user, for the user's own open item n on the list of dev boss session <boss> to be started: the
boss's inbox gets "you were asked to start #<n> \"<text>\"", and it starts a worker on it and links it (dev todo add
--replaces <n> --on <worker>). A closed boss's item goes to the manager, which reopens that boss; refused when no
manager takes it (none runs, or its inbox is off), as for a live boss with its inbox off. The item reads starting
until that --replaces (or a set), and dev todo --json marks it start_late after 10 minutes. A second start is refused
until then, and sent again after. --question refuses it unless item n still reads that text. Refused in a Claude
Code session ($CLAUDECODE set), as dev todo answer is, and for a desktop app boss, which has no dev inbox.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			switch {
			case os.Getenv("CLAUDECODE") != "" && !fromBoard:
				return errors.New("refused in a Claude Code session: only the user starts an item (in a shell, the sidebar or a board)")
			case question == "":
				return errors.New("--question <the item's text> is needed")
			case *boss != "":
				return errors.New("the boss is start's first argument: leave out --boss")
			case strings.HasPrefix(name, "local_"):
				return errors.New("a desktop app boss has no dev inbox: ask it in its chat")
			}
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			to, err := startItem(tmux.New(), name, id, question, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d sent to %s\n", id, to)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when item n reads otherwise")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a desktop board's button: allowed in a Claude Code session, as the board runs inside one")
	_ = cmd.Flags().MarkHidden("from-board")
	return cmd
}

// notStarted is a start refused before the list was touched: no receiver for it.
type notStarted struct{ error }

func (e notStarted) Unwrap() error { return e.error }

// startItem marks the user's own item id of boss name's list started while it reads text and sends its note to the
// boss, or the manager for a closed boss: the receiver's name. A note queued for a boss not resting says so on warn.
func startItem(c *tmux.Client, name string, id int, text string, warn io.Writer) (string, error) {
	s, to, closed, err := startReceiver(c, name)
	if err != nil {
		return "", err
	}
	it, prev, err := s.Start(id, text, to.Name, time.Now())
	if err != nil {
		return "", err
	}
	queued, err := to.Send(c, control.RealClock, inbox.Started(name, id, it.Text, closed))
	switch {
	case !queued:
		return "", errors.Join(fmt.Errorf("#%d not sent to %s: %w", id, to.Name, err), s.Unstart(id, it.Started.At, prev, time.Now()))
	case err != nil:
		fmt.Fprintf(warn, "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, to.Name, err)
	}
	return to.Name, nil
}

// startReceiver is the list of dev boss name and the boss a start of its item goes to: name while its session runs,
// else, for a closed boss's list (closed), the manager.
func startReceiver(c *tmux.Client, name string) (s todo.Store, to inbox.Boss, closed bool, err error) {
	pane, err := control.BossPane(c, name)
	if err == nil {
		if to, err = inbox.Recipient(c, name); err != nil {
			return s, to, false, notStarted{err}
		}
		s, err = todo.ForPane(c, pane)
		return s, to, false, err
	}
	s, ok, cerr := todo.ClosedStore(c, name, time.Now())
	switch {
	case cerr != nil:
		return s, to, false, errors.Join(err, cerr)
	case !ok:
		return s, to, false, notStarted{err}
	case name == inbox.Manager:
		return s, to, true, notStarted{errors.New("the manager is closed: dev boss reopens it")}
	}
	to, err = inbox.Recipient(c, inbox.Manager)
	if err != nil {
		return s, to, true, notStarted{fmt.Errorf("%s is closed and no manager takes it (%w): dev boss %s reopens it", name, err, strings.TrimPrefix(name, "boss-"))}
	}
	return s, to, true, nil
}
