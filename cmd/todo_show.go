package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/itemview"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// showGuard marks dev todo show running inside its popup, so it draws there instead of opening another.
const showGuard = "DEV_TODO_SHOW_IN_POPUP"

func newTodoShowCmd(boss *string) *cobra.Command {
	var client, question string
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "show <boss> <n>",
		Short: "Show item n of a dev boss's list in a popup, as the user: answer it, tick, start, mark it in progress, reopen it, open its link, go to its boss or worker",
		Long: `Show item n on the list of dev boss session <boss> in a tmux popup sized to it, centred on the client: its text,
context, options or actions, status, link and list. An ask is answered with 1-9, j/k and ⏎, or m in your own words
(as dev todo answer); your own item is started (s, as dev todo start) or ticked (d, as dev todo done), your own To do
card marked in progress with a line of what you do now (i, as dev todo doing; esc cancels) or, with a doing line,
stopped (i again), your own ticked one reopened (r, as dev todo reopen), or, decided for you, overruled with one of
the other options by its number (as dev todo overrule); o opens
its link, w goes to the worker it's on, g to its boss; esc closes. The boards' and the sidebar's TO DO open it with space.
Item n is the open one, else the newest ticked in the last day, as Done shows it. --question refuses it unless item n
still reads that text (item numbers restart once a list empties). --print prints it instead, no popup: its line as
dev todo has it (number, mark, text, on whom when not you, answer), an open one's context, its details, as the manager
checks one item.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("show takes a boss and an item number, got %d argument(s)\nusage: %s", len(args), cmd.UseLine())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("the boss is show's first argument: leave out --boss")
			}
			name := args[0]
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w\nusage: %s", args[1], err, cmd.UseLine())
			}
			c := tmux.New()
			it, err := showItem(c, cmd.ErrOrStderr(), name, id, question)
			if err != nil {
				return err
			}
			if printOnly {
				// As the text list marks it: --json's status has it, MarkGone's flag doesn't
				it.Gone = it.Status == todo.StatusGone
				printShown(cmd.OutOrStdout(), ofClosed(it.Shown, it.Closed), it.Closed, time.Now())
				return nil
			}
			if client == "" {
				if client, err = c.Run("display-message", "-p", "#{client_name}"); err != nil {
					return err
				}
				client = strings.TrimSpace(client)
			}
			_, managerErr := c.Run("has-session", "-t", tmux.SessionTarget(sidebar.Manager))
			m := itemview.New(it, showActions(c, client), managerErr == nil)
			if os.Getenv(showGuard) == "" {
				return showPopup(c, client, m, name, id, it.Text)
			}
			p, err := tea.NewProgram(showModel{m: m}, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
			if err != nil {
				return err
			}
			if res := p.(showModel).m.Result; res != "" {
				// The popup closes: the client's status line says what went
				_, _ = c.Run("display-message", "-c", client, res)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "the tmux client to show it on and switch: tmux's current one by default")
	cmd.Flags().StringVar(&question, "question", "", "the item's text: refuse when item n reads otherwise")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the item as text instead of the popup")
	return cmd
}

// printShown prints it at now as dev todo show --print does: its line (todoLine, with boss closed), an open one's
// context and details, indented.
func printShown(out io.Writer, it todo.Shown, closed bool, now time.Time) {
	line := todoLine(it, now)
	if closed {
		// Its boss can't take an answer: dev todo answer refuses one
		line += "\tboss closed"
	}
	fmt.Fprintln(out, line)
	if !it.Done {
		for _, c := range it.Context {
			fmt.Fprintln(out, "    - "+c)
		}
	}
	if it.Details != "" && !it.Done {
		for _, l := range strings.Split(it.Details, "\n") {
			fmt.Fprintln(out, strings.TrimRight("    "+l, " "))
		}
	}
}

// showItem is item id of boss name's list as dev todo --all --json has it, its status set: the open one, else the newest
// ticked one (still on the list, or among its finished items, as the boards' Done shows them); refused when it's gone
// or reads other than question.
func showItem(c *tmux.Client, stderr io.Writer, name string, id int, question string) (itemview.Item, error) {
	lists, err := todoAll(c, stderr, time.Now(), true, false, true)
	if err != nil {
		return itemview.Item{}, err
	}
	l := slices.IndexFunc(lists, func(bl bossList) bool { return bl.Boss == name })
	if l < 0 {
		return itemview.Item{}, fmt.Errorf("%s has no to do list", name)
	}
	bl := lists[l]
	if bl.Error != "" {
		return itemview.Item{}, fmt.Errorf("%s: %s", name, bl.Error)
	}
	reads := func(text string) bool { return question == "" || text == question }
	i := slices.IndexFunc(bl.Items, func(it todo.Shown) bool { return it.ID == id && !it.Done })
	if i >= 0 && reads(bl.Items[i].Text) {
		return itemview.Item{Boss: name, Closed: bl.Closed, Shown: bl.Items[i]}, nil
	}
	// Ticked ones: still on the list first, then the finished ones, newest first
	if j := slices.IndexFunc(bl.Items, func(it todo.Shown) bool { return it.ID == id && it.Done && reads(it.Text) }); j >= 0 {
		return itemview.Item{Boss: name, Closed: bl.Closed, Shown: bl.Items[j]}, nil
	}
	if j := slices.IndexFunc(bl.Finished, func(f todo.Finished) bool { return f.ID == id && reads(f.Text) }); j >= 0 {
		f := bl.Finished[j]
		return itemview.Item{Boss: name, Closed: bl.Closed, DoneAt: f.DoneAt, Shown: f.Shown()}, nil
	}
	if i >= 0 {
		return itemview.Item{}, fmt.Errorf("%s's #%d is %q now, not %q", name, id, bl.Items[i].Text, question)
	}
	return itemview.Item{}, fmt.Errorf("%s has no item #%d, open or ticked in the last day", name, id)
}

// showActions are the view's keys' paths: the boards' dev todo answer, done, start, reopen, overrule and doing, the
// browser, and a switch of client.
func showActions(c *tmux.Client, client string) itemview.Actions {
	return itemview.Actions{
		Answer:   sidebar.AnswerNeed,
		Tick:     sidebar.TickNeed(sidebar.WhereBoard),
		Start:    sidebar.StartNeed,
		Reopen:   sidebar.ReopenNeed,
		Overrule: sidebar.OverruleNeed,
		Doing:    sidebar.DoingNeed,
		Open: func(url string) error {
			// A link is the boss's text: never an option to open
			if strings.HasPrefix(url, "-") {
				return fmt.Errorf("not a link: %q", url)
			}
			return exec.Command("open", url).Run()
		},
		Switch: func(session string) error {
			_, err := c.Run("switch-client", "-c", client, "-t", tmux.SessionTarget(session))
			return err
		},
	}
}

// showPopup re-runs dev todo show for item id in a borderless popup on client (the view draws its own), as wide and
// tall as the view, cut to the client.
func showPopup(c *tmux.Client, client string, m itemview.Model, boss string, id int, text string) error {
	size, err := c.Run("display-message", "-c", client, "-p", "#{client_width} #{client_height}")
	if err != nil {
		return err
	}
	var cw, ch int
	if _, err := fmt.Sscan(size, &cw, &ch); err != nil {
		return fmt.Errorf("client size %q: %w", strings.TrimSpace(size), err)
	}
	w := min(itemview.Width, cw)
	h := min(m.Height(w, time.Now()), ch)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	command := tmux.ShellJoin(self, "todo", "show", "--client", client, "--question", text, "--", boss, strconv.Itoa(id))
	return popup(c, client, strconv.Itoa(w), strconv.Itoa(h), showGuard, command)
}

// popup runs command in a borderless popup on client, w by h (cells or a percentage), guard set in its environment.
func popup(c *tmux.Client, client, w, h, guard, command string) error {
	// Terminal's colours, as the views are made for: tmux 3.8's popup-style is mid grey on a light-theme client
	_, err := c.Run("display-popup", "-c", client, "-E", "-B", "-s", "bg=terminal,fg=terminal", "-w", w, "-h", h,
		"-e", guard+"=1", "--", command)
	// display-popup exits with the view's status, and the view, or the error that stopped it (Execute waits for a key),
	// showed in the popup
	if e, ok := errors.AsType[*tmux.Error](err); ok && e.Stderr == "" {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil
		}
	}
	return err
}

// showModel runs the view alone in its popup, as big as the popup, until it's done.
type showModel struct {
	m    itemview.Model
	w, h int
}

func (s showModel) Init() tea.Cmd { return nil }

func (s showModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		s.w, s.h = size.Width, size.Height
		return s, nil
	}
	var cmd tea.Cmd
	if ms, ok := msg.(tea.MouseMsg); ok {
		if ms.Action != tea.MouseActionPress || ms.Button != tea.MouseButtonLeft {
			return s, nil
		}
		s.m, cmd = s.m.Click(ms.X, ms.Y, s.w, s.h, time.Now())
	} else {
		s.m, cmd = s.m.Update(msg)
	}
	if s.m.Done {
		return s, tea.Quit
	}
	return s, cmd
}

func (s showModel) View() string {
	if s.w == 0 {
		return ""
	}
	return s.m.View(s.w, s.h, time.Now())
}
