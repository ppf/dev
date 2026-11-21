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
	cmd := &cobra.Command{
		Use:   "show <boss> <n>",
		Short: "Show item n of a dev boss's list in a popup, as the user: answer it, tick, start, open its link, go to its boss or worker",
		Long: `Show item n on the list of dev boss session <boss> in a tmux popup sized to it, centred on the client: its text,
context, options or actions, status, link and list. An ask is answered with 1-9, j/k and ⏎, or m in your own words
(as dev todo answer); your own item is started (s, as dev todo start) or ticked (d, as dev todo done), your own ticked
one on the board's Done reopened (r, as dev todo reopen); o opens its
link, w goes to the worker it's on, g to its boss; esc closes. The boards' and the sidebar's TO DO open it with space.
--question refuses it unless item n still reads that text (item numbers restart once a list empties).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("the boss is show's first argument: leave out --boss")
			}
			name := args[0]
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("item number %q: %w", args[1], err)
			}
			c := tmux.New()
			it, err := showItem(c, cmd.ErrOrStderr(), name, id, question)
			if err != nil {
				return err
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
	return cmd
}

// showItem is item id of boss name's list as dev todo --all --json has it, its status set; refused when it's gone,
// ticked, or reads other than question.
func showItem(c *tmux.Client, stderr io.Writer, name string, id int, question string) (itemview.Item, error) {
	lists, err := todoAll(c, stderr, time.Now(), true, false, false)
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
	i := slices.IndexFunc(bl.Items, func(it todo.Shown) bool { return it.ID == id && !it.Done })
	switch {
	case i < 0:
		return itemview.Item{}, fmt.Errorf("%s has no open item #%d", name, id)
	case question != "" && bl.Items[i].Text != question:
		return itemview.Item{}, fmt.Errorf("%s's #%d is %q now, not %q", name, id, bl.Items[i].Text, question)
	}
	return itemview.Item{Boss: name, Closed: bl.Closed, Shown: bl.Items[i]}, nil
}

// showActions are the view's keys' paths: the boards' dev todo answer, done and start, the browser, and a switch of
// client.
func showActions(c *tmux.Client, client string) itemview.Actions {
	return itemview.Actions{
		Answer: sidebar.AnswerNeed,
		Tick:   sidebar.TickNeed,
		Start:  sidebar.StartNeed,
		Reopen: sidebar.ReopenNeed,
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
