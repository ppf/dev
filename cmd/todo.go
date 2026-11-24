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
	"unicode/utf8"

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
runs in: the T3 Code thread ($T3CODE_THREAD_ID) unless another thread delegated it, else a dev boss session in
tmux, else the desktop app session ($CLAUDE_CODE_HOST_SESSION_ID) unless a boss started it; --boss names one (a
session name, or a desktop app session's local_… id). It outlives the coordinator's /clear and the boss session: a dev boss reopened under its name gets it back, and a desktop app
session without a list takes over, on its first dev todo, add or done (not --json, set or clear, nor --boss) or
worker notes, the newest one with open items of a session in its project whose Claude stopped. A boss but a T3 Code one gets its open items when it is resumed or cleared,
and a dev boss also when it starts. A ticked item shows ticked for a few seconds, then leaves.
A decision for the user goes on the list with dev todo ask and its options; the user answers it with dev todo answer,
which ticks it and tells the boss. dev todo note keeps the boss's decisions, what the user said and open risks beside the list.
dev todo take-over takes over an earlier boss's list for this project, of any kind (tmux, desktop app, T3 Code).
--all prints every dev boss's list on this tmux server.`,
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
			if l.Moved != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev todo: "+l.Moved)
			}
			if asJSON {
				setStatus(s, l, boss, cmd.ErrOrStderr())
				return printJSON(out, l)
			}
			hintT3(cmd, boss, s, time.Now())
			if err := l.MarkGone(func() ([]todo.Session, error) { return todoWorkers(boss, cmd.ErrOrStderr()) }); err != nil {
				return err
			}
			for _, it := range l.Items {
				printItem(out, "", it, time.Now())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: items (id, text, on, session {id, name} when on is a worker, link, done, until, options ("<label> | <detail> (Recommended)" as given), choices (each option as {label, detail, recommended}), context (its lines) and answer for an ask, added, done_on, check {kind, pr, since} when it may be out of date, started {at, to} and start_late (10 minutes on) once dev todo start asked for it, status: decide, yours, starting, working, blocked, asks, waiting, idle, parked, gone, check or boss closed, none when ticked, or on someone when the workers can't be read, status_on: its worker or person, or the boss a start went to, quiet for a test item (--quiet), no_details for the user's own item marked --no-details, now: what its worker does now, when known (a desktop app worker's status text; dev serve's board also reads a tmux worker's tool call or wait), pr_state: its GitHub PR's open, merged or closed at the last lookup, column: todo, progress, waiting, review or done, assignee {kind: you, boss or worker, name, running}, waiting_for: the person it waits for, needs_you when it needs the user, parked for the boss's own that no one has taken up, doing {text, at}: who does it says what they do now, part_of: the id of its other half, for: an ask's item that waits on it (dev todo ask --for), waits_on {id, text, options, choices}: the open ask an item waits on, obvious: why its boss marked an open ask obvious, decided {by: manager or boss, pick, over, why, at}: an ask settled for the user with its recommended option, asking: the manager has its question up now (dev todo asking), finished items too), updated (unix seconds), stale; with --all an array of them, each with its boss, closed for a closed boss's, and error for a list it couldn't read (stale, no items)`)
	cmd.Flags().BoolVar(&all, "all", false, "every dev boss's list on this tmux server, the bosses with none left out: rows start with the boss")
	cmd.PersistentFlags().StringVar(&boss, "boss", "", "the boss session whose list it is: a session name, or a desktop app session's local_… id; done and check take a closed boss too")
	cmd.AddCommand(newTodoSetCmd(&boss), newTodoAddCmd(&boss), newTodoAskCmd(&boss), newTodoAnswerCmd(&boss), newTodoStartCmd(&boss), newTodoDoingCmd(&boss), newTodoReopenCmd(&boss), newTodoOverruleCmd(&boss), newTodoAskingCmd(&boss), newTodoShowCmd(&boss), newTodoBoardCmd(&boss), newTodoDoneCmd(&boss), newTodoClearCmd(&boss), newTodoCheckCmd(&boss), newTodoNoteCmd(&boss), newTodoTakeOverCmd(&boss))
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

// longItem is the text length over which dev todo add warns.
const longItem = 120

func warnLong(stderr io.Writer, text string) {
	if n := utf8.RuneCountInString(text); n > longItem {
		fmt.Fprintf(stderr, "⚠ long item (%d characters): keep a short title, put details in --context\n", n)
	}
}

func warnNoDetails(s todo.Store, stderr io.Writer, id int, now time.Time) {
	l, err := s.Read(now)
	if err != nil {
		fmt.Fprintf(stderr, "⚠ details not checked: %v\n", err)
		return
	}
	for _, it := range l.Items {
		if it.ID == id && it.NeedsDetails() {
			fmt.Fprintln(stderr, "⚠ no details: add them with --details (what it is and why, where it stands, what done looks like; for a waiting item who, what was asked, when and where)")
		}
	}
}

// waitingFor is person as an item's on, refused when it names what --on means otherwise: me, boss, this list's own
// boss (own), or a session the boss started.
func waitingFor(person, own, boss string, stderr io.Writer) (string, error) {
	switch {
	case strings.EqualFold(person, todo.Me), strings.EqualFold(person, todo.Boss), own != "" && strings.EqualFold(person, own):
		return "", fmt.Errorf("--waiting-for %s: not a person: the user's own is --on me, your own task --on boss", person)
	}
	ws, err := todoWorkers(boss, stderr)
	if err != nil {
		return "", err
	}
	if w, ok := todo.Match(person, ws); ok {
		// Its name, unless another worker shares it or --on reads it as me or boss
		on := w.Name
		if m, ok := todo.Match(w.Name, ws); !ok || m.ID != w.ID || strings.EqualFold(on, todo.Me) || strings.EqualFold(on, todo.Boss) {
			on = w.ID
		}
		return "", fmt.Errorf("--waiting-for %s: a session you started: --on %s", person, on)
	}
	return person, nil
}

func newTodoAddCmd(boss *string) *cobra.Command {
	var it todo.Item
	var until, person string
	var replaces int
	cmd := &cobra.Command{
		Use:   "add <item>",
		Short: "Add an item (or put it in place of an open one, --replaces, which may leave out its text); prints its number",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			it.On, person = todo.Clean(it.On), todo.Clean(person)
			waiting := cmd.Flags().Changed("waiting-for")
			switch {
			case it.On != "" && waiting:
				return errors.New("--on or --waiting-for, not both")
			case waiting && person == "":
				return errors.New("--waiting-for: no person")
			}
			s, err := todoStore(*boss, false, cmd.ErrOrStderr())
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
			if it.Context, err = contextLines(it.Context); err != nil {
				return err
			}
			if it.Details, err = detailsFlag(cmd, it.Details); err != nil {
				return err
			}
			if it.NoDetails && it.Details != "" {
				return errors.New("--details or --no-details, not both")
			}
			stderr := cmd.ErrOrStderr()
			// A desktop app boss's list has no name: its own is its session id
			own := s.Boss()
			if own == "" {
				_, own, _ = todoBoss(*boss)
				own = strings.TrimPrefix(own, t3Boss)
			}
			switch {
			case waiting:
				if it.On, err = waitingFor(person, own, *boss, stderr); err != nil {
					return err
				}
			case strings.EqualFold(it.On, todo.Me):
				it.On = todo.Me
			case strings.EqualFold(it.On, todo.Boss):
				it.On = todo.Boss
				fmt.Fprintln(stderr, "on boss: your own task, to do until you work on it")
			case it.On != "":
				// A worker of this boss's is linked, so its board shows the worker's state, or that it's gone
				ws, err := todoWorkers(*boss, stderr)
				if err != nil {
					return err
				}
				if w, ok := todo.Match(it.On, ws); ok {
					it.On, it.Session = w.Name, &w
				} else if own != "" && strings.EqualFold(it.On, own) {
					fmt.Fprintf(stderr, "on %s: this list's own boss, so your own task, as --on boss\n", it.On)
					it.On = todo.Boss
				} else {
					fmt.Fprintf(stderr, "no session %s: a person is --waiting-for %s, your own task --on boss\n", it.On, it.On)
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
			// Only once all is checked: a desktop boss's first call takes over an earlier list
			if err := tryAdoptable(*boss, func(t todo.Store) error {
				t = t.NeedDetails()
				if cmd.Flags().Changed("replaces") {
					return t.Replace(replaces, it, now)
				}
				_, err := t.Add(it, now)
				return err
			}); err != nil {
				return err
			}
			if s, err = todoStore(*boss, true, stderr); err != nil {
				return err
			}
			s = s.NeedDetails()
			if cmd.Flags().Changed("replaces") {
				if err := s.Replace(replaces, it, now); err != nil {
					return err
				}
				warnLong(cmd.ErrOrStderr(), it.Text)
				warnNoDetails(s, cmd.ErrOrStderr(), replaces, now)
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
			warnLong(cmd.ErrOrStderr(), it.Text)
			warnNoDetails(s, cmd.ErrOrStderr(), id, now)
			if !it.Quiet && (it.On == "" || it.On == todo.Me) {
				waitsOnYou(*boss, cmd.ErrOrStderr(), id, it.Text, nil)
			}
			baseline(s, cmd.ErrOrStderr())
			return printCount(cmd.OutOrStdout(), s, "added "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().StringVar(&it.On, "on", "", "who does it, when not the user: a session it started, worker or boss (its name or local_… id; its boards show its live state, or that it's gone); boss, or this boss's own name: the boss's own task; me: the user's own (with --replaces, gives it back); any other name: a person, as --waiting-for")
	cmd.Flags().StringVar(&person, "waiting-for", "", "the person it waits for, no session (a reviewer, a teammate)")
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	cmd.Flags().StringVar(&until, "until", "", `tick it by itself at this local time: 15:30 (today), 2026-11-16 (that day's end), "2026-11-16 10:00"`)
	cmd.Flags().StringVar(&it.DoneOn, "done-on", "", "tick it by itself once its --link GitHub PR is merged, approved or green (checks passed), its first lookup included, and tell the boss; only this flag does, never the item's text")
	cmd.Flags().StringArrayVar(&it.Context, "context", nil, "a line of context under the item, in order (with --replaces, replaces its context)")
	cmd.Flags().StringVar(&it.Details, "details", "", detailsHelp)
	cmd.Flags().IntVar(&replaces, "replaces", 0, "put it in place of open item n, keeping n's number, place and the text, --on, --link, --until, --done-on, --context, --details, --part-of not given, and its doing line unless --on changes")
	cmd.Flags().BoolVar(&it.NoDetails, "no-details", false, "an item the user does that needs no details, kept on it (never marked or counted as missing them) while the user does it without details (--details, --on or --waiting-for drop it); a waiting item still warns: without it or that mark, one the user does with none (none given, none kept by --replaces) is refused")
	cmd.Flags().BoolVar(&it.Quiet, "quiet", false, quietHelp)
	cmd.Flags().IntVar(&it.PartOf, "part-of", 0, "the other half of a task split between two assignees: open item n, not linked yet; both show the link until one leaves the list")
	return cmd
}

func newTodoAskCmd(boss *string) *cobra.Command {
	var it todo.Item
	var replaces int
	var decided string
	cmd := &cobra.Command{
		Use:   "ask [<question>]",
		Short: "Put a decision on the list for the user, with the options to pick from; prints its number",
		Long: `Put a decision on the list for the user, with its options (the recommended one marked "(Recommended)"),
and go on: the boss doesn't wait for it. The question is at most 100 characters, its findings go in --context lines;
an option is "<label> | <detail>", its label at most 30 characters. The user answers with dev todo answer (a pick or their own words), which
ticks it and sends the boss "you answered #<n> \"<question>\": <answer>". --replaces <n> corrects open ask n in place,
keeping its number: a pick the user made before it still answers it while the question and the option picked stay.
--for <n> links it to open item n that waits on its answer: n's card and opened item show "waits on your answer to
#<ask>" with its options, an answer there answering the ask; --replaces keeps the link.
--obvious '<why>' marks an ask with one recommended option whose others make little sense, and that is no matter of
money, the usage limit, scope, outward-facing, hard-to-undo steps, secrets or taste: the manager may settle it with
that option (dev todo answer --recommended). --decided '<why>' is an easy decision the boss settles itself: the ask goes
on the list ticked at once with its recommended option, decided for the user by the boss, for them to overrule.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			replace := cmd.Flags().Changed("replaces")
			switch {
			case len(args) == 1:
				if it.Text = todo.Clean(args[0]); it.Text == "" {
					return errors.New("empty question")
				}
			case !replace:
				return errors.New("no question: only --replaces keeps the ask's question")
			case len(it.Options) == 0 && len(it.Context) == 0 && it.Link == "" && !it.Quiet && !cmd.Flags().Changed("details") && !cmd.Flags().Changed("for") && !cmd.Flags().Changed("obvious"):
				return errors.New("--replaces: nothing to correct: give the question, --option, --context, --details, --link, --for, --quiet or --obvious")
			}
			if it.For < 0 {
				return fmt.Errorf("--for %d: an item's number, or 0 to unlink", it.For)
			}
			markObvious, markDecided := cmd.Flags().Changed("obvious"), cmd.Flags().Changed("decided")
			marks := markObvious || markDecided
			switch {
			case marks && *boss != "":
				return errors.New("only the list's own boss marks or decides its asks: leave out --boss")
			case markObvious && markDecided:
				return errors.New("--obvious leaves the ask to the manager, --decided settles it now: give one")
			case markDecided && (replace || cmd.Flags().Changed("for")):
				return errors.New("--decided puts a new ask on the list ticked: no --replaces or --for")
			}
			it.Obvious, decided = todo.Clean(it.Obvious), todo.Clean(decided)
			switch {
			case markObvious && it.Obvious == todo.NotObvious && !replace:
				return errors.New("--obvious - removes the mark of an ask: only with --replaces")
			case markObvious && it.Obvious == "":
				return errors.New("--obvious needs why the ask is obvious (with --replaces, - removes the mark)")
			case markDecided && decided == "":
				return errors.New("--decided needs why the decision is easy")
			}
			if replace && cmd.Flags().Changed("for") && it.For == 0 {
				it.For = todo.NoFor
			}
			if len(it.Options) == 0 && !replace {
				return errors.New(`no --option: give the user the options to pick from, the recommended one marked "(Recommended)"`)
			}
			if len(it.Options) > todo.MaxOptions {
				return fmt.Errorf("%d options, at most %d", len(it.Options), todo.MaxOptions)
			}
			var labels []string
			for i, o := range it.Options {
				c := todo.Split(todo.Clean(o))
				if c.Label == "" {
					return fmt.Errorf("option %d is empty", i+1)
				}
				if slices.ContainsFunc(labels, func(x string) bool { return strings.EqualFold(x, c.Label) }) {
					return fmt.Errorf("option %d repeats %q", i+1, c.Label)
				}
				labels, it.Options[i] = append(labels, c.Label), c.String()
			}
			// A replace keeping its options is checked against them in the store
			if len(it.Options) > 0 && it.Obvious != todo.NotObvious {
				if err := todo.CheckObvious(it.Obvious+decided, it.Options); err != nil {
					return err
				}
			}
			context, err := contextLines(it.Context)
			if err != nil {
				return err
			}
			it.Context = context
			if it.Details, err = detailsFlag(cmd, it.Details); err != nil {
				return err
			}
			now := time.Now()
			if decided != "" {
				s, err := todoStore(*boss, true, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				id, err := s.Decide(it, decided, now)
				if err != nil {
					return err
				}
				baseline(s, cmd.ErrOrStderr())
				return printCount(cmd.OutOrStdout(), s, "decided "+strconv.Itoa(id)+" · ", now)
			}
			// Only once all is checked: a desktop boss's first call takes over an earlier list
			if replace || it.For != 0 {
				if err := tryAdoptable(*boss, func(t todo.Store) error {
					if !replace {
						_, err := t.Add(it, now)
						return err
					}
					_, err := t.ReplaceAsk(replaces, it, now)
					return err
				}); err != nil {
					return err
				}
			}
			s, err := todoStore(*boss, true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if replace {
				renamed, err := s.ReplaceAsk(replaces, it, now)
				if err != nil {
					return err
				}
				warnNoDetails(s, cmd.ErrOrStderr(), replaces, now)
				// Only a new question is news to the boss above: it reads the rest from the list
				if renamed {
					replacedWaits(s, *boss, cmd.ErrOrStderr(), replaces, now)
				}
				baseline(s, cmd.ErrOrStderr())
				return printCount(cmd.OutOrStdout(), s, "replaced "+strconv.Itoa(replaces)+" · ", now)
			}
			id, err := s.Add(it, now)
			if err != nil {
				return err
			}
			warnNoDetails(s, cmd.ErrOrStderr(), id, now)
			if !it.Quiet {
				waitsOnYou(*boss, cmd.ErrOrStderr(), id, it.Text, it.Options)
			}
			baseline(s, cmd.ErrOrStderr())
			return printCount(cmd.OutOrStdout(), s, "asked "+strconv.Itoa(id)+" · ", now)
		},
	}
	cmd.Flags().IntVar(&replaces, "replaces", 0, "correct open ask n in place, keeping its number and the question, options, --context, --details, --link not given (and --quiet once set)")
	cmd.Flags().StringArrayVar(&it.Options, "option", nil, `an option, in order, up to 9: "<label> | <detail>", the label at most 30 characters (what an answer names), the detail its cost or effect; the recommended one ends with "(Recommended)"`)
	cmd.Flags().StringArrayVar(&it.Context, "context", nil, "a line of context under the question, in order: findings, then paths")
	cmd.Flags().StringVar(&it.Details, "details", "", detailsHelp)
	cmd.Flags().StringVar(&it.Link, "link", "", "a link for it, e.g. its PR's URL")
	cmd.Flags().BoolVar(&it.Quiet, "quiet", false, quietHelp)
	cmd.Flags().IntVar(&it.For, "for", 0, "open item n of this list that waits on this answer: its card and opened item show the ask's options (with --replaces, links it anew; 0 unlinks it)")
	cmd.Flags().StringVar(&it.Obvious, "obvious", "", "why the ask is obvious, one line: the manager may settle it with its one recommended option (kept by a --replaces that keeps the question and options; - removes it)")
	cmd.Flags().StringVar(&decided, "decided", "", "why the ask is an easy decision, one line: settled now by the boss with its one recommended option, ticked")
	return cmd
}

const detailsHelp = "its longer text: what it is and why, where it stands, what done looks like (for a waiting item also who, what was asked, when, where; expected on every item but a worker's or --quiet: add refuses one the user does without, unless --no-details, else a warning); its steps, what's out of scope; for a message to send (mail, chat, comment), its state on the first line (\"Draft ready, not sent\" or \"Sent <when> to <whom>, <where>\"), then the draft; at most 4000 characters, a blank line between paragraphs, \"- \" lines for a list; - reads it from stdin (with --replaces, replaces its details)"

const maxDetailsBytes = 64 << 10

// detailsFlag is --details as stored (todo.CleanDetails), read from stdin for -; given but empty is an error.
func detailsFlag(cmd *cobra.Command, raw string) (string, error) {
	if !cmd.Flags().Changed("details") {
		return "", nil
	}
	stdin := raw == "-"
	if stdin {
		// Bytes enough for MaxDetails characters, however spaced: a log piped in by mistake is refused, not read whole
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxDetailsBytes+1))
		switch {
		case err != nil:
			return "", fmt.Errorf("--details -: %w", err)
		case len(b) > maxDetailsBytes:
			return "", fmt.Errorf("--details -: stdin is over %d bytes, at most %d characters kept", maxDetailsBytes, todo.MaxDetails)
		}
		raw = string(b)
	}
	switch raw = todo.CleanDetails(raw); {
	case raw == "" && stdin:
		return "", errors.New("--details -: stdin is empty")
	case raw == "":
		return "", errors.New("--details is empty")
	}
	// Before any store opens: a desktop boss's first write takes over an earlier list
	return raw, todo.CheckDetails(raw)
}

const quietHelp = "a test item: a TEST tag on the boards, left out of their tallies and roll-ups (a column still lists it), the boss above not told; kept by --replaces"

func newTodoAnswerCmd(boss *string) *cobra.Command {
	var question, via string
	var fromBoard, recommended bool
	cmd := &cobra.Command{
		Use:   "answer <boss> <n> <option|text>",
		Short: "Answer item n on a dev boss's list, as the user: ticks it and sends the boss what was answered",
		Long: `Answer ask n (dev todo ask) on the list of dev boss session <boss>, as the user: an option's number or label, or your own
words. It ticks the item and the boss's inbox gets "you answered #<n> \"<question>\": <answer>", which the boss
takes as the user's answer. Refused in a Claude Code session ($CLAUDECODE set), so no Claude answers for the user,
and for a desktop app boss, which has no dev inbox: answer it in its chat. --question refuses the answer unless item n
still reads that text (item numbers restart once a list empties). --via manager is the manager relaying what the user
told it, only from their pick in the manager's AskUserQuestion: only in the manager's coordinator pane, for an item
older than that pick, once; its one question must start "<project> #<n>: " and contain --question, the answer must be
the option picked (or what they typed under Other), and the note quotes it: "you answered #<n> \"<question>\" via
manager (\"<pick>\"): <answer>". The pick is the user's latest one, typed messages after it or not; a typed message
is never the answer: ask the item as a question instead. An
item the user answered meanwhile elsewhere sends nothing, saying who answered what where: an error only for another
answer. Answered other than via manager, a boss under a boss tells the one above ("ask answered").
--recommended --via manager is the manager settling an ask its boss marked obvious (dev todo ask --obvious) with its
recommended option, no pick needed: the answer names that option as the manager checked it, refused once it is no longer
the recommended one; only from the manager's coordinator pane, never an unmarked ask, the manager's own, a permission
prompt or a dialog. The item records it as decided for the user by the manager, and the
boss's note says so: "you answered #<n> \"<question>\" settled by the manager with your recommended option: <answer>".`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			switch {
			case via != "" && via != inbox.Manager:
				return fmt.Errorf("--via %s: only --via %s", via, inbox.Manager)
			case recommended && via == "":
				return errors.New("--recommended is the manager settling a marked ask: needs --via manager")
			case len(args) == 2 && recommended:
				return errors.New("--recommended needs the recommended option you checked as the answer")
			case len(args) == 2:
				return errors.New("no answer: give an option's number or label, or your own words")
			case recommended && question == "":
				return errors.New("--recommended needs --question with the item's text")
			case recommended && name == inbox.Manager:
				return errors.New("--recommended settles a boss's marked ask, never the manager's own: those are the user's")
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
			if recommended {
				if _, err := strconv.Atoi(args[2]); err == nil {
					return errors.New("--recommended: name the recommended option by its label, not its number (options may move)")
				}
				return settleAsk(c, cmd.OutOrStdout(), cmd.ErrOrStderr(), name, id, question, args[2])
			}
			var manager inbox.Boss
			var said *inbox.Prompt
			var before int64
			if via != "" {
				if manager, said, err = managerPrompt(c); err != nil {
					return err
				}
				if err := pickFor(said, name, id, question); err != nil {
					return err
				}
				before = said.At
			}
			var claim func() (string, func() error, error)
			if said != nil {
				// A pick answers one item, also with two answers relayed at once
				claim = func() (string, func() error, error) {
					if err := manager.Box.ClaimPick(*said); err != nil {
						return "", nil, fmt.Errorf("--via manager: %w", err)
					}
					return said.Answer, func() error { return manager.Box.Unclaim(*said) }, nil
				}
			}
			where := ""
			if said != nil {
				where = todo.ViaManager
			}
			it, b, pane, err := answerTick(c, name, id, question, args[2], where, before, claim)
			var already todo.Answered
			if said != nil && errors.As(err, &already) && already.Same {
				// The user answered it elsewhere while the manager's question was up, the same way
				fmt.Fprintf(cmd.OutOrStdout(), "#%d not sent to %s: %v\n", id, name, already)
				return nil
			}
			if err != nil {
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
			if said == nil {
				tellAbove(c, pane, it, note, func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev todo: "+s) })
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d answered, sent to %s\n", id, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when item n reads otherwise")
	cmd.Flags().BoolVar(&fromBoard, "from-board", false, "a desktop board's button (needs --question): allowed in a Claude Code session, as the board runs inside one")
	_ = cmd.Flags().MarkHidden("from-board")
	cmd.Flags().StringVar(&via, "via", "", "manager: the manager relaying the user's pick in its AskUserQuestion (only in its coordinator pane)")
	cmd.Flags().BoolVar(&recommended, "recommended", false, "with --via manager: settle an ask its boss marked obvious with its recommended option, as the manager")
	return cmd
}

// managerPrompt is the manager and the user's latest pick in it, typed messages since or not, else their latest
// prompt to it (a typed message), for an answer relayed from its coordinator pane.
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
	pick, err := self.Box.LatestPick()
	if err != nil || pick != nil {
		return self, pick, err
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

// pickFor refuses the user's latest pick to the manager (managerPrompt) unless it is one question asking item id of
// boss, question, that didn't leave it open; a typed message, when they picked nothing, can't be tied to an item.
func pickFor(pick *inbox.Prompt, boss string, id int, question string) error {
	asked := strings.ToLower(pick.Question)
	item := fmt.Sprintf(" #%d: ", id)
	named := strings.HasPrefix(asked, strings.ToLower(boss)+item) || strings.HasPrefix(asked, strings.ToLower(strings.TrimPrefix(boss, "boss-"))+item)
	switch {
	case !pick.Pick:
		return errors.New("--via manager: the user's latest words here are a typed message, not a pick: ask the item as a one-click question (AskUserQuestion; they can still type under Other)")
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
	var text, result string
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
			if cmd.Flags().Changed("result") {
				if len(ids) != 1 {
					return errors.New("--result ticks one item: say what was done for each")
				}
				var err error
				if result, err = todo.CheckResult(result); err != nil {
					return err
				}
			}
			s, _, err := listStore(*boss, true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			s = s.WithResult(result)
			// Outside Claude Code and a T3 Code thread: the user ran it
			if os.Getenv("CLAUDECODE") == "" && os.Getenv("T3CODE_THREAD_ID") == "" {
				s = s.ByUser()
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
	cmd.Flags().StringVar(&result, "result", "", "what was done, one line of at most 300 characters, kept on the ticked item: what, to whom, when, the id or link that proves it")
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
			s, closed, err := listStore(*boss, true, cmd.ErrOrStderr())
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
				printItem(out, "", it, now)
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

func newTodoNoteCmd(boss *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Print the boss's notes: decisions with their why, what the user said, open risks",
		Long: fmt.Sprintf(`Notes are short dated lines a boss keeps with its list, outliving its /clear and its session: dev todo note
decided|said|risk|state "<text>" adds one, dev todo note drop <n> removes one, without a subcommand they are printed.
At most %d notes of at most %d characters each. decided: a choice, --why says why; said: what the user told the boss
that changes the work, it needs an end (--until or --item); risk: an open risk to watch; state: the boss's standing
state, kept off the board ("6 fixes parked for after your testing"), one per list: a new one replaces it. Not actions (dev todo add), not
standing rules (CLAUDE.md, memory). A note with --until drops itself then; one with --item n is flagged once item n is done.`, todo.MaxNotes, todo.MaxNote),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, _, err := listStore(*boss, false, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			now := time.Now()
			notes, err := s.Notes(now)
			if err != nil {
				return err
			}
			hintT3(cmd, *boss, s, now)
			text := todo.NotesText(notes, now)
			if text == "" {
				text = "no notes"
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), text)
			return err
		},
	}
	cmd.AddCommand(newTodoNoteDropCmd(boss))
	for _, kind := range []string{todo.NoteDecided, todo.NoteSaid, todo.NoteRisk, todo.NoteState} {
		cmd.AddCommand(newTodoNoteAddCmd(boss, kind))
	}
	return cmd
}

func newTodoNoteAddCmd(boss *string, kind string) *cobra.Command {
	var n todo.Note
	var until string
	cmd := &cobra.Command{
		Use:   kind + ` "<text>"`,
		Short: "Add a " + kind + " note; prints its number",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n.Kind, n.Text, n.Why = kind, todo.Clean(args[0]), todo.Clean(n.Why)
			now := time.Now()
			if until != "" {
				t, err := todo.ParseUntil(until, now)
				if err != nil {
					return err
				}
				n.Until = t.Unix()
			}
			// Refused before the store: a desktop boss's first call takes over an earlier list
			if err := n.Check(); err != nil {
				return err
			}
			s, err := todoStore(*boss, true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			id, err := s.AddNote(n, now)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "noted n%d\n", id)
			return err
		},
	}
	cmd.Flags().StringVar(&n.Why, "why", "", "why, one line")
	cmd.Flags().StringVar(&until, "until", "", `drop it by itself at this local time: 15:30 (today), 2026-11-16 (that day's end), "2026-11-16 10:00"`)
	cmd.Flags().IntVar(&n.Item, "item", 0, "tie it to open item n: flagged once n is done or its list is replaced")
	return cmd
}

func newTodoNoteDropCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "drop <n>",
		Short: "Remove note n (n3 or 3)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(args[0]), "n"))
			if err != nil {
				return fmt.Errorf("note number %q: %w", args[0], err)
			}
			s, err := todoStore(*boss, false, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := s.DropNote(id, time.Now()); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "dropped n%d\n", id)
			return err
		},
	}
}

func newTodoClearCmd(boss *string) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Empty the list: its items leave the board's TO DO",
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
	lists, err := todoAll(c, stderr, now, asJSON, false, asJSON)
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
		printBossList(out, bl, now)
	}
	return nil
}

// printBossList prints bl's rows for --all, each after its boss: a row for a list not read, then its items.
func printBossList(out io.Writer, bl bossList, now time.Time) {
	if bl.Error != "" {
		fmt.Fprintln(out, bl.Boss+"\t⚠ "+bl.Error)
	}
	for _, it := range bl.Items {
		printItem(out, bl.Boss+"\t", ofClosed(it, bl.Closed), now)
	}
}

// ofClosed is it as printed on a list whose boss is closed or not: no one is on a closed boss's own item, it's To do,
// as its column says.
func ofClosed(it todo.Shown, closed bool) todo.Shown {
	if closed && todo.OnBoss(it.On, it.Session) {
		it.Doing = nil
	}
	return it
}

// todoAll is printAll's lists, never nil: with asJSON each item's status set (dev todo --all --json, dev serve's
// board), else its gone marked; with finished, the lists with only finished items too (todo.All).
func todoAll(c *tmux.Client, stderr io.Writer, now time.Time, asJSON, withNow, finished bool) ([]bossList, error) {
	all, err := todo.All(c, now, nil, true, finished)
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
		setStatuses(c, lists, now, withNow, stderr)
	}
	markAsking(c, lists, now, stderr)
	return lists, nil
}

// markAsking marks the open ask whose question the manager has up now (inbox.ManagerAsking), still reading what it
// asked; a failure is only reported, the lists hold without it.
func markAsking(c *tmux.Client, lists []bossList, now time.Time, stderr io.Writer) {
	a, err := inbox.ManagerAsking(c, now)
	if err != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: the manager's question not read: "+err.Error())
	}
	if a == nil {
		return
	}
	for _, bl := range lists {
		if bl.Boss != a.Boss {
			continue
		}
		for i, it := range bl.Items {
			if it.ID == a.ID && it.Text == a.Text && !it.Done && len(it.Options) > 0 {
				bl.Items[i].Asking = true
			}
		}
	}
}

// waitsOnYou tells the boss above the list's boss (a boss under a boss) that item id waits on the user; a failure
// is only reported, as the item is on the list.
func waitsOnYou(boss string, stderr io.Writer, id int, text string, options []string) {
	pane, _, err := todoBoss(boss)
	queued := false
	var noState error
	if err == nil && pane != "" {
		queued, noState, err = inbox.WaitsOn(tmux.New(), control.RealClock, pane, id, text, options)
	}
	if noState != nil {
		fmt.Fprintln(stderr, "⚠ dev todo: the boss above's note lacks the item's state: "+noState.Error())
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
	if l.Items[i].On == "" && !l.Items[i].Quiet {
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
	case strings.HasPrefix(id, t3Boss):
		s, err := todo.T3(strings.TrimPrefix(id, t3Boss))
		if err != nil {
			return s, err
		}
		// Only its own thread runs this: --boss names no T3 Code thread
		dir, err := mainCheckout()
		return s.WithDir(dir), err
	}
	return desktopTodo(id, adopt && boss == "", stderr)
}

// listStore is todoStore, else, for a --boss closed on this server with open items, its list by its file: closed
// reports it; one not read adds its error. Any other --boss keeps todoStore's error, a live session that is no boss too.
func listStore(boss string, adopt bool, stderr io.Writer) (s todo.Store, closed bool, err error) {
	return listStoreOf(boss, adopt, false, stderr)
}

// listStoreOf is listStore; with finished a closed boss's list with only ticked or finished items comes too.
func listStoreOf(boss string, adopt, finished bool, stderr io.Writer) (s todo.Store, closed bool, err error) {
	s, err = todoStore(boss, adopt, stderr)
	if err == nil || boss == "" || strings.HasPrefix(boss, "local_") {
		return s, false, err
	}
	gone, ok, cerr := todo.ClosedStore(tmux.New(), boss, time.Now(), finished)
	switch {
	case cerr != nil:
		return s, false, errors.Join(err, unreadList{cerr})
	case !ok:
		return s, false, err
	}
	return gone, true, nil
}

// unreadList is listStore's error for closed lists not read: not a missing list.
type unreadList struct{ error }

func (e unreadList) Unwrap() error { return e.error }

// t3Boss starts todoBoss's id for a T3 Code thread, before the thread's id.
const t3Boss = "t3:"

// t3Child starts the id of a thread T3 Code started for another's delegate_task: a worker, not a boss.
const t3Child = "thread:delegated-task:"

var errT3Child = errors.New("a task another T3 Code thread delegated: the to do list is its boss's")

// todoBoss is the boss session whose list it is: --boss's, else the one it runs in; a pane in a tmux one, else
// a desktop app session's id, else a T3 Code thread's id after t3Boss.
func todoBoss(boss string) (pane, id string, err error) {
	switch {
	case strings.HasPrefix(boss, "local_"):
		return "", boss, nil
	case boss != "":
		pane, err := control.BossPane(tmux.New(), boss)
		return pane, "", err
	}
	// First: a T3 Code started in tmux passes its pane to every thread
	if id := os.Getenv("T3CODE_THREAD_ID"); id != "" {
		if strings.HasPrefix(id, t3Child) {
			return "", "", fmt.Errorf("%s is %w", id, errT3Child)
		}
		return "", t3Boss + id, nil
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return pane, "", nil
	}
	if id := os.Getenv("CLAUDE_CODE_HOST_SESSION_ID"); id != "" {
		return "", id, nil
	}
	return "", "", errors.New("no boss session: run it in a dev boss session, a desktop app session or a T3 Code thread, or pass --boss <session|local_id>")
}

// todoWorkers is the workers of the boss whose list it is: tmux sessions it started, or desktop app sessions it
// spawned, not archived (none for a T3 Code thread: dev can't see its threads); records that can't be read are
// reported on stderr.
func todoWorkers(boss string, stderr io.Writer) ([]todo.Session, error) {
	pane, id, err := todoBoss(boss)
	switch {
	case err != nil:
		return nil, err
	case pane != "":
		return todo.Workers(tmux.New(), pane)
	case strings.HasPrefix(id, t3Boss):
		return nil, nil
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

// tryAdoptable runs try on a trial (todo.Store.Trial) of the earlier list a desktop app boss's first write takes over
// (desktopTodo), so a refused write takes over nothing; nil for --boss or a tmux boss's list, or none taken over. One
// the lookup can't tell is left to desktopTodo, which says why.
func tryAdoptable(boss string, try func(todo.Store) error) error {
	pane, id, err := todoBoss(boss)
	if err != nil || pane != "" || boss != "" || strings.HasPrefix(id, t3Boss) {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths := desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	s, err := todo.Desktop(id)
	if err != nil {
		return err
	}
	from, ok, _ := s.Adoptable(func() (func(string) bool, error) { return desktop.Earlier(paths, id) })
	if !ok {
		return nil
	}
	return try(from.Trial())
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

// printItem prints it at now (todoLine), then, while open, its context lines indented, each line after prefix.
func printItem(out io.Writer, prefix string, it todo.Shown, now time.Time) {
	fmt.Fprintln(out, prefix+todoLine(it, now))
	if it.Done {
		return
	}
	for _, c := range it.Context {
		fmt.Fprintln(out, prefix+"    - "+c)
	}
}

// todoLine is an item as text at now: number, ☐ (◌ waiting on someone else than the boss, ☑ ticked), text, who it waits on, link,
// who is on it (OnIt), why to re-check it.
func todoLine(it todo.Shown, now time.Time) string {
	parts := []string{strconv.Itoa(it.ID), it.Box(), it.Text}
	switch {
	case it.Gone:
		parts = append(parts, "on "+it.On+", session gone")
	case todo.OnBoss(it.On, it.Session):
		parts = append(parts, "boss's own")
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
	// Read by other bosses and the manager too (--all): no "you"
	if on := it.OnIt("the boss is on it"); on != "" {
		parts = append(parts, on)
	}
	switch {
	case it.Decided != nil:
		parts = append(parts, decidedLine(*it.Decided))
	case it.Answer != "":
		parts = append(parts, "answered: "+it.Answer)
	case len(it.Options) > 0:
		parts = append(parts, "options: "+strings.Join(it.Options, " · "))
	}
	if it.Obvious != "" && !it.Done {
		parts = append(parts, "obvious: "+it.Obvious)
	}
	if it.Check != nil {
		parts = append(parts, "⚠ "+it.Check.Long(now))
	}
	return strings.Join(parts, "\t")
}

// decidedLine is an ask decided for the user as text: "decided for you: manager, Merge over Wait · Hold at 14:32".
func decidedLine(d todo.Decided) string {
	line := "decided for you: " + d.By + ", " + todo.Split(d.Pick).Label
	if len(d.Over) > 0 {
		line += " over " + strings.Join(d.OverLabels(), " · ")
	}
	return line + " at " + time.Unix(d.At, 0).Format("15:04")
}

// contextLines is each --context line cleaned (todo.Clean); an empty one is an error.
func contextLines(raw []string) ([]string, error) {
	for i, l := range raw {
		if raw[i] = todo.Clean(l); raw[i] == "" {
			return nil, fmt.Errorf("--context line %d is empty", i+1)
		}
	}
	return raw, nil
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
