package cmd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoAskingCmd(boss *string) *cobra.Command {
	var questions []string
	cmd := &cobra.Command{
		Use:   "asking <boss> <n> [<boss> <n>]...",
		Short: "Mark the asks of the manager's next dialog as asked in the manager now, right before it (manager only)",
		Long: `Run by the manager right before its AskUserQuestion about ask n of boss <boss>'s list, one call naming every ask of
the dialog (up to 4 pairs): the terminal board and the web board (the phone page) show "asked in manager now" on each,
so the user answers there or here, knowing. A mark goes once its item closes or its text changes, and all of them at
the manager's next pick of any question (its PostToolUse hook), another asking, a /clear or restart of the manager, or
30 minutes on (a dialog closed with Esc fires no hook). One --question per pair, in the same order, is that ask's text:
refused unless open ask n still reads it; one refused item marks none. Only from the manager's coordinator pane.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || len(args)%2 != 0 || len(args) > 8 {
				return fmt.Errorf("asking takes 1 to 4 pairs of <boss> <n>, got %d argument(s)", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("the boss is asking's first argument: leave out --boss")
			}
			if slices.Contains(questions, "") {
				return errors.New("asking needs --question with the ask's text: one is empty")
			}
			if len(questions) != len(args)/2 {
				return fmt.Errorf("asking needs one --question with the ask's text per <boss> <n>, in order: %d for %d", len(questions), len(args)/2)
			}
			c := tmux.New()
			self, err := managerSelf(c, "dev todo asking")
			if err != nil {
				return err
			}
			now := time.Now().Unix()
			var marks []inbox.Asking
			for k := 0; k < len(args); k += 2 {
				name := args[k]
				id, err := strconv.Atoi(args[k+1])
				if err != nil {
					return fmt.Errorf("item number %q: %w", args[k+1], err)
				}
				it, err := showItem(c, cmd.ErrOrStderr(), name, id, questions[k/2])
				switch {
				case err != nil:
					return err
				case it.Done:
					return fmt.Errorf("%s #%d is closed already: don't ask it", name, id)
				case len(it.Options) == 0:
					return fmt.Errorf("%s #%d is no ask", name, id)
				case it.Closed:
					return fmt.Errorf("%s is closed: its #%d can't be answered", name, id)
				case !todo.Own(it.On, it.Session, name):
					return fmt.Errorf("%s #%d is on %s, not the user's", name, id, it.On)
				case slices.ContainsFunc(marks, func(a inbox.Asking) bool { return a.Boss == name && a.ID == id }):
					return fmt.Errorf("%s #%d is named twice", name, id)
				}
				marks = append(marks, inbox.Asking{Boss: name, ID: id, Text: it.Text, At: now})
			}
			if err := self.Box.SetAsking(marks); err != nil {
				return err
			}
			for _, a := range marks {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s #%d: asked in manager now\n", a.Boss, a.ID); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&questions, "question", nil, "an ask's text, one per <boss> <n> in order: refuse when item n reads otherwise")
	return cmd
}

// managerSelf is the manager, from its own coordinator pane only; what names the run refused elsewhere.
func managerSelf(c *tmux.Client, what string) (inbox.Boss, error) {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		self, ok, err := inbox.Coordinator(c, pane)
		if err != nil || ok && self.Name == inbox.Manager {
			return self, err
		}
	}
	return inbox.Boss{}, errors.New(what + " runs only in the manager's coordinator pane")
}
