package selfreload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/tmux"
)

// paneModel is the smallest pane: it reloads as soon as a Ready lands; ctrl+c quits.
type paneModel struct {
	reload Pane
}

func (m paneModel) Init() tea.Cmd {
	return m.reload.Init()
}

func (m paneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.reload.Update(msg); ok {
		if quit := m.reload.Reload(true); quit != nil {
			return m, quit
		}
		return m, cmd
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	return m, nil
}

func (m paneModel) View() string { return "" }

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestPaneReloadWaitsForIdleAndBusy(t *testing.T) {
	p := Pane{Busy: NewBusy()}
	p.Update(ReadyMsg{Version: "v2"})
	if p.Held() != "v2" {
		t.Fatalf("Held %q, want v2", p.Held())
	}
	if quits(p.Reload(false)) {
		t.Fatal("reloaded while not idle")
	}
	cmd := p.Busy.Run(func() tea.Msg { return "out" })
	if quits(p.Reload(true)) {
		t.Fatal("reloaded while a command was about to run")
	}
	msg := cmd()
	if quits(p.Reload(true)) {
		t.Fatal("reloaded between the command's end and the model taking its message")
	}
	if got := Done(msg); got != "out" {
		t.Fatalf("Done = %v, want the command's message", got)
	}
	if Done("other") != "other" {
		t.Error("Done changed a message no Busy command sent")
	}
	if !quits(p.Reload(true)) || !p.Quitting() {
		t.Fatal("no reload once idle and nothing runs")
	}
	if p.Reload(true) != nil {
		t.Error("a second Reload while quitting quits again")
	}
}

func TestPaneAfterExec(t *testing.T) {
	r := &Ready{Version: "v2"}
	for _, tc := range []struct {
		name        string
		err         error
		show, count bool
	}{
		{"exec fails", fmt.Errorf("selfreload: exec /bin/dev: %w", syscall.EACCES), true, true},
		{"tmux fails", errors.New("selfreload: canary claim: exit status 1"), true, false},
		{"not now", ErrNotNow, false, false},
		// The kill switch read failed, or a claim couldn't be dropped: the user may have to act
		{"not now, with an error", fmt.Errorf("%w: %w", ErrNotNow, errors.New("tmux")), true, false},
	} {
		p := Pane{ready: r, now: r}
		p.afterExec(tc.err)
		if p.ready != nil || p.now != nil {
			t.Errorf("%s: Ready kept: the next program would exec again at once", tc.name)
		}
		if (p.Err != nil) != tc.show {
			t.Errorf("%s: Err %v, want shown %v", tc.name, p.Err, tc.show)
		}
		if (p.fails == 1) != tc.count {
			t.Errorf("%s: fails %d, want counted %v", tc.name, p.fails, tc.count)
		}
	}
}

func TestPaneExecFailsGivesUp(t *testing.T) {
	watches := 0
	p := Pane{watch: func() tea.Cmd { watches++; return func() tea.Msg { return nil } }}
	for i := range maxExecFails {
		p.Update(ReadyMsg{Version: "v2"})
		if !quits(p.Reload(true)) {
			t.Fatalf("try %d: no reload", i+1)
		}
		p.afterExec(fmt.Errorf("selfreload: exec /bin/dev: %w", syscall.ENOEXEC))
	}
	cmd, _ := p.Update(ReadyMsg{Version: "v2"})
	if p.Held() != "" || p.Reload(true) != nil {
		t.Fatalf("reloads into v2 after %d failed execs", maxExecFails)
	}
	if cmd == nil || watches != 0 {
		t.Fatalf("dropped v2 with %d watches started at once (want a later rewatch)", watches)
	}
	if cmd, _ = p.Update(rewatchMsg{}); cmd == nil || watches != 1 {
		t.Fatalf("rewatch started %d watches: a later release would never load", watches)
	}
	if !strings.Contains(p.Err.Error(), "failed 3 times") {
		t.Errorf("Err %v, want the give-up", p.Err)
	}
	p.Update(ReadyMsg{Version: "v3"})
	if !quits(p.Reload(true)) {
		t.Error("a newer version doesn't reload")
	}
}

func TestPaneReadyClearsWarning(t *testing.T) {
	p := Pane{}
	p.Update(warnMsg{errors.New("tmux down")})
	if p.Err == nil || p.Err.Error() != "reload: tmux down" {
		t.Fatalf("Err %v after a warning", p.Err)
	}
	p.Update(ReadyMsg{Version: "v2"})
	if p.Err != nil {
		t.Error("a Ready leaves the watcher's last warning")
	}
}

func TestPaneFlash(t *testing.T) {
	t.Setenv(envReloadedTo, "v2")
	if p := NewPane("v2"); p.Flash != "v2" {
		t.Errorf("reloaded into v2: Flash %q", p.Flash)
	}
	if p := NewPane("v3"); p.Flash != "" {
		t.Errorf("v3 not reloaded into: Flash %q", p.Flash)
	}
	p := NewPane("v2")
	p.Update(flashDoneMsg{})
	if p.Flash != "" {
		t.Error("flash kept after FlashFor")
	}
}

// TestRunPaneStaysAfterExecError: an exec that fails keeps the process in RunPane, which starts the program and
// the watcher again; only the model quitting on its own ends it. It swaps package-level doubles: no t.Parallel.
func TestRunPaneStaysAfterExecError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execErr error
	}{
		{"exec fails", errors.New("exec format error")},
		{"another pane is the canary", ErrNotNow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watches := make(chan Options, 2)
			calls := 0 // RunPane calls watchPane from one goroutine
			watchPane = func(_ context.Context, o Options) <-chan Ready {
				ch := make(chan Ready, 1)
				if calls++; calls == 1 {
					ch <- Ready{Version: "v2"}
					close(ch)
				}
				watches <- o
				return ch
			}
			var execs [][]string
			execPane = func(r Ready, args []string, set ...string) error {
				execs = append(execs, append(slices.Clone(args), set...))
				return tc.execErr
			}
			// A pipe per program: a quit program's reader may still hold the last one
			inputs := make(chan *io.PipeWriter, 2)
			orig := programOptions
			programOptions = func() []tea.ProgramOption {
				in, keys := io.Pipe()
				inputs <- keys
				return []tea.ProgramOption{tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithoutRenderer()}
			}
			t.Cleanup(func() {
				watchPane, execPane = Watch, Exec
				programOptions = orig
			})

			c := tmux.NewFake(func([]string) (string, error) { return "", errors.New("no server") })
			o := Options{Exe: "/bin/dev", Own: "v1", Tmux: c}
			env := func(m paneModel) []string { return []string{"DEV_STATE=x"} }
			done := make(chan error, 1)
			go func() {
				done <- RunPane(paneModel{}, func(m *paneModel) *Pane { return &m.reload }, o, []string{"/bin/dev", "sidebar"}, env)
			}()
			for i := range 2 {
				select {
				case o := <-watches:
					if o.Exe != "/bin/dev" || o.Own != "v1" || !o.Canary || o.Tmux != c || o.Stagger != paneStagger || o.Warn == nil {
						t.Fatalf("watch %d options %+v", i, o)
					}
				case err := <-done:
					t.Fatalf("RunPane returned %v before watching again", err)
				case <-time.After(5 * time.Second):
					t.Fatalf("watch %d never started", i)
				}
			}
			<-inputs
			keys := <-inputs
			if _, err := keys.Write([]byte{0x03}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("RunPane after Ctrl-C: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("RunPane kept going after Ctrl-C")
			}
			if len(execs) != 1 || strings.Join(execs[0], " ") != "/bin/dev sidebar DEV_STATE=x" {
				t.Fatalf("execs %v, want one with /bin/dev sidebar and the env", execs)
			}
		})
	}
}
