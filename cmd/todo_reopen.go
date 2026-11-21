package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/todo"
)

func newTodoReopenCmd(boss *string) *cobra.Command {
	var question string
	var fromBoard bool
	cmd := &cobra.Command{
		Use:   "reopen <boss> <n> --question <text>",
		Short: "Put the user's ticked item n of a dev boss's list back on it, as the user: Done back to To do",
		Long: `Put item n of the list of dev boss session <boss>, ticked in the last day, back on the list as an open item, as
the user: only their own (no --on), the newest ticked one by that number that reads --question's text. It keeps its
number, unless an item has it now (numbers restart once a list empties): then it gets the next one, printed; its added
time is now, so its checks and age start afresh. Its text,
link, context and options come back; a passed --until, --done-on, an ask's answer, a doing line, a start and a
--part-of link do not. Works for a closed boss's list. Refused in a Claude Code session ($CLAUDECODE set), as dev
todo start is.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case os.Getenv("CLAUDECODE") != "" && !fromBoard:
				return errors.New("refused in a Claude Code session: only the user reopens an item (in a shell or a board)")
			case question == "":
				return errors.New("--question <the item's text> is needed")
			case *boss != "":
				return errors.New("the boss is reopen's first argument: leave out --boss")
			}
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			it, err := reopenItem(args[0], id, question, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if it.ID != id {
				fmt.Fprintf(cmd.OutOrStdout(), "#%d reopened as #%d: #%d is another item now\n", id, it.ID, id)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d reopened\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when ticked item n reads otherwise")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a desktop board's button: allowed in a Claude Code session, as the board runs inside one")
	_ = cmd.Flags().MarkHidden("from-board")
	return cmd
}

// noReopenList is a reopen that failed before a list was found.
type noReopenList struct{ error }

func (e noReopenList) Unwrap() error { return e.error }

// reopenItem reopens the user's ticked item id of boss name's list, a closed boss's too, while it reads text.
func reopenItem(name string, id int, text string, warn io.Writer) (todo.Item, error) {
	s, _, err := listStoreOf(name, false, true, warn)
	if err != nil {
		return todo.Item{}, noReopenList{err}
	}
	return s.Reopen(id, text, time.Now())
}
