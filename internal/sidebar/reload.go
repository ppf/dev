package sidebar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/tmux"
)

const (
	// reloadStagger spreads the sidebars' reloads so they don't redraw and hit tmux together
	reloadStagger = 5 * time.Second
	// reloadFlashFor is how long the bottom row shows the version a reload brought
	reloadFlashFor = 10 * time.Second
	maxExecFails   = 3
	rewatchAfter   = 10 * time.Minute
	// envReloadedTo is set by selfreload.Exec on the process it starts
	envReloadedTo = "DEV_RELOADED_TO"
)

// Test doubles for the watcher, the exec and the program's terminal.
var (
	watchReload    = selfreload.Watch
	execReload     = selfreload.Exec
	programOptions = func() []tea.ProgramOption { return []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()} }
)

// readyMsg is the watcher's Ready: a new release is installed.
type readyMsg selfreload.Ready

// reloadWarnMsg is a watcher check that failed.
type reloadWarnMsg struct{ err error }

// rewatchMsg starts watching again after a version was given up.
type rewatchMsg struct{}

// flashDoneMsg ends the bottom row's "↻ dev <version>".
type flashDoneMsg struct{}

// Run shows the sidebar in pane until the process is killed (its pane closes) or Ctrl-C. When a new release
// of exe is installed it re-execs itself with args at an idle point (same pid, same pane); an exec that fails
// keeps this version running, the error on the bottom row. It never returns to reload.
func Run(c *tmux.Client, pane, exe string, args []string) error {
	return run(newModel(c, pane), selfreload.Version(), exe, args)
}

func run(m Model, own, exe string, args []string) error {
	// One context for the process: the canary's ok mark runs in the first Watch for 30 s
	ctx := context.Background()
	for {
		var p *tea.Program
		o := selfreload.Options{Exe: exe, Own: own, Tmux: m.c, Canary: true, Stagger: reloadStagger,
			Warn: func(err error) { p.Send(reloadWarnMsg{err}) }}
		m.watch = func() tea.Cmd {
			ready := watchReload(ctx, o)
			return func() tea.Msg {
				if r, ok := <-ready; ok {
					return readyMsg(r)
				}
				return nil
			}
		}
		p = tea.NewProgram(m, programOptions()...)
		final, err := p.Run()
		if err != nil {
			return fmt.Errorf("sidebar: %w", err)
		}
		m = final.(Model)
		if m.reloadNow == nil {
			return nil
		}
		m = m.afterExec(execReload(*m.reloadNow, args))
	}
}

// afterExec is the sidebar after a failed exec, to run again on this version and watch again.
func (m Model) afterExec(err error) Model {
	v := m.reloadNow.Version
	m.reloadNow, m.ready = nil, nil
	// Another pane is the canary, or the kill switch went on: nothing to show
	if err == selfreload.ErrNotNow {
		return m
	}
	m.reloadErr = fmt.Errorf("reload: %w", err)
	// Only the exec itself counts: a tmux hiccup must not give up on a good release
	if _, ok := errors.AsType[syscall.Errno](err); !ok {
		return m
	}
	if v != m.failedTo {
		m.failedTo, m.fails = v, 0
	}
	m.fails++
	if m.fails >= maxExecFails {
		m.reloadErr = fmt.Errorf("reload to %s failed %d times, staying on this version: %w", v, m.fails, err)
	}
	return m
}

// watchCmd starts watching for a new release; none without a watch func.
func (m Model) watchCmd() tea.Cmd {
	if m.watch == nil {
		return nil
	}
	return m.watch()
}

// gaveUp:v's exec failed maxExecFails times; each try quits and redraws the program, so no more.
func (m Model) gaveUp(v string) bool {
	return v == m.failedTo && m.fails >= maxExecFails
}

// reloadedTo is the version a reload brought this process, "" when it wasn't started by one.
func reloadedTo(own string) string {
	if v := os.Getenv(envReloadedTo); v == own {
		return v
	}
	return ""
}

// idle: nothing the user started is in flight or open, so a reload loses nothing.
func (m Model) idle() bool {
	return !m.answering && !m.needMode && !m.needOpen && m.ticked == "" && m.running.load() == 0
}

// maybeReload quits the program to exec into the held Ready once the sidebar is idle.
func (m Model) maybeReload() (Model, tea.Cmd) {
	if m.ready == nil || !m.idle() {
		return m, nil
	}
	m.reloadNow = m.ready
	return m, tea.Quit
}

// counter counts the actions running off the update loop; nil-safe for models built in tests.
type counter struct{ n *atomic.Int32 }

func newCounter() counter { return counter{n: new(atomic.Int32)} }

func (c counter) load() int32 {
	if c.n == nil {
		return 0
	}
	return c.n.Load()
}

func (c counter) add(d int32) {
	if c.n != nil {
		c.n.Add(d)
	}
}

// runAction runs fn as a command, off the update loop since display-popup blocks until the popup closes,
// and flashes its error on client. It counts as running from now, so a reload waits for it.
func (m Model) runAction(client string, fn func() error) tea.Cmd {
	m.running.add(1)
	c := m.c
	return func() tea.Msg {
		defer m.running.add(-1)
		if err := fn(); err != nil {
			showMessage(c, client, "❌ "+err.Error())
		}
		return nil
	}
}
