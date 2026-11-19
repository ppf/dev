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
	"github.com/ppf/dev/internal/sidebar"
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
				return printAll(cmd.OutOrStdout(), cmd.ErrOrStderr(), tmux.New(), asJSON)
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
				setStatus(s, l, boss, cmd.ErrOrStderr())
				return printJSON(out, l)
			}
			if err := l.MarkGone(func() ([]todo.Session, error) { return todoWorkers(boss, cmd.ErrOrStderr()) }); err != nil {
				return err
			}
			for _, it := range l.Items {
				fmt.Fprintln(out, todoLine(it, time.Now()))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: items (id, text, on, session {id, name} when on is a worker, link, done, until, options and answer for an ask, added, done_on, check {kind, pr, since} when it may be out of date, status: decide, yours, working, blocked, waiting, idle, gone, check or boss closed, none when ticked, or on someone when the workers can't be read, and status_on: its worker or person), updated (unix seconds), stale; with --all an array of them, each with its boss, closed for a closed boss's, and error for a list it couldn't read (stale, no items)`)
	cmd.Flags().BoolVar(&all, "all", false, "every dev boss's list on this tmux server, the bosses with none left out: rows start with the boss")
	cmd.PersistentFlags().StringVar(&boss, "boss", "", "the boss session whose list it is: a session name, or a desktop app session's local_… id; done and check take a closed boss too")
	cmd.AddCommand(newTodoSetCmd(&boss), newTodoAddCmd(&boss), newTodoAskCmd(&boss), newTodoAnswerCmd(&boss), newTodoDoneCmd(&boss), newTodoClearCmd(&boss), newTodoCheckCmd(&boss))
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
				} else if s.Boss() != "" && strings.EqualFold(it.On, s.Boss()) {
					fmt.Fprintf(cmd.ErrOrStderr(), "on %s: this list's own boss, so the user's own, as --on me\n", it.On)
					it.On = todo.Me
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "on %s: no single session this boss started (worker or boss) by that name or id, so a person\n", it.On)
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
				if it.On == todo.Me || it.Text != "" {
					replacedWaits(s, *boss, cmd.ErrOrStderr(), replaces, now)
				}
				baseline(s, cmd.ErrOrStderr())
				return printCount(cmd.OutOrStdout(), s, "replaced "+strconv.Itoa(replaces)+" · ", now)
			}
			id, err := s.Add(it, now)
			if err != nil {
				return err
			}
			if it.On == "" || it.On == todo.Me {
				waitsOnYou(*boss, cmd.ErrOrStderr(), id, it.Text, nil)
			}
			baseline(s, cmd.ErrOrStderr())
			return printCount(cmd.OutOrStdout(), s, "added "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().StringVar(&it.On, "on", "", "who does it, when not the user: a session it started, worker or boss (its name or local_… id; its boards show its live state, or that it's gone) or a person; me, or this boss's own name: the user's own (with --replaces, gives it back)")
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	cmd.Flags().StringVar(&until, "until", "", `tick it by itself at this local time: 15:30 (today), 2026-11-16 (that day's end), "2026-11-16 10:00"`)
	cmd.Flags().StringVar(&it.DoneOn, "done-on", "", "tick it by itself once its --link GitHub PR is merged, approved or green (checks passed), its first lookup included, and tell the boss; only this flag does, never the item's text")
	cmd.Flags().IntVar(&replaces, "replaces", 0, "put it in place of open item n, keeping n's number, place and the text, --on, --link, --until, --done-on not given")
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
			waitsOnYou(*boss, cmd.ErrOrStderr(), id, it.Text, it.Options)
			baseline(s, cmd.ErrOrStderr())
			return printCount(cmd.OutOrStdout(), s, "asked "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().StringArrayVar(&it.Options, "option", nil, `an option, in order, up to 9; the recommended one ends with "(Recommended)"`)
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	return cmd
}

func newTodoAnswerCmd(boss *string) *cobra.Command {
	var question, via string
	var fromBoard bool
	cmd := &cobra.Command{
		Use:   "answer <boss> <n> <option|text>",
		Short: "Answer item n on a dev boss's list, as the user: ticks it and sends the boss what was answered",
		Long: `Answer ask n (dev todo ask) on the list of dev boss session <boss>, as the user: an option's number or label, or your own
words. It ticks the item and the boss's inbox gets "you answered #<n> \"<question>\": <answer>", which the boss
takes as the user's answer. Refused in a Claude Code session ($CLAUDECODE set), so no Claude answers for the user,
and for a desktop app boss, which has no dev inbox: answer it in its chat. --question refuses the answer unless item n
still reads that text (item numbers restart once a list empties). --via manager is the manager relaying what the user
told it: only in the manager's coordinator pane, only for an item older than the user's latest prompt there, and the
note quotes that prompt: "you answered #<n> \"<question>\" via manager (\"<prompt>\"): <answer>". The user's pick in
the manager's AskUserQuestion counts as that prompt for one answer: its one question must contain --question, and the
answer must be the option picked.`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			switch {
			case via != "" && via != inbox.Manager:
				return fmt.Errorf("--via %s: only --via %s", via, inbox.Manager)
			case os.Getenv("CLAUDECODE") != "" && !fromBoard && via == "":
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
			var manager inbox.Boss
			var said *inbox.Prompt
			var before int64
			if via != "" {
				if manager, said, err = managerPrompt(c); err != nil {
					return err
				}
				if said.Pick {
					if err := pickFor(said, name, id, question); err != nil {
						return err
					}
				}
				before = said.At
			}
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
			var picked string
			if said != nil && said.Pick {
				// Right before ticking: a pick answers one item, also with two answers relayed at once
				if err := manager.Box.Claim(*said); err != nil {
					return fmt.Errorf("--via manager: %w", err)
				}
				picked = said.Answer
			}
			it, err := s.AnswerPick(id, question, args[2], picked, before, time.Now())
			if err != nil {
				if said != nil && said.Pick {
					err = errors.Join(err, manager.Box.Unclaim(*said))
				}
				return err
			}
			note := inbox.Answer(it.ID, it.Text, it.Answer)
			if said != nil {
				note = inbox.AnswerVia(it.ID, it.Text, it.Answer, said.Text)
			}
			queued, err := b.Send(c, control.RealClock, note)
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
	cmd.Flags().StringVar(&via, "via", "", "manager: the manager relaying the user's answer from their latest prompt to it (only in its coordinator pane)")
	return cmd
}

// managerPrompt is the manager and the user's latest prompt to it, for an answer relayed from its coordinator pane.
func managerPrompt(c *tmux.Client) (inbox.Boss, *inbox.Prompt, error) {
	notHere := errors.New("--via manager runs only in the manager's coordinator pane")
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return inbox.Boss{}, nil, notHere
	}
	self, ok, err := inbox.Coordinator(c, pane)
	switch {
	case err != nil:
		return self, nil, err
	case !ok || self.Name != inbox.Manager:
		return self, nil, notHere
	}
	p, err := self.Box.UserPrompt()
	switch {
	case err != nil:
		return self, nil, err
	case p == nil:
		return self, nil, errors.New("--via manager: the user hasn't prompted the manager yet")
	}
	return self, p, nil
}

// pickFor refuses a pick, the user's latest words to the manager, that isn't one question asking item id of boss,
// question, or that left it open.
func pickFor(pick *inbox.Prompt, boss string, id int, question string) error {
	asked := strings.ToLower(pick.Question)
	item := fmt.Sprintf(" #%d: ", id)
	named := strings.HasPrefix(asked, strings.ToLower(boss)+item) || strings.HasPrefix(asked, strings.ToLower(strings.TrimPrefix(boss, "boss-"))+item)
	switch {
	case question == "":
		return errors.New("--via manager from a pick needs --question with the item's text")
	case pick.Question == "":
		return errors.New("--via manager: the user's pick wasn't one question: ask one item per question")
	case !named || !strings.Contains(asked, strings.ToLower(question)):
		return fmt.Errorf("--via manager: the user's pick was for %q, not %s #%d %q", pick.Question, boss, id, question)
	case strings.EqualFold(pick.Answer, leaveOpen):
		return fmt.Errorf("--via manager: the user picked %q: the item stays open", leaveOpen)
	}
	return nil
}

// leaveOpen is the option the manager adds beside an ask's only one: picked, the item stays open.
const leaveOpen = "Leave it open"

func newTodoDoneCmd(boss *string) *cobra.Command {
	var text string
	cmd := &cobra.Command{
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
			if text != "" && len(ids) != 1 {
				return errors.New("--text ticks one item")
			}
			s, _, err := listStore(*boss, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			now := time.Now()
			if text != "" {
				err = s.DoneText(ids[0], text, now)
			} else {
				err = s.Done(ids, now)
			}
			if err != nil {
				return err
			}
			return printCount(cmd.OutOrStdout(), s, "done "+strings.Join(args, " ")+" · ", now)
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "the item's text: refuse when item n reads otherwise or is on someone else (a board's tick)")
	return cmd
}

func newTodoCheckCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Look up now the PRs the open items link to, then print the list with why to re-check each item",
		Long: `Look up now, with gh, the PR each open item links to (--link), then print the list as dev todo does, each item
to re-check marked ⚠ with why: its PR merged or closed; approved, changes requested, checks green or failing since the
item was added or replaced (its first lookup after that); its PR's lookups failing for 20 minutes; unchanged for 2 days
or more (not an ask, nor a dated item before its --until). An item on a worker session gone says so (session gone),
not on a closed boss's list, which --boss checks too.
An item added with --done-on ticks by itself once a lookup finds its PR in that state.
A dev boss's previews look its items' PRs up by themselves (the manager's, the closed bosses' lists' too): every 15
seconds two of those not looked up for 10 minutes; then, with its inbox on, they tell the boss in it, once per item and reason, of each PR move, worker gone, and of each item unchanged
for 2 days (again after 2 more without --replaces), and of each item ticked by --done-on; an --until tick, and on a
desktop app boss every tick, is in the boss's next reminder. A desktop app boss runs this. A lookup that failed prints
on stderr; the list still prints.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, closed, err := listStore(*boss, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			var lookups []error
			if err := s.Lookup(time.Now(), 0, 0, func(url string) (sidebar.PRView, error) {
				p, err := viewPR(url)
				if err != nil {
					lookups = append(lookups, err)
				}
				return p, err
			}); err != nil {
				return err
			}
			for _, err := range lookups {
				fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev todo check: "+err.Error())
			}
			now := time.Now()
			l, err := s.Read(now)
			if err != nil {
				return err
			}
			if !closed {
				if err := l.MarkGone(func() ([]todo.Session, error) { return todoWorkers(*boss, cmd.ErrOrStderr()) }); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			for _, it := range l.Items {
				fmt.Fprintln(out, todoLine(it, now))
			}
			return nil
		},
	}
}

// viewPR looks a to do item's PR up; tests replace it.
var viewPR = sidebar.ViewPR

// baseline looks up the PR of an item just added or replaced, so what its PR does from now on is what its check
// compares; a failure is only reported, as the item is on the list and the previews look it up again.
func baseline(s todo.Store, stderr io.Writer) {
	if err := s.Lookup(time.Now(), todo.LookupTTL, 0, viewPR); err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: its PR not looked up: "+err.Error())
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

// bossList is one boss's list, for dev todo --all; Error is what couldn't be read (a list not read: stale, no items).
type bossList struct {
	Boss   string `json:"boss"`
	Error  string `json:"error,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	todo.List
}

// printAll prints each dev boss session's list with items on c's server, by session name; none without a server.
// A list it can't read is a row with its error, the others still printed.
func printAll(out, stderr io.Writer, c *tmux.Client, asJSON bool) error {
	now := time.Now()
	lists, err := todoAll(c, stderr, now, asJSON)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(out, lists)
	}
	if len(lists) == 0 {
		_, err := fmt.Fprintln(out, "no boss has a to do list")
		return err
	}
	for _, bl := range lists {
		if bl.Error != "" {
			fmt.Fprintln(out, bl.Boss+"\t⚠ "+bl.Error)
		}
		for _, it := range bl.Items {
			fmt.Fprintln(out, bl.Boss+"\t"+todoLine(it, now))
		}
	}
	return nil
}

// todoAll is printAll's lists, never nil: with asJSON each item's status set (dev todo --all --json, dev serve's
// board), else its gone marked.
func todoAll(c *tmux.Client, stderr io.Writer, now time.Time, asJSON bool) ([]bossList, error) {
	all, err := todo.All(c, now, nil, true)
	if err != nil {
		return nil, err
	}
	lists := []bossList{}
	for _, l := range all {
		bl := bossList{Boss: l.Boss, Closed: l.Closed, List: l.List}
		switch {
		case l.Err != nil:
			bl.Error = "list not read: " + l.Err.Error()
		case !asJSON && !l.Closed:
			// The items still print: what each says holds without the workers
			target := tmux.SessionTarget(l.Boss)
			if err := l.MarkGone(func() ([]todo.Session, error) { return todo.Workers(c, target) }); err != nil {
				bl.Error = "workers not read: " + err.Error()
			}
		}
		lists = append(lists, bl)
	}
	if asJSON {
		setStatuses(c, lists, stderr)
	}
	return lists, nil
}

// waitsOnYou tells the boss above the list's boss (a boss under a boss) that item id waits on the user; a failure
// is only reported, as the item is on the list.
func waitsOnYou(boss string, stderr io.Writer, id int, text string, options []string) {
	pane, _, err := todoBoss(boss)
	queued := false
	if err == nil && pane != "" {
		queued, err = inbox.WaitsOn(tmux.New(), control.RealClock, pane, id, text, options)
	}
	switch {
	case err != nil && queued:
		fmt.Fprintln(stderr, "⚠ dev todo: queued for the boss above, delivered when it next rests: "+err.Error())
	case err != nil:
		fmt.Fprintln(stderr, "⚠ dev todo: the boss above wasn't told: "+err.Error())
	}
}

// replacedWaits tells the boss above of item id, just replaced, when it is the user's: given back, or with new text.
func replacedWaits(s todo.Store, boss string, stderr io.Writer, id int, now time.Time) {
	l, err := s.Read(now)
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: the boss above wasn't told: "+err.Error())
		return
	}
	i := slices.IndexFunc(l.Items, func(x todo.Shown) bool { return x.ID == id })
	if i < 0 {
		fmt.Fprintf(stderr, "⚠ dev todo: the boss above wasn't told: #%d left the list\n", id)
		return
	}
	if l.Items[i].On == "" {
		waitsOnYou(boss, stderr, id, l.Items[i].Text, l.Items[i].Options)
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

// listStore is todoStore, else, for a --boss closed on this server with open items, its list by its file: closed
// reports it; one not read adds its error. Any other --boss keeps todoStore's error, a live session that is no boss too.
func listStore(boss string, stderr io.Writer) (s todo.Store, closed bool, err error) {
	s, err = todoStore(boss, true, stderr)
	if err == nil || boss == "" || strings.HasPrefix(boss, "local_") {
		return s, false, err
	}
	gone, ok, cerr := todo.ClosedStore(tmux.New(), boss, time.Now())
	switch {
	case cerr != nil:
		return s, false, errors.Join(err, cerr)
	case !ok:
		return s, false, err
	}
	return gone, true, nil
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

// todoLine is an item as text at now: number, ☐ (◌ waiting on someone, ☑ ticked), text, who it waits on, link, why
// to re-check it.
func todoLine(it todo.Shown, now time.Time) string {
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
	if it.DoneOn != "" {
		parts = append(parts, "done on "+it.DoneOn)
	}
	switch {
	case it.Answer != "":
		parts = append(parts, "answered: "+it.Answer)
	case len(it.Options) > 0:
		parts = append(parts, "options: "+strings.Join(it.Options, " · "))
	}
	if it.Check != nil {
		parts = append(parts, "⚠ "+it.Check.Long(now))
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
