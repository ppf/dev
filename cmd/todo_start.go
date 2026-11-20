package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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
		Short: "Ask for To do item n on a dev boss's list to be started, as the user: its boss, or the manager for a closed boss",
		Long: `Ask, as the user, for open item n on the list of dev boss session <boss> to be started, while it is a To do card
(placed as dev todo --json places it) and no ask: the user's own, the boss's own parked one (--on boss), one whose
worker is gone, or a closed boss's one on a session. The boss's inbox gets "you were asked to start #<n> \"<text>\"", worded by whose it is, and it starts
a worker on it and links it (dev todo add --replaces <n> --on <worker>), or does a parked one itself. A closed boss's
item goes to the manager, which reopens that boss; refused when no manager takes it (none runs, or its inbox is off), as
for a live boss with its inbox off. The item reads starting (the user's own; the others keep their word, In progress)
until that --replaces (or a set), and dev todo --json marks it start_late after 10 minutes. A second start is refused
until then, and sent again after. --question refuses it unless item n still reads that text. Refused in a Claude Code
session ($CLAUDECODE set), as dev todo answer is, for a desktop app boss, which has no dev inbox, and for the manager's
own list.`,
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

// startItem marks item id of boss name's list started while it reads text and sits in To do (placed as the boards
// place it, from the live sessions; a late start wherever it sits) and sends its note to the boss, or the manager for a closed boss: the receiver's
// name. A note queued for a boss not resting says so on warn. The manager's own list isn't started.
func startItem(c *tmux.Client, name string, id int, text string, warn io.Writer) (string, error) {
	if name == inbox.Manager {
		return "", notStarted{errors.New("the manager's own list isn't started: ask it in its session")}
	}
	s, to, closed, err := startReceiver(c, name)
	if err != nil {
		return "", err
	}
	now := time.Now()
	sh, err := placed(c, s, name, id, closed, now, warn)
	if err != nil {
		return "", err
	}
	it, prev, err := s.Start(sh, text, to.Name, now)
	if err != nil {
		return "", err
	}
	sh.Text = it.Text
	queued, err := to.Send(c, control.RealClock, inbox.Started(name, sh, closed))
	switch {
	case !queued:
		return "", errors.Join(fmt.Errorf("#%d not sent to %s: %w", id, to.Name, err), s.Unstart(id, it.Started.At, prev, time.Now()))
	case err != nil:
		fmt.Fprintf(warn, "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, to.Name, err)
	}
	return to.Name, nil
}

// placed is open item id of s, boss name's list, placed as the boards place it; none: just its id. When the sessions
// can't be read, an item linked to one isn't placed but refused, as its worker may run.
func placed(c *tmux.Client, s todo.Store, name string, id int, closed bool, now time.Time, warn io.Writer) (todo.Shown, error) {
	l, err := s.Read(now)
	if err != nil {
		return todo.Shown{}, err
	}
	i := slices.IndexFunc(l.Items, func(it todo.Shown) bool { return it.ID == id && !it.Done })
	if i < 0 {
		// Start says so, refused
		return todo.Shown{ID: id}, nil
	}
	l.Items = l.Items[i : i+1]
	live, err := liveSessions(c, warn)
	if err != nil && !closed && l.Items[0].Session != nil {
		return todo.Shown{}, notStarted{fmt.Errorf("item %d: sessions not read, so whether %s runs isn't known: %w", id, l.Items[0].Session.Name, err)}
	}
	o := todo.OwnerOf(live, name, closed)
	o.Unread = o.Unread || err != nil
	l.SetStatus(o)
	return l.Items[0], nil
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
