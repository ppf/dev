package cmd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

func newTodoAskingCmd(boss *string) *cobra.Command {
	var question string
	cmd := &cobra.Command{
		Use:   "asking <boss> <n>",
		Short: "Mark ask n of a dev boss's list as asked in the manager now, right before the manager's question about it (manager only)",
		Long: `Run by the manager right before its AskUserQuestion about ask n of boss <boss>'s list: the terminal board and the web
board (the phone page) show "asked in manager now" on it, so the user answers there or here, knowing. It goes once the
item closes or its text changes, at the manager's next pick of any question (its PostToolUse hook), another asking, a
/clear or restart of the manager, or 30 minutes on (a dialog closed with Esc fires no hook). --question is the ask's
text: refused unless open ask n still reads it. Only from the manager's coordinator pane.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("the boss is asking's first argument: leave out --boss")
			}
			if question == "" {
				return errors.New("asking needs --question with the ask's text")
			}
			name := args[0]
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			c := tmux.New()
			self, err := managerSelf(c, "dev todo asking")
			if err != nil {
				return err
			}
			it, err := showItem(c, cmd.ErrOrStderr(), name, id, question)
			switch {
			case err != nil:
				return err
			case it.Done:
				return fmt.Errorf("%s #%d is closed already: don't ask it", name, id)
			case len(it.Options) == 0:
				return fmt.Errorf("%s #%d is no ask", name, id)
			}
			if err := self.Box.SetAsking(inbox.Asking{Boss: name, ID: id, Text: it.Text, At: time.Now().Unix()}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s #%d: asked in manager now\n", name, id)
			return err
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the ask's text: refuse when item n reads otherwise")
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
