package cmd

import (
	"bufio"
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
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoCmd() *cobra.Command {
	var asJSON bool
	var boss string
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "A boss's to do list: what waits on the user, for its board",
		Long: `Keep the boss's list of what waits on the user: a dev boss's previews pane shows it at the top, and
--json is what a desktop board reads. Without a subcommand, print it. The list belongs to the boss session it
runs in: a dev boss session in tmux, else the desktop app session ($CLAUDE_CODE_HOST_SESSION_ID) unless a
boss started it; --boss names one (a session name, or a desktop app session's local_… id). It outlives the
coordinator's /clear; a ticked item shows ticked for a few seconds, then leaves.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := todoStore(boss)
			if err != nil {
				return err
			}
			l, err := s.Read(time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, l)
			}
			for _, it := range l.Items {
				fmt.Fprintln(out, todoLine(it))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: items (id, text, on, link, done, until), updated (unix seconds), stale`)
	cmd.PersistentFlags().StringVar(&boss, "boss", "", "the boss session whose list it is: a session name, or a desktop app session's local_… id")
	cmd.AddCommand(newTodoSetCmd(&boss), newTodoAddCmd(&boss), newTodoDoneCmd(&boss), newTodoClearCmd(&boss))
	return cmd
}

func newTodoSetCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "set [item...]",
		Short: "Replace the list (items from the arguments, else stdin, one per line), numbered from 1",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := todoStore(*boss)
			if err != nil {
				return err
			}
			items := make([]string, len(args))
			for i, a := range args {
				if items[i] = todo.Clean(a); items[i] == "" {
					return fmt.Errorf("item %d is empty", i+1)
				}
			}
			if len(args) == 0 {
				if items, err = lines(cmd.InOrStdin()); err != nil {
					return err
				}
			}
			if len(items) == 0 {
				return errors.New("no items (dev todo clear empties the list)")
			}
			now := time.Now()
			if err := s.Set(items, now); err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "", now)
		},
	}
}

func newTodoAddCmd(boss *string) *cobra.Command {
	var it todo.Item
	var until string
	var replaces int
	cmd := &cobra.Command{
		Use:   "add <item>",
		Short: "Add an item (or put it in place of an open one, --replaces); prints its number",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := todoStore(*boss)
			if err != nil {
				return err
			}
			it.On = todo.Clean(it.On)
			if it.Text = todo.Clean(args[0]); it.Text == "" {
				return errors.New("empty item")
			}
			now := time.Now()
			if until != "" {
				t, err := todo.ParseUntil(until, now)
				if err != nil {
					return err
				}
				it.Until = t.Unix()
			}
			if cmd.Flags().Changed("replaces") {
				if err := s.Replace(replaces, it, now); err != nil {
					return err
				}
				return printCount(cmd.OutOrStdout(), s, "replaced "+strconv.Itoa(replaces)+" · ", now)
			}
			id, err := s.Add(it, now)
			if err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "added "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().StringVar(&it.On, "on", "", "who it waits on, when not the user (shown last)")
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	cmd.Flags().StringVar(&until, "until", "", `tick it by itself at this local time: 15:30 (today), 2026-11-16 (that day's end), "2026-11-16 10:00"`)
	cmd.Flags().IntVar(&replaces, "replaces", 0, "put it in place of open item n, keeping n's number, place and the --on, --link, --until not given")
	return cmd
}

func newTodoDoneCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "done <n>...",
		Short: "Tick items by number: they show ticked for a few seconds, then leave",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := make([]int, len(args))
			for i, a := range args {
				n, err := strconv.Atoi(a)
				if err != nil {
					return fmt.Errorf("item number %q: %w", a, err)
				}
				ids[i] = n
			}
			slices.Sort(ids)
			ids = slices.Compact(ids)
			s, err := todoStore(*boss)
			if err != nil {
				return err
			}
			now := time.Now()
			if err := s.Done(ids, now); err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "done "+strings.Join(args, " ")+" · ", now)
		},
	}
}

func newTodoClearCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Empty the list: its board shows no to do block",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := todoStore(*boss)
			if err != nil {
				return err
			}
			now := time.Now()
			if err := s.Clear(now); err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "", now)
		},
	}
}

// todoStore is the list of --boss's session, else of the dev boss session or desktop app session it runs in.
func todoStore(boss string) (todo.Store, error) {
	switch {
	case strings.HasPrefix(boss, "local_"):
		return desktopTodo(boss)
	case boss != "":
		c := tmux.New()
		pane, err := control.BossPane(c, boss)
		if err != nil {
			return todo.Store{}, err
		}
		return todo.ForPane(c, pane)
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return todo.ForPane(tmux.New(), pane)
	}
	if id := os.Getenv("CLAUDE_CODE_HOST_SESSION_ID"); id != "" {
		return desktopTodo(id)
	}
	return todo.Store{}, errors.New("no boss session: run it in a dev boss session or a desktop app session, or pass --boss <session|local_id>")
}

var errDesktopWorker = errors.New("a worker another session started: the to do list is its boss's")

// desktopTodo is the list of desktop app session id, which must have an app record and not be a worker.
func desktopTodo(id string) (todo.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return todo.Store{}, err
	}
	worker, err := desktop.Spawned(desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR")).Records, id)
	if err != nil {
		return todo.Store{}, err
	}
	if worker {
		return todo.Store{}, fmt.Errorf("%s is %w", id, errDesktopWorker)
	}
	return todo.Desktop(id)
}

// printCount prints prefix, then how many items are open: "to do 3, 1 on others", "to do empty".
func printCount(out io.Writer, s todo.Store, prefix string, now time.Time) error {
	l, err := s.Read(now)
	if err != nil {
		return err
	}
	text := "to do empty"
	if n, on := l.Open(); n > 0 {
		text = "to do " + strconv.Itoa(n)
		if on > 0 {
			text += fmt.Sprintf(", %d on others", on)
		}
	}
	_, err = fmt.Fprintln(out, prefix+text)
	return err
}

// todoLine is an item as text: number, ☐ (◌ waiting on someone, ☑ ticked), text, who it waits on, link.
func todoLine(it todo.Shown) string {
	box := "☐"
	switch {
	case it.Done:
		box = "☑"
	case it.On != "":
		box = "◌"
	}
	parts := []string{strconv.Itoa(it.ID), box, it.Text}
	if it.On != "" {
		parts = append(parts, "on "+it.On)
	}
	if it.Until != 0 {
		parts = append(parts, "until "+time.Unix(it.Until, 0).Format(todo.UntilLayout))
	}
	if it.Link != "" {
		parts = append(parts, it.Link)
	}
	return strings.Join(parts, "\t")
}

// lines is r's non-blank lines, cleaned (todo.Clean).
func lines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if l := todo.Clean(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}
