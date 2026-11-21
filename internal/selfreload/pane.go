package selfreload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// paneStagger spreads the panes' reloads so they don't redraw and hit tmux together
	paneStagger = 5 * time.Second
	// FlashFor is how long a pane shows the version a reload brought
	FlashFor     = 10 * time.Second
	maxExecFails = 3
	rewatchAfter = 10 * time.Minute
)

// Test doubles for the watcher, the exec and the program's terminal.
var (
	watchPane      = Watch
	execPane       = Exec
	programOptions = func() []tea.ProgramOption { return []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()} }
)

// ReadyMsg is the watcher's Ready as a pane's message: a new release is installed.
type ReadyMsg Ready

type warnMsg struct{ err error }

type rewatchMsg struct{}

type flashDoneMsg struct{}

// Pane is the self-reload state a Bubble Tea pane's model (a sidebar, a boss's previews) embeds: RunPane
// watches for a new release, the model holds its Ready until idle (Reload), then RunPane execs it in place.
type Pane struct {
	Busy     Busy           // work running off the update loop, which an exec would cut off
	Err      error          // a failed reload or watcher check, shown until a key or the watcher's next Ready
	Flash    string         // the version a reload just brought, shown for FlashFor
	watch    func() tea.Cmd // starts a watcher, its Ready as a ReadyMsg; set by RunPane, nil in tests
	ready    *Ready         // held until the model is idle
	now      *Ready         // quitting the program for RunPane to exec it
	failedTo string         // the version whose exec failed last, fails times in a row
	fails    int
	exit     bool // the user quit: a reload decided before tea.Quit lands must not turn it into an exec
}

// NewPane is a pane's reload state in a process running version own.
func NewPane(own string) Pane {
	return Pane{Busy: NewBusy(), Flash: ReloadedTo(own)}
}

// Init starts watching, and ends the flash of a version just reloaded into.
func (p Pane) Init() tea.Cmd {
	cmds := []tea.Cmd{p.watchCmd()}
	if p.Flash != "" {
		cmds = append(cmds, tea.Tick(FlashFor, func(time.Time) tea.Msg { return flashDoneMsg{} }))
	}
	return tea.Batch(cmds...)
}

// Update takes the reload's own messages (handled true); the model then calls Reload when it may be idle.
func (p *Pane) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case ReadyMsg:
		r := Ready(msg)
		if p.gaveUp(r.Version) {
			// A new watcher checks the same file at once: look again much later, for a newer release
			return tea.Tick(rewatchAfter, func(time.Time) tea.Msg { return rewatchMsg{} }), true
		}
		// The watcher's checks passed: its last warning is gone
		p.ready, p.Err = &r, nil
		return nil, true
	case warnMsg:
		p.Err = fmt.Errorf("reload: %w", msg.err)
		return nil, true
	case flashDoneMsg:
		p.Flash = ""
		return nil, true
	case rewatchMsg:
		return p.watchCmd(), true
	}
	return nil, false
}

// Held is the version waiting for the model to be idle; "" when none.
func (p Pane) Held() string {
	if p.ready == nil {
		return ""
	}
	return p.ready.Version
}

// Quitting reports the program quitting to reload: the model must take no input and start no action (the exec
// would cut it off).
func (p Pane) Quitting() bool { return p.now != nil }

// Reload quits the program to exec into the held Ready, when idle and nothing runs off the update loop.
func (p *Pane) Reload(idle bool) tea.Cmd {
	if p.ready == nil || p.now != nil || p.exit || !idle || p.Busy.Load() != 0 {
		return nil
	}
	p.now = p.ready
	return tea.Quit
}

// Quit is the user's quit (ctrl+c): the program exits, no reload.
func (p *Pane) Quit() tea.Cmd {
	p.exit = true
	return tea.Quit
}

// afterExec is the pane after a failed exec, to run again on this version and watch again.
func (p *Pane) afterExec(err error) {
	v := p.now.Version
	p.now, p.ready = nil, nil
	// Another pane is the canary, or the kill switch went on: nothing to show
	if err == ErrNotNow {
		return
	}
	p.Err = fmt.Errorf("reload: %w", err)
	// Only the exec itself counts: a tmux hiccup must not give up on a good release
	if _, ok := errors.AsType[syscall.Errno](err); !ok {
		return
	}
	if v != p.failedTo {
		p.failedTo, p.fails = v, 0
	}
	p.fails++
	if p.fails >= maxExecFails {
		p.Err = fmt.Errorf("reload to %s failed %d times, staying on this version: %w", v, p.fails, err)
	}
}

func (p Pane) watchCmd() tea.Cmd {
	if p.watch == nil {
		return nil
	}
	return p.watch()
}

// gaveUp: v's exec failed maxExecFails times; each try quits and redraws the program, so no more.
func (p Pane) gaveUp(v string) bool {
	return v == p.failedTo && p.fails >= maxExecFails
}

// RunPane runs m (its Pane at pane(&m)) until the process is killed or the program quits on its own. Once m quits
// to reload (Pane.Reload) it execs args (args[0] included) into the new release of o.Exe with env(m) set
// ("KEY=VALUE"; env may be nil); an exec that fails runs m again in-process with the error on Pane.Err (none for
// a bare ErrNotNow). It never returns to reload: a pane's clean exit may close its session. o.Canary, o.Stagger
// and o.Warn are its own.
func RunPane[M tea.Model](m M, pane func(*M) *Pane, o Options, args []string, env func(M) []string) error {
	// One context for the process: the canary's ok mark runs in the first Watch for 30 s
	ctx := context.Background()
	// The program now running: warnings (the ok mark's too) go to the one after an in-process restart
	var cur atomic.Pointer[tea.Program]
	o.Canary, o.Stagger = true, paneStagger
	o.Warn = func(err error) {
		if p := cur.Load(); p != nil {
			p.Send(warnMsg{err})
		}
	}
	for {
		pane(&m).watch = func() tea.Cmd {
			ready := watchPane(ctx, o)
			return func() tea.Msg {
				if r, ok := <-ready; ok {
					return ReadyMsg(r)
				}
				return nil
			}
		}
		p := tea.NewProgram(m, programOptions()...)
		cur.Store(p)
		final, err := p.Run()
		if err != nil {
			return err
		}
		m = final.(M)
		r := pane(&m)
		if r.now == nil {
			return nil
		}
		var set []string
		if env != nil {
			set = env(m)
		}
		r.afterExec(execPane(*r.now, args, set...))
	}
}

// ReloadedTo is the version a reload brought this process (own), "" when it wasn't started by one.
func ReloadedTo(own string) string {
	if v := os.Getenv(envReloadedTo); v == own {
		return v
	}
	return ""
}

// Busy counts work a pane runs off its update loop (an action, a popup, a reply being sent): from when its command
// is made in Update until the model takes its message (Done), so no reload falls in between. Nil-safe for test models.
type Busy struct{ n *atomic.Int32 }

func NewBusy() Busy { return Busy{n: new(atomic.Int32)} }

func (b Busy) Load() int32 {
	if b.n == nil {
		return 0
	}
	return b.n.Load()
}

func (b Busy) Add(d int32) {
	if b.n != nil {
		b.n.Add(d)
	}
}

// Run is fn as a command counted from now until the model takes its message (Done).
func (b Busy) Run(fn func() tea.Msg) tea.Cmd {
	b.Add(1)
	return func() tea.Msg { return busyDone{msg: fn(), b: b} }
}

// busyDone is a Busy command's message, counted until the model takes it.
type busyDone struct {
	msg tea.Msg
	b   Busy
}

// Done is msg as the model's Update takes it: a Busy command's own message (nil when it has none), no longer counted.
func Done(msg tea.Msg) tea.Msg {
	if d, ok := msg.(busyDone); ok {
		d.b.Add(-1)
		return d.msg
	}
	return msg
}
