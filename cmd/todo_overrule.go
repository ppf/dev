package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoOverruleCmd(boss *string) *cobra.Command {
	var question string
	var fromBoard bool
	cmd := &cobra.Command{
		Use:   "overrule <boss> <n> <option> --question <text>",
		Short: "Answer ask n of a dev boss's list, decided for the user, with another of its options, as the user",
		Long: `Answer ask n of the list of dev boss session <boss>, ticked in the last day as decided for the user (by the
manager, or by the boss itself), with <option>: one of the options it was picked over, by its label or its number in
the ask. In one step it reopens the ask and answers it, ticked by the user, no longer decided for them, and sends the
boss the answer as the user's, the decision overruled; a note of the manager's settle still queued goes. Only while
item n reads --question's text and its boss runs. It keeps its number unless an item has it now, as dev todo reopen. Refused in a Claude Code session ($CLAUDECODE set), as dev todo
reopen is.`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case os.Getenv("CLAUDECODE") != "" && !fromBoard:
				return errors.New("refused in a Claude Code session: only the user overrules a decision (in a shell or a board)")
			case question == "":
				return errors.New("--question <the item's text> is needed")
			case *boss != "":
				return errors.New("the boss is overrule's first argument: leave out --boss")
			}
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			it, err := overruleItem(tmux.New(), cmd.ErrOrStderr(), args[0], id, question, args[2], "")
			if err != nil {
				return err
			}
			if it.ID != id {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d overruled as #%d with %q (#%d is another item now), sent to %s\n", id, it.ID, it.Answer, id, args[0])
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d overruled with %q, sent to %s\n", it.ID, it.Answer, args[0])
			return err
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when ticked item n reads otherwise")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a board's key: allowed in a Claude Code session, as a board may run inside one")
	_ = cmd.Flags().MarkHidden("from-board")
	return cmd
}

// overruleItem answers ask id of boss name's list, decided for the user and reading question, with pick in its place
// (todo.Store.Overrule), given via, and tells the boss; resolving the boss first, so one that can't be found changes
// nothing. A manager's settled note of the item still queued goes in the write that queues the overrule.
func overruleItem(c *tmux.Client, stderr io.Writer, name string, id int, question, pick, via string) (todo.Item, error) {
	b, err := inbox.Recipient(c, name)
	if err != nil {
		return todo.Item{}, fmt.Errorf("an overrule tells the boss: %w", err)
	}
	pane, err := control.BossPane(c, name)
	if err != nil {
		return todo.Item{}, err
	}
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return todo.Item{}, err
	}
	it, was, err := s.Overrule(id, question, pick, via, time.Now())
	if errors.As(err, new(todo.StillDone)) {
		// Answered all the same: the boss hears of it
		fmt.Fprintf(stderr, "⚠ dev todo: %v\n", err)
	} else if err != nil {
		return it, err
	}
	note := inbox.AnswerOverruled(it.ID, it.Text, it.Answer, todo.Split(was.Pick).Label, was.By)
	queued, err := b.SendOverruled(c, control.RealClock, note, id, question)
	switch {
	case !queued:
		return it, notTold{fmt.Errorf("#%d overruled with %q, not sent to %s (tell it yourself): %w", id, it.Answer, name, err)}
	case err != nil:
		fmt.Fprintf(stderr, "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, name, err)
	}
	return it, nil
}

// withdrawSettled drops a queued settled note of item id, text from boss name's inbox, as the user reopened it; none for
// a boss not running: its inbox went with its session.
func withdrawSettled(c *tmux.Client, warn io.Writer, name string, id int, text string) {
	if !running(c, name) {
		return
	}
	b, err := inbox.Recipient(c, name)
	if err == nil {
		err = b.Box.WithdrawSettled(id, text)
	}
	if err != nil && !errors.Is(err, inbox.ErrNotBoss) && !errors.Is(err, inbox.ErrInboxOff) {
		fmt.Fprintf(warn, "⚠ dev todo: a settled note of #%d may still reach %s: %v\n", id, name, err)
	}
}
