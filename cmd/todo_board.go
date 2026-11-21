package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/kanban"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// boardGuard marks dev todo board running inside its popup, so it draws there instead of opening another.
const boardGuard = "DEV_TODO_BOARD_IN_POPUP"

func newTodoBoardCmd(boss *string) *cobra.Command {
	var client string
	cmd := &cobra.Command{
		Use:   "board [<boss> [<the bosses under it>...]]",
		Short: "Show the to do lists as a kanban board in a full-window popup, as the user: To do, In progress, Waiting, Review, Done",
		Long: `Show every dev boss's to do items (or only the lists of <boss> and the bosses under it) in a full-window tmux popup, as
the web page's five columns, in its order: To do, In progress, Waiting, Review, Done (ticked in the last 24 hours), each
item in the column dev todo --all --json gives it. A card shows its project and number, its state at the right (starting,
a PR's, its worker's), the title, @assignee (you for yours) with its marks (decide, needs you, waiting for, boss closed,
⇄ with another item) and, under it, what is done on it now. A column with a card that waits on you has a red head; the top
line counts what needs you and what is yours. All five show side by side from 136 columns wide, else as many as fit, h/l
moving on to the rest. j/k move within a column, ⏎ or space opens the item over the board (as dev todo show), p shows
the next project, esc closes. The lists are read again every 2 seconds and after an action, so a card moved on the web
page moves here. The previews' and the sidebar's TO DO open it with b.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("the bosses are board's arguments: leave out --boss")
			}
			c := tmux.New()
			if err := knownBosses(c, args); err != nil {
				return err
			}
			if client == "" {
				out, err := c.Run("display-message", "-p", "#{client_name}")
				if err != nil {
					return err
				}
				client = strings.TrimSpace(out)
			}
			if os.Getenv(boardGuard) == "" {
				self, err := os.Executable()
				if err != nil {
					return err
				}
				command := tmux.ShellJoin(append([]string{self, "todo", "board", "--client", client, "--"}, args...)...)
				return popup(c, client, "100%", "100%", boardGuard, command)
			}
			whose := "all bosses"
			if len(args) > 0 {
				whose = args[0] + " and its workers"
			}
			m := kanban.New(whose, func() kanban.Loaded { return kanbanLists(c, args) }, showActions(c, client))
			_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
			return err
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "the tmux client to show it on and switch: tmux's current one by default")
	return cmd
}

// kanbanLists are the lists as dev todo --all --json reads them, only bosses' when any are named.
func kanbanLists(c *tmux.Client, bosses []string) kanban.Loaded {
	var warn strings.Builder
	lists, err := todoAll(c, &warn, time.Now(), true, true, true)
	if err != nil {
		return kanban.Loaded{Err: err}
	}
	out := []kanban.List{}
	for _, bl := range lists {
		if len(bosses) == 0 || slices.Contains(bosses, bl.Boss) {
			out = append(out, kanban.List{Boss: bl.Boss, Closed: bl.Closed, Error: bl.Error, Items: bl.Items, Finished: bl.Finished})
		}
	}
	_, managerErr := c.Run("has-session", "-t", tmux.SessionTarget(sidebar.Manager))
	return kanban.Loaded{Lists: out, Warn: strings.TrimSpace(warn.String()), ManagerRuns: managerErr == nil}
}

// knownBosses refuses a name that's neither a server session nor a list's boss (closed too): its board stays empty.
func knownBosses(c *tmux.Client, bosses []string) error {
	if len(bosses) == 0 {
		return nil
	}
	lists, err := todoAll(c, io.Discard, time.Now(), false, false, true)
	if err != nil {
		return err
	}
	for _, b := range bosses {
		if slices.ContainsFunc(lists, func(bl bossList) bool { return bl.Boss == b }) {
			continue
		}
		if _, err := c.Run("has-session", "-t", tmux.SessionTarget(b)); err != nil {
			return fmt.Errorf("%s: no such session and no to do list", b)
		}
	}
	return nil
}
