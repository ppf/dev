package sidebar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/tmux"
)

func TestIdle(t *testing.T) {
	busy := newCounter()
	busy.add(1)
	for _, tc := range []struct {
		name string
		m    Model
		want bool
	}{
		{"nothing open", Model{running: newCounter()}, true},
		{"model from a test literal", Model{}, true},
		{"answer in flight", Model{answering: true}, false},
		{"TO DO mode", Model{needMode: true}, false},
		{"TO DO row open", Model{needOpen: true}, false},
		{"tick not yet read back", Model{ticked: "a#1"}, false},
		{"popup action running", Model{running: busy}, false},
	} {
		if got := tc.m.idle(); got != tc.want {
			t.Errorf("%s: idle() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// quits reports whether cmd is tea.Quit.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestReadyWhenIdleQuitsToReload(t *testing.T) {
	m := Model{running: newCounter()}
	next, cmd := m.Update(readyMsg{Version: "v2"})
	got := next.(Model)
	if !quits(cmd) || got.reloadNow == nil || got.reloadNow.Version != "v2" {
		t.Fatalf("idle Ready: quit %v, reloadNow %v; want quit with v2", quits(cmd), got.reloadNow)
	}
}

func TestReadyHeldInTodoModeUntilClosed(t *testing.T) {
	m, _ := key(needsModel(new([]answerCall)), "n")
	if !m.needMode {
		t.Fatal("n left TO DO mode closed")
	}
	next, cmd := m.Update(readyMsg{Version: "v2"})
	m = next.(Model)
	if quits(cmd) || m.reloadNow != nil {
		t.Fatal("reloaded while TO DO mode was open")
	}
	if row := m.bottomRow(); !strings.Contains(row, "↻ v2 ready: reloads when idle") {
		t.Fatalf("bottom row %q: want the held reload", row)
	}
	// A refresh while still open holds it
	next, cmd = m.Update(refreshMsg{gen: m.gen, err: errors.New("x")})
	m = next.(Model)
	if quits(cmd) {
		t.Fatal("reloaded on a refresh while TO DO mode was open")
	}
	m, _ = key(m, "esc")
	next, cmd = m.Update(refreshMsg{gen: m.gen, err: errors.New("x")})
	m = next.(Model)
	if !quits(cmd) || m.reloadNow == nil {
		t.Fatal("no reload on the first refresh after TO DO mode closed")
	}
}

func TestRunActionHoldsReloadUntilDone(t *testing.T) {
	release := make(chan struct{})
	m := Model{running: newCounter(), c: tmux.NewFake(func([]string) (string, error) { return "", nil })}
	action := m.runAction("", func() error { <-release; return nil })
	next, cmd := m.Update(readyMsg{Version: "v2"})
	m = next.(Model)
	if quits(cmd) {
		t.Fatal("reloaded while an action was about to run")
	}
	done := make(chan tea.Msg)
	go func() { done <- action() }()
	close(release)
	<-done
	if _, cmd = m.Update(refreshMsg{gen: m.gen, err: errors.New("x")}); !quits(cmd) {
		t.Fatal("no reload once the action was done")
	}
}

func TestBottomRowReload(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 20}, reloadFlash: "v2"}
	if row := m.bottomRow(); !strings.Contains(row, "↻ dev v2") {
		t.Errorf("after a reload: %q, want ↻ dev v2", row)
	}
	m.reloadErr = errors.New("reload: boom")
	if row := m.bottomRow(); !strings.Contains(row, "❌ reload: boom") {
		t.Errorf("after a failed reload: %q", row)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if next.(Model).reloadErr != nil {
		t.Error("a key leaves the reload error")
	}
}

func TestAfterExec(t *testing.T) {
	r := &selfreload.Ready{Version: "v2"}
	for _, tc := range []struct {
		name        string
		err         error
		show, count bool
	}{
		{"exec fails", fmt.Errorf("selfreload: exec /bin/dev: %w", syscall.EACCES), true, true},
		{"tmux fails", errors.New("selfreload: canary claim: exit status 1"), true, false},
		{"not now", selfreload.ErrNotNow, false, false},
		// The kill switch read failed, or a claim couldn't be dropped: the user may have to act
		{"not now, with an error", fmt.Errorf("%w: %w", selfreload.ErrNotNow, errors.New("tmux")), true, false},
	} {
		m := Model{ready: r, reloadNow: r}.afterExec(tc.err)
		if m.ready != nil || m.reloadNow != nil {
			t.Errorf("%s: Ready kept: the next program would exec again at once", tc.name)
		}
		if (m.reloadErr != nil) != tc.show {
			t.Errorf("%s: reloadErr %v, want shown %v", tc.name, m.reloadErr, tc.show)
		}
		if (m.fails == 1) != tc.count {
			t.Errorf("%s: fails %d, want counted %v", tc.name, m.fails, tc.count)
		}
	}
}

func TestExecFailsGivesUp(t *testing.T) {
	watches := 0
	m := Model{running: newCounter(), watch: func() tea.Cmd { watches++; return func() tea.Msg { return nil } }}
	for i := range maxExecFails {
		next, cmd := m.Update(readyMsg{Version: "v2"})
		if m = next.(Model); !quits(cmd) {
			t.Fatalf("try %d: no reload", i+1)
		}
		m = m.afterExec(fmt.Errorf("selfreload: exec /bin/dev: %w", syscall.ENOEXEC))
	}
	next, cmd := m.Update(readyMsg{Version: "v2"})
	m = next.(Model)
	if m.ready != nil || m.reloadNow != nil {
		t.Fatalf("reloads into v2 after %d failed execs", maxExecFails)
	}
	if cmd == nil || watches != 0 {
		t.Fatalf("dropped v2 with %d watches started at once (want a later rewatch)", watches)
	}
	if _, cmd = m.Update(rewatchMsg{}); cmd == nil || watches != 1 {
		t.Fatalf("rewatch started %d watches: a later release would never load", watches)
	}
	if !strings.Contains(m.reloadErr.Error(), "failed 3 times") {
		t.Errorf("reloadErr %v, want the give-up", m.reloadErr)
	}
	if _, cmd = m.Update(readyMsg{Version: "v3"}); !quits(cmd) {
		t.Error("a newer version doesn't reload")
	}
}

func TestInputIgnoredWhileQuittingToReload(t *testing.T) {
	m := Model{running: newCounter(), c: tmux.NewFake(func([]string) (string, error) { return "", nil })}
	next, _ := m.Update(readyMsg{Version: "v2"})
	m = next.(Model)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}); cmd != nil || m.running.load() != 0 {
		t.Error("a key after the quit decision starts an action the exec would cut off")
	}
	if _, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd != nil {
		t.Error("a click after the quit decision is acted on")
	}
}

func TestReadyClearsWarning(t *testing.T) {
	m := Model{running: newCounter(), needMode: true, reloadErr: errors.New("reload: tmux down")}
	next, _ := m.Update(readyMsg{Version: "v2"})
	if next.(Model).reloadErr != nil {
		t.Error("a Ready leaves the watcher's last warning")
	}
}

func TestReloadWarnShown(t *testing.T) {
	next, _ := Model{snap: snapshot{width: 60, height: 20}}.Update(reloadWarnMsg{errors.New("selfreload: tmux down")})
	if row := next.(Model).bottomRow(); !strings.Contains(row, "❌ reload: selfreload: tmux down") {
		t.Errorf("bottom row %q, want the watcher's warning", row)
	}
}

// TestRunStaysAfterExecError: an exec that fails keeps the process in run, which starts the program and
// the watcher again; only Ctrl-C ends it. It swaps package-level doubles: no t.Parallel in this package.
func TestRunStaysAfterExecError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execErr error
	}{
		{"exec fails", errors.New("exec format error")},
		{"another pane is the canary", selfreload.ErrNotNow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watches := make(chan selfreload.Options, 2)
			calls := 0 // run calls watchReload from one goroutine
			watchReload = func(_ context.Context, o selfreload.Options) <-chan selfreload.Ready {
				ch := make(chan selfreload.Ready, 1)
				if calls++; calls == 1 {
					ch <- selfreload.Ready{Version: "v2"}
					close(ch)
				}
				watches <- o
				return ch
			}
			var execs [][]string
			execReload = func(r selfreload.Ready, args []string) error {
				execs = append(execs, args)
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
				watchReload, execReload = selfreload.Watch, selfreload.Exec
				programOptions = orig
			})

			c := tmux.NewFake(func([]string) (string, error) { return "", errors.New("no server") })
			done := make(chan error, 1)
			go func() {
				done <- run(Model{c: c, running: newCounter()}, "v1", "/bin/dev", []string{"/bin/dev", "sidebar"})
			}()
			for i := range 2 {
				select {
				case o := <-watches:
					if o.Exe != "/bin/dev" || o.Own != "v1" || !o.Canary || o.Tmux != c {
						t.Fatalf("watch %d options %+v", i, o)
					}
				case err := <-done:
					t.Fatalf("run returned %v before watching again", err)
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
					t.Fatalf("run after Ctrl-C: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("run kept going after Ctrl-C")
			}
			if len(execs) != 1 || strings.Join(execs[0], " ") != "/bin/dev sidebar" {
				t.Fatalf("execs %v, want one with /bin/dev sidebar", execs)
			}
		})
	}
}
