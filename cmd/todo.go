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
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoCmd() *cobra.Command {
	var asJSON, all bool
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
and a dev boss also when it starts. A ticked item shows ticked for a few seconds, then leaves.
A decision for the user goes on the list with dev todo ask and its options; the user answers it with dev todo answer,
which ticks it and tells the boss. --all prints every dev boss's list on this tmux server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				if boss != "" {
					return errors.New("--all lists every boss: leave out --boss")
				}
				return printAll(cmd.OutOrStdout(), tmux.New(), asJSON)
			}
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
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: items (id, text, on, session {id, name} when on is a worker, link, done, until, options and answer for an ask), updated (unix seconds), stale; with --all an array of them, each with its boss`)
	cmd.Flags().BoolVar(&all, "all", false, "every dev boss's list on this tmux server, the bosses with none left out: rows start with the boss")
	cmd.PersistentFlags().StringVar(&boss, "boss", "", "the boss session whose list it is: a session name, or a desktop app session's local_… id")
	cmd.AddCommand(newTodoSetCmd(&boss), newTodoAddCmd(&boss), newTodoAskCmd(&boss), newTodoAnswerCmd(&boss), newTodoDoneCmd(&boss), newTodoClearCmd(&boss))
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

func newTodoAskCmd(boss *string) *cobra.Command {
	var it todo.Item
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Put a decision on the list for the user, with the options to pick from; prints its number",
		Long: `Put a decision on the list for the user, with its options (the recommended one marked "(Recommended)"),
and go on: the boss doesn't wait for it. The user answers with dev todo answer (a pick or their own words), which
ticks it and sends the boss "you answered #<n> \"<question>\": <answer>".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if it.Text = todo.Clean(args[0]); it.Text == "" {
				return errors.New("empty question")
			}
			if len(it.Options) == 0 {
				return errors.New(`no --option: give the user the options to pick from, the recommended one marked "(Recommended)"`)
			}
			if len(it.Options) > todo.MaxOptions {
				return fmt.Errorf("%d options, at most %d", len(it.Options), todo.MaxOptions)
			}
			var labels []string
			for i, o := range it.Options {
				o = todo.Clean(o)
				l, _ := todo.Label(o)
				if l == "" {
					return fmt.Errorf("option %d is empty", i+1)
				}
				if slices.ContainsFunc(labels, func(x string) bool { return strings.EqualFold(x, l) }) {
					return fmt.Errorf("option %d repeats %q", i+1, l)
				}
				labels, it.Options[i] = append(labels, l), o
			}
			s, err := todoStore(*boss, true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			now := time.Now()
			id, err := s.Add(it, now)
			if err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "asked "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().StringArrayVar(&it.Options, "option", nil, `an option, in order, up to 9; the recommended one ends with "(Recommended)"`)
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	return cmd
}

func newTodoAnswerCmd(boss *string) *cobra.Command {
	var question string
	var fromBoard bool
	cmd := &cobra.Command{
		Use:   "answer <boss> <n> <option|text>",
		Short: "Answer item n on a dev boss's list, as the user: ticks it and sends the boss what was answered",
		Long: `Answer ask n (dev todo ask) on the list of dev boss session <boss>, as the user: an option's number or label, or your own
words. It ticks the item and the boss's inbox gets "you answered #<n> \"<question>\": <answer>", which the boss
takes as the user's answer. Refused in a Claude Code session ($CLAUDECODE set), so no Claude answers for the user,
and for a desktop app boss, which has no dev inbox: answer it in its chat. --question refuses the answer unless item n
still reads that text (item numbers restart once a list empties).`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			switch {
			case os.Getenv("CLAUDECODE") != "" && !fromBoard:
				return errors.New("refused in a Claude Code session: only the user answers (in a shell, the sidebar or a board)")
			case fromBoard && question == "":
				return errors.New("--from-board needs --question")
			case *boss != "":
				return errors.New("the boss is answer's first argument: leave out --boss")
			case strings.HasPrefix(name, "local_"):
				return errors.New("a desktop app boss has no dev inbox: answer it in its chat")
			}
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			c := tmux.New()
			// Before ticking: an answer it can't send stays open
			b, err := inbox.Recipient(c, name)
			if err != nil {
				return err
			}
			pane, err := control.BossPane(c, name)
			if err != nil {
				return err
			}
			s, err := todo.ForPane(c, pane)
			if err != nil {
				return err
			}
			it, err := s.Answer(id, question, args[2], time.Now())
			if err != nil {
				return err
			}
			queued, err := b.Send(c, control.RealClock, inbox.Answer(it.ID, it.Text, it.Answer))
			switch {
			case !queued:
				return fmt.Errorf("#%d answered %q and ticked, not sent to %s (tell it yourself): %w", id, it.Answer, name, err)
			case err != nil:
				// Answered all the same: a board shows a failed exit as an answer to give again
				fmt.Fprintf(cmd.ErrOrStderr(), "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d answered, sent to %s\n", id, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when item n reads otherwise")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a desktop board's button (needs --question): allowed in a Claude Code session, as the board runs inside one")
	_ = cmd.Flags().MarkHidden("from-board")
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

// bossList is one boss's list, for dev todo --all --json.
type bossList struct {
	Boss string `json:"boss"`
	todo.List
}

// printAll prints each dev boss session's list with items on c's server, by session name; none without a server.
func printAll(out io.Writer, c *tmux.Client, asJSON bool) error {
	type session struct {
		Name string `tmux:"session_name"`
		Herd string `tmux:"@dev_herd"`
	}
	sessions, err := tmux.Query[session](c, "list-sessions")
	if err != nil && !tmux.IsNoServer(err) {
		return err
	}
	slices.SortFunc(sessions, func(a, b session) int { return strings.Compare(a.Name, b.Name) })
	lists := []bossList{}
	now := time.Now()
	for _, sess := range sessions {
		if sess.Herd == "" {
			continue
		}
		target := tmux.SessionTarget(sess.Name)
		s, err := todo.ForPane(c, target)
		if err != nil {
			if _, gone := c.Run("has-session", "-t", target); gone != nil {
				// Closed since list-sessions
				continue
			}
			return fmt.Errorf("%s: %w", sess.Name, err)
		}
		l, err := s.Read(now)
		if err != nil {
			return fmt.Errorf("%s: %w", sess.Name, err)
		}
		if len(l.Items) == 0 {
			continue
		}
		if !asJSON {
			if err := l.MarkGone(func() ([]todo.Session, error) { return todo.Workers(c, target) }); err != nil {
				return err
			}
		}
		lists = append(lists, bossList{Boss: sess.Name, List: l})
	}
	if asJSON {
		return printJSON(out, lists)
	}
	if len(lists) == 0 {
		_, err := fmt.Fprintln(out, "no boss has a to do list")
		return err
	}
	for _, bl := range lists {
		for _, it := range bl.Items {
			fmt.Fprintln(out, bl.Boss+"\t"+todoLine(it))
		}
	}
	return nil
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
	switch {
	case it.Answer != "":
		parts = append(parts, "answered: "+it.Answer)
	case len(it.Options) > 0:
		parts = append(parts, "options: "+strings.Join(it.Options, " · "))
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
