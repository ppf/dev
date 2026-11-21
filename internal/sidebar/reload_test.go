package sidebar

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/tmux"
)

func TestIdle(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Model
		want bool
	}{
		{"nothing open", Model{}, true},
		{"answer in flight", Model{answering: true}, false},
		{"TO DO mode", Model{needMode: true}, false},
		{"TO DO row open", Model{needOpen: true}, false},
		{"tick not yet read back", Model{ticked: "a#1"}, false},
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
	next, cmd := Model{}.Update(selfreload.ReadyMsg{Version: "v2"})
	if got := next.(Model); !quits(cmd) || !got.reload.Quitting() {
		t.Fatalf("idle Ready: quit %v, quitting %v; want quit with v2", quits(cmd), got.reload.Quitting())
	}
}

func TestReadyHeldInTodoModeUntilClosed(t *testing.T) {
	m, _ := key(needsModel(new([]answerCall)), "n")
	if !m.needMode {
		t.Fatal("n left TO DO mode closed")
	}
	next, cmd := m.Update(selfreload.ReadyMsg{Version: "v2"})
	m = next.(Model)
	if quits(cmd) || m.reload.Quitting() {
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
	if !quits(cmd) || !m.reload.Quitting() {
		t.Fatal("no reload on the first refresh after TO DO mode closed")
	}
}

func TestRunActionHoldsReloadUntilDone(t *testing.T) {
	release := make(chan struct{})
	m := Model{reload: selfreload.NewPane("v1"), c: tmux.NewFake(func([]string) (string, error) { return "", nil })}
	action := m.runAction("", func() error { <-release; return nil })
	next, cmd := m.Update(selfreload.ReadyMsg{Version: "v2"})
	m = next.(Model)
	if quits(cmd) {
		t.Fatal("reloaded while an action was about to run")
	}
	done := make(chan tea.Msg)
	go func() { done <- action() }()
	close(release)
	msg := <-done
	if _, cmd = m.Update(refreshMsg{gen: m.gen, err: errors.New("x")}); quits(cmd) {
		t.Fatal("reloaded between the action's end and its message")
	}
	next, _ = m.Update(msg)
	if _, cmd = next.(Model).Update(refreshMsg{gen: m.gen, err: errors.New("x")}); !quits(cmd) {
		t.Fatal("no reload once the action was done")
	}
}

func TestBottomRowReload(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 20}, reload: selfreload.Pane{Flash: "v2"}}
	if row := m.bottomRow(); !strings.Contains(row, "↻ dev v2") {
		t.Errorf("after a reload: %q, want ↻ dev v2", row)
	}
	m.reload.Err = errors.New("reload: boom")
	if row := m.bottomRow(); !strings.Contains(row, "❌ reload: boom") {
		t.Errorf("after a failed reload: %q", row)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if next.(Model).reload.Err != nil {
		t.Error("a key leaves the reload error")
	}
}

func TestInputIgnoredWhileQuittingToReload(t *testing.T) {
	m := Model{reload: selfreload.NewPane("v1"), c: tmux.NewFake(func([]string) (string, error) { return "", nil })}
	next, _ := m.Update(selfreload.ReadyMsg{Version: "v2"})
	m = next.(Model)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}); cmd != nil || m.reload.Busy.Load() != 0 {
		t.Error("a key after the quit decision starts an action the exec would cut off")
	}
	if _, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd != nil {
		t.Error("a click after the quit decision is acted on")
	}
}

func TestReloadWarnShown(t *testing.T) {
	m := Model{snap: snapshot{width: 60, height: 20}, reload: selfreload.Pane{Err: errors.New("reload: selfreload: tmux down")}}
	if row := m.bottomRow(); !strings.Contains(row, "❌ reload: selfreload: tmux down") {
		t.Errorf("bottom row %q, want the watcher's warning", row)
	}
}
