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

func newTodoDoingCmd(boss *string) *cobra.Command {
	var question string
	var stop, fromBoard bool
	cmd := &cobra.Command{
		Use:   "doing [<boss>] <n> [text] [--stop]",
		Short: "Mark item n in progress with what is done now: the user on their own item (tells its boss), a boss on its own",
		Long: `Mark an open item in progress, with a short line of what is done now; --stop ends it.
The user: dev todo doing <boss> <n> ["text"] --question <the item's text>, only on their own item that is no ask:
the boss's inbox gets "the user is on #<n> \"<item>\": <text>" (for a closed boss's list, the manager's), so no worker
is started on it; a newer line replaces one still queued. No one running, or a desktop app boss: marked, no one told.
Refused in a Claude Code session ($CLAUDECODE set), as dev todo start is.
A boss: dev todo doing <n> ["text"] in its own session, only on its own items (--on boss); no one is told.
A tick, --stop or an --on change ends it; a --replaces keeps it.`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := strconv.Atoi(args[0]); err == nil {
				if question != "" || fromBoard {
					return errors.New("--question and --from-board are the user's form: dev todo doing <boss> <n> --question <the item's text>")
				}
				return bossDoing(cmd, *boss, args, stop)
			}
			switch {
			case os.Getenv("CLAUDECODE") != "" && !fromBoard:
				return errors.New("refused in a Claude Code session: only the user marks their item doing (in a shell or a board); a boss marks its own: dev todo doing <n>")
			case question == "":
				return errors.New("--question <the item's text> is needed")
			case *boss != "":
				return errors.New("the boss is doing's first argument: leave out --boss")
			case len(args) < 2:
				return errors.New("no item number: dev todo doing <boss> <n>")
			}
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			text, err := doingText(args[2:], stop)
			if err != nil {
				return err
			}
			to, err := userDoing(tmux.New(), args[0], id, question, text, stop, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d %s, %s\n", id, doingDone(stop), toldOr(to))
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when item n reads otherwise (the user's form)")
	cmd.Flags().BoolVar(&stop, "stop", false, "end it")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a desktop board's button: allowed in a Claude Code session, as the board runs inside one")
	_ = cmd.Flags().MarkHidden("from-board")
	return cmd
}

// toldOr is "<told> told", "no one told" for none.
func toldOr(told string) string {
	if told == "" {
		return "no one told"
	}
	return told + " told"
}

// doingDone is what a doing change did.
func doingDone(stop bool) string {
	if stop {
		return "stopped"
	}
	return "in progress"
}

// doingText is the doing line from the arguments after the item number: none, or one; --stop takes none.
func doingText(args []string, stop bool) (string, error) {
	switch {
	case len(args) > 1:
		return "", errors.New("one doing line: quote it")
	case len(args) == 1 && stop:
		return "", errors.New("--stop takes no doing line")
	case len(args) == 1:
		return args[0], nil
	}
	return "", nil
}

// doingOf is the Doing a change sets: none for stop.
func doingOf(text string, stop bool) *todo.Doing {
	if stop {
		return nil
	}
	return &todo.Doing{Text: text}
}

// noList is a doing refused before the list was read: no such list.
type noList struct{ error }

func (e noList) Unwrap() error { return e.error }

// notTold is a doing marked whose running receiver couldn't take its note.
type notTold struct{ error }

func (e notTold) Unwrap() error { return e.error }

// bossDoing is dev todo doing <n> [text] in a boss's own session: its own item, no one told.
func bossDoing(cmd *cobra.Command, boss string, args []string, stop bool) error {
	id, _ := strconv.Atoi(args[0])
	text, err := doingText(args[1:], stop)
	if err != nil {
		return err
	}
	s, err := todoStore(boss, true, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if _, err := s.SetDoing(id, "", false, doingOf(text, stop), time.Now()); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d %s\n", id, doingDone(stop))
	return err
}

// userDoing marks the user's own item id of boss name's list, while it reads question, as theirs in progress with text,
// or ends it (stop), and tells the boss, or the manager for a closed boss: the one told, "" for no one (none running,
// its inbox off, a desktop app boss), said on warn. A running one whose inbox couldn't take the note is an error, as
// it could start a worker on the item.
func userDoing(c *tmux.Client, name string, id int, question, text string, stop bool, warn io.Writer) (string, error) {
	s, closed, err := listStore(name, false, warn)
	if err != nil {
		return "", noList{err}
	}
	it, err := s.SetDoing(id, question, true, doingOf(text, stop), time.Now())
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(name, "local_") {
		fmt.Fprintf(warn, "⚠ dev todo: #%d %s, no one told: a desktop app boss has no dev inbox\n", id, doingDone(stop))
		return "", nil
	}
	recipient := name
	if closed {
		recipient = inbox.Manager
	}
	to, err := inbox.Recipient(c, recipient)
	switch {
	case err != nil && running(c, recipient) && !errors.Is(err, inbox.ErrInboxOff):
		return "", notTold{fmt.Errorf("#%d %s, not sent to %s (tell it yourself): %w", id, doingDone(stop), recipient, err)}
	case err != nil:
		fmt.Fprintf(warn, "⚠ dev todo: #%d %s, no one told: %v\n", id, doingDone(stop), err)
		return "", nil
	}
	now := ""
	if it.Doing != nil {
		now = it.Doing.Text
	}
	queued, err := to.Send(c, control.RealClock, inbox.UserDoing(name, id, it.Text, now, stop, closed))
	switch {
	case !queued:
		return "", notTold{fmt.Errorf("#%d %s, not sent to %s (tell it yourself): %w", id, doingDone(stop), to.Name, err)}
	case err != nil:
		fmt.Fprintf(warn, "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, to.Name, err)
	}
	return to.Name, nil
}
