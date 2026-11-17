package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
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
coordinator's /clear and the boss session: a dev boss reopened under its name gets it back, and a desktop app
session without a list takes over, on its first dev todo, add or done (not --json, set or clear, nor --boss) or
worker notes, the newest one with open items of a session in its project whose Claude stopped. A boss gets its open items when it is resumed or cleared,
and a dev boss also when it starts. A ticked item shows ticked for a few seconds, then leaves.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Not the board's --json read, which runs for every desktop session, boss or not
			s, err := todoStore(boss, !asJSON, cmd.ErrOrStderr())
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
			if err := l.MarkGone(func() ([]todo.Session, error) { return todoWorkers(boss, cmd.ErrOrStderr()) }); err != nil {
				return err
			}
			for _, it := range l.Items {
				fmt.Fprintln(out, todoLine(it))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: items (id, text, on, session {id, name} when on is a worker, link, done, until), updated (unix seconds), stale`)
	cmd.PersistentFlags().StringVar(&boss, "boss", "", "the boss session whose list it is: a session name, or a desktop app session's local_… id")
	cmd.AddCommand(newTodoSetCmd(&boss), newTodoAddCmd(&boss), newTodoDoneCmd(&boss), newTodoClearCmd(&boss))
	return cmd
}

func newTodoSetCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "set [item...]",
		Short: "Replace the list (items from the arguments, else stdin, one per line), numbered from 1",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := todoStore(*boss, false, cmd.ErrOrStderr())
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
		Short: "Add an item (or put it in place of an open one, --replaces, which may leave out its text); prints its number",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := todoStore(*boss, true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			switch {
			case len(args) == 1:
				if it.Text = todo.Clean(args[0]); it.Text == "" {
					return errors.New("empty item")
				}
			case !cmd.Flags().Changed("replaces"):
				return errors.New("no item: only --replaces keeps the item's text")
			}
			if it.On = todo.Clean(it.On); strings.EqualFold(it.On, todo.Me) {
				it.On = todo.Me
			} else if it.On != "" {
				// A worker of this boss's is linked, so its board shows the worker's state, or that it's gone
				ws, err := todoWorkers(*boss, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if w, ok := todo.Match(it.On, ws); ok {
					it.On, it.Session = w.Name, &w
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "on %s: no single worker of this boss by that name or id, so a person\n", it.On)
				}
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
	cmd.Flags().StringVar(&it.On, "on", "", "who does it, when not the user: a worker session (its name or local_… id; its boards show its live state, or that it's gone) or a person; me: the user's own (with --replaces, gives it back)")
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	cmd.Flags().StringVar(&until, "until", "", `tick it by itself at this local time: 15:30 (today), 2026-11-16 (that day's end), "2026-11-16 10:00"`)
	cmd.Flags().IntVar(&replaces, "replaces", 0, "put it in place of open item n, keeping n's number, place and the text, --on, --link, --until not given")
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
			s, err := todoStore(*boss, true, cmd.ErrOrStderr())
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
			s, err := todoStore(*boss, false, cmd.ErrOrStderr())
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

// todoStore is the list of --boss's session, else of the boss session it runs in; adopt lets a desktop one take
// over an earlier list (desktopTodo). Never for --boss: that session takes over on its own first dev todo.
func todoStore(boss string, adopt bool, stderr io.Writer) (todo.Store, error) {
	pane, id, err := todoBoss(boss)
	switch {
	case err != nil:
		return todo.Store{}, err
	case pane != "":
		return todo.ForPane(tmux.New(), pane)
	}
	return desktopTodo(id, adopt && boss == "", stderr)
}

// todoBoss is the boss session whose list it is: --boss's, else the one it runs in; a pane in a tmux one, else
// a desktop app session's id.
func todoBoss(boss string) (pane, id string, err error) {
	switch {
	case strings.HasPrefix(boss, "local_"):
		return "", boss, nil
	case boss != "":
		pane, err := control.BossPane(tmux.New(), boss)
		return pane, "", err
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return pane, "", nil
	}
	if id := os.Getenv("CLAUDE_CODE_HOST_SESSION_ID"); id != "" {
		return "", id, nil
	}
	return "", "", errors.New("no boss session: run it in a dev boss session or a desktop app session, or pass --boss <session|local_id>")
}

// todoWorkers is the workers of the boss whose list it is: tmux sessions it started, or desktop app sessions it
// spawned, not archived; records that can't be read are reported on stderr.
func todoWorkers(boss string, stderr io.Writer) ([]todo.Session, error) {
	pane, id, err := todoBoss(boss)
	switch {
	case err != nil:
		return nil, err
	case pane != "":
		return todo.Workers(tmux.New(), pane)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	named, warnings, err := desktop.Workers(desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR")).Records, id)
	ws := make([]todo.Session, len(named))
	for i, n := range named {
		ws[i] = todo.Session{ID: n.ID, Name: n.Name}
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "⚠ dev todo: "+w.Error())
		// A record it couldn't read may be a live worker's: listed by id, so its items aren't called gone
		for _, unread := range recordID.FindAllString(w.Error(), -1) {
			ws = append(ws, todo.Session{ID: strings.TrimSuffix(unread, ".json"), Name: strings.TrimSuffix(unread, ".json")})
		}
	}
	return ws, err
}

// recordID is an app session record's file name in a warning.
var recordID = regexp.MustCompile(`local_[A-Za-z0-9_-]+\.json`)

var errDesktopWorker = errors.New("a worker another session started: the to do list is its boss's")

// desktopTodo is the list of desktop app session id, not a worker's; with adopt it takes over an earlier one
// (desktop.Earlier), reporting a failure on stderr and keeping its own.
func desktopTodo(id string, adopt bool, stderr io.Writer) (todo.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return todo.Store{}, err
	}
	paths := desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	worker, err := desktop.Spawned(paths.Records, id)
	if err != nil {
		return todo.Store{}, err
	}
	if worker {
		return todo.Store{}, fmt.Errorf("%s is %w", id, errDesktopWorker)
	}
	s, err := todo.Desktop(id)
	if err != nil || !adopt {
		return s, err
	}
	if _, err := s.AdoptDesktop(func() (func(string) bool, error) { return desktop.Earlier(paths, id) }); err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: no earlier list taken over: "+err.Error())
	}
	return s, nil
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
	switch {
	case it.Gone:
		parts = append(parts, "on "+it.On+", session gone")
	case it.On != "":
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
