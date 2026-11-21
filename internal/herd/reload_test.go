package herd

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/selfreload"
)

func TestIdle(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Model
		want bool
	}{
		{"nothing open", Model{}, true},
		{"reply box open", Model{box: replyBox{session: "a"}}, false},
		{"a reply kept for another card", Model{drafts: map[string]replyBox{"a": {}}}, false},
		{"kill asked", Model{confirm: "a"}, false},
		{"answer in flight", Model{answering: "a"}, false},
		{"TO DO mode", Model{needMode: true}, false},
	} {
		if got := tc.m.idle(); got != tc.want {
			t.Errorf("%s: idle() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func typed(m Model, ks ...string) Model {
	m, _ = keys(m, ks...)
	return m
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// ready is m holding a Ready for v2.
func ready(t *testing.T, m Model) Model {
	t.Helper()
	m.reload = selfreload.NewPane("v1")
	next, cmd := m.Update(selfreload.ReadyMsg{Version: "v2"})
	if quits(cmd) {
		t.Fatal("reloaded on the Ready: only a refresh decides")
	}
	return next.(Model)
}

// refreshed is m after a refresh showing the same board, and whether it quit to reload.
func refreshed(m Model) (Model, bool) {
	next, cmd := m.Update(refreshMsg{snap: m.snap, inputs: m.inputs})
	return next.(Model), quits(cmd)
}

func TestReloadOnRefreshWhenIdle(t *testing.T) {
	m, quit := refreshed(ready(t, board4()))
	if !quit || !m.reload.Quitting() {
		t.Fatal("idle board: no reload on the refresh after the Ready")
	}
}

func TestReloadHeldByTypedReply(t *testing.T) {
	b := board4()
	b.snap.focused = true
	m := typed(ready(t, b), "m", "h", "i")
	if m.box.session == "" {
		t.Fatal("m opened no reply box")
	}
	if m, quit := refreshed(m); quit {
		t.Fatal("reloaded with a reply being typed")
	} else if got := string(m.box.ed.text); got != "hi" {
		t.Fatalf("draft %q after the refresh, want hi", got)
	}
	// Focus moved away: the reply is kept for its card, and still holds the reload
	m.blur()
	m.width = 120
	m, quit := refreshed(m)
	if quit {
		t.Fatal("reloaded with a reply kept for a card")
	}
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "j/k select") || !strings.Contains(row, "↻ v2 reloads once the unsent message goes") {
		t.Errorf("bottom row %q: want the keys, then what holds the reload", row)
	}
	m.open(m.snap.tiles[index(m.snap.tiles, m.selected)])
	if string(m.box.ed.text) != "hi" {
		t.Fatal("the kept reply didn't come back")
	}
	m = typed(m, "esc")
	if _, quit = refreshed(m); !quit {
		t.Fatal("no reload once the reply was cleared")
	}
}

func TestReloadHeldByKillQuestion(t *testing.T) {
	m := typed(ready(t, board4()), "x")
	if m.confirm == "" {
		t.Fatal("x asked nothing")
	}
	m, quit := refreshed(m)
	if quit {
		t.Fatal("reloaded with a kill asked")
	}
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "? y/n") {
		t.Errorf("bottom row %q: the kill question gone", row)
	}
	if _, quit = refreshed(typed(m, "n")); !quit {
		t.Fatal("no reload once the kill was answered no")
	}
}

func TestReloadHeldInTodoModeUntilLeft(t *testing.T) {
	m, _ := keys(ready(t, needsBoard(t, new([]listAnswer))), "n")
	if !m.needMode {
		t.Fatal("n left TO DO mode closed")
	}
	m, quit := refreshed(m)
	if quit || !m.needMode {
		t.Fatal("reloaded in TO DO mode")
	}
	if row := ansiStrip(m.bottom())[0]; !strings.HasPrefix(strings.TrimSpace(row), "j/k move") || !strings.Contains(row, "↻ v2") {
		t.Errorf("bottom row %q: want TO DO's keys first, then the held reload", row)
	}
	m, _ = keys(m, "esc")
	if _, quit = refreshed(m); !quit {
		t.Fatal("no reload once TO DO mode was left")
	}
}

func TestReloadHeldByAnswerInFlight(t *testing.T) {
	m := ready(t, board4())
	m.answering = "fix-login"
	m, quit := refreshed(m)
	if quit {
		t.Fatal("reloaded with an answer in flight")
	}
	next, _ := m.Update(answeredMsg{session: "fix-login", msg: info("Answered fix-login"), took: true})
	if _, quit = refreshed(next.(Model)); !quit {
		t.Fatal("no reload once the answer was done")
	}
}

func TestReloadHeldByCommandRunning(t *testing.T) {
	m := ready(t, board4())
	release := make(chan struct{})
	action := m.action(func() error { <-release; return nil })
	_, wd := m.Update(watchDueMsg{})
	if n := m.reload.Busy.Load(); n != 2 {
		t.Fatalf("busy %d, want the action and the inbox watch counted from their making", n)
	}
	if _, quit := refreshed(m); quit {
		t.Fatal("reloaded with an action and the inbox watch about to run")
	}
	close(release)
	action()
	m.reload.Busy.Add(-1) // the inbox watch's command, not run here: it needs a tmux server
	if wd == nil {
		t.Fatal("no inbox watch")
	}
	if _, quit := refreshed(m); !quit {
		t.Fatal("no reload once the commands were done")
	}
}

func TestQuittingTakesNothing(t *testing.T) {
	m, quit := refreshed(ready(t, board4()))
	if !quit {
		t.Fatal("no reload")
	}
	inputs := m.inputs
	for _, msg := range []tea.Msg{runes("m"), tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}, tickMsg{}, watchDueMsg{}} {
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || got.box.session != "" || got.inputs != inputs || got.reload.Busy.Load() != 0 {
			t.Errorf("%T after the quit decision: acted on (cmd %v, box %q, inputs %d)", msg, cmd != nil, got.box.session, got.inputs)
		}
	}
}

func TestCarryRestore(t *testing.T) {
	m := board4()
	m.folds, m.expands = map[string]bool{"ci-fix": false}, map[string]bool{"fix-login": true}
	m.todoFolded, m.waitOpen, m.history = true, true, []string{"one", "two"}
	m.msg = alert("❌ x: boom")
	env := m.carry()
	if len(env) != 1 || !strings.HasPrefix(env[0], envState+"=") {
		t.Fatalf("carry = %q", env)
	}
	r := Model{}.restore(strings.TrimPrefix(env[0], envState+"="))
	if r.selected != m.selected || !maps.Equal(r.folds, m.folds) || !maps.Equal(r.expands, m.expands) || !r.todoFolded || !r.waitOpen ||
		!slices.Equal(r.history, m.history) || r.msg != m.msg || r.restoredSize != shared || r.reload.Err != nil {
		t.Fatalf("restored %+v", r)
	}
	// A new process: the first refresh takes the carried size (no refit, no focus handed back) and keeps the
	// expand of a card in Asking
	n := Model{width: 70, height: 40, clk: m.clk}.restore(strings.TrimPrefix(env[0], envState+"="))
	next, _ := n.Update(refreshMsg{snap: m.snap})
	n = next.(Model)
	if n.size != shared {
		t.Errorf("size %v after the first refresh, want the carried %v", n.size, shared)
	}
	if !n.expands["fix-login"] {
		t.Error("the first refresh dropped the expand of a card in Asking")
	}
	if n.selected != "docs-api" {
		t.Errorf("selected %q, want docs-api", n.selected)
	}
	if n.restored {
		t.Error("still restored after the first refresh: a card entering Asking later would keep its expand")
	}
}

func TestCarryBoundsHistory(t *testing.T) {
	m := Model{}
	for range maxHistory {
		m.history = append(m.history, strings.Repeat("x", 2000))
	}
	m.history[len(m.history)-1] = "newest"
	env := m.carry()[0]
	if len(env) > len(envState)+1+maxState {
		t.Fatalf("carried %d bytes, over %d", len(env), maxState)
	}
	r := Model{}.restore(strings.TrimPrefix(env, envState+"="))
	if len(r.history) == 0 || r.history[len(r.history)-1] != "newest" {
		t.Errorf("history kept %d replies, newest %q: want the newest ones", len(r.history), r.history[len(r.history)-1])
	}
}

func TestRestoreBadState(t *testing.T) {
	m := Model{width: 60, height: 10}.restore("{nope")
	if m.reload.Err == nil || !strings.Contains(ansiStrip(m.bottom())[0], "❌ reload: UI state not carried") {
		t.Fatalf("bad state: Err %v, bottom %q", m.reload.Err, ansiStrip(m.bottom()))
	}
}

func TestBottomRowAfterReload(t *testing.T) {
	m := twoTiles()
	m.reload = selfreload.Pane{Flash: "v2"}
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "↻ dev v2") {
		t.Errorf("after a reload: %q, want ↻ dev v2", row)
	}
	m.reload.Err = errors.New("reload: boom")
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "❌ reload: boom") {
		t.Errorf("after a failed reload: %q", row)
	}
	if got := typed(m, "j"); got.reload.Err != nil {
		t.Error("a key leaves the reload error")
	}
}

func TestNoPlaceholderBeforeReloadsFirstRefresh(t *testing.T) {
	m := Model{width: 60, height: 10}.restore(`{"selected":"a"}`)
	if got := strings.Join(ansiStrip(m.render()), "\n"); strings.Contains(got, "No worker sessions yet") {
		t.Errorf("a reload's first frame says the workers are gone:\n%s", got)
	}
}
