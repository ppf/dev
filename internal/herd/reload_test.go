package herd

import (
	"errors"
	"maps"
	"slices"
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
		{"reply box open", Model{box: replyBox{session: "a"}}, false},
		{"a reply kept for another card (carried)", Model{drafts: map[string]replyBox{"a": {}}}, true},
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

// ready is m holding a Ready for v2, its cards' sessions on the server.
func ready(t *testing.T, m Model) Model {
	t.Helper()
	for _, tile := range m.snap.tiles {
		m.snap.all = append(m.snap.all, tile.sess)
	}
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
	// Focus moved away: the reply is kept for its card and carried by the reload
	m.blur()
	m, quit := refreshed(m)
	if !quit {
		t.Fatal("no reload with only a kept reply: it would hold the reload for as long as it's forgotten")
	}
	n := Model{}.restore(strings.TrimPrefix(m.carry()[0], envState+"="))
	n.snap = m.snap
	n.open(n.snap.tiles[index(n.snap.tiles, m.selected)])
	if got := string(n.box.ed.text); got != "hi" || n.box.ed.pos != 2 {
		t.Fatalf("after the reload the kept reply is %q at %d, want hi at 2", got, n.box.ed.pos)
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
	m.c = tmux.NewFake(func([]string) (string, error) { return "", errors.New("no server") })
	action := m.action(func() error { return errors.New("boom") })
	_, watch := m.Update(watchDueMsg{})
	if n := m.reload.Busy.Load(); n != 2 {
		t.Fatalf("busy %d, want the action and the inbox watch counted from their making", n)
	}
	if _, quit := refreshed(m); quit {
		t.Fatal("reloaded with an action and the inbox watch about to run")
	}
	// Run, their messages not yet taken: a reload now would drop the action's ❌
	done, watched := action(), watch()
	if _, quit := refreshed(m); quit {
		t.Fatal("reloaded before the action's message was taken")
	}
	next, _ := m.Update(watched)
	next, _ = next.(Model).Update(done)
	m = next.(Model)
	if !strings.Contains(m.msg.text, "boom") {
		t.Fatalf("msg %q, want the action's error", m.msg.text)
	}
	if _, quit := refreshed(m); !quit {
		t.Fatal("no reload once the commands' messages were taken")
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
	if row := ansiStrip(m.bottom())[0]; !strings.HasPrefix(row, " ↻ dev v2  ") || !strings.Contains(row, "j/k") {
		t.Errorf("after a reload: %q, want ↻ dev v2 before the keys", row)
	}
	m.reload.Err = errors.New("reload: boom")
	m.msg = info("Sent to b")
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "❌ reload: boom") {
		t.Errorf("after a failed reload, under an info message: %q", row)
	}
	m.msg = alert("❌ close failed")
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "close failed") {
		t.Errorf("an action's alert under a reload error: %q", row)
	}
	m.msg = message{}
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

// The sidebar's ask to focus TO DO, read by a refresh, wins over a held reload: its exec would lose it.
func TestReloadHeldByTodoFocusAsk(t *testing.T) {
	m := ready(t, needsBoard(t, new([]listAnswer)))
	snap := m.snap
	snap.needsFocus = true
	next, cmd := m.Update(refreshMsg{snap: snap, inputs: m.inputs})
	m = next.(Model)
	if quits(cmd) || !m.needMode {
		t.Fatalf("quit %v, TO DO mode %v: want TO DO focused, the reload held", quits(cmd), m.needMode)
	}
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "↻ v2") {
		t.Errorf("bottom row %q: want the held reload", row)
	}
}

// A watcher's warning doesn't open an empty, collapsed pane: it takes the coordinator's room until a key.
func TestReloadWarningKeepsPaneCollapsed(t *testing.T) {
	var ran []string
	m := Model{width: collapsedW, height: 10, size: collapsed, c: tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "", nil
	})}
	m.reload.Err = errors.New("reload: tmux down")
	m.fit()
	if m.size != collapsed || len(ran) != 0 {
		t.Fatalf("size %v, tmux calls %q: want it left collapsed", m.size, ran)
	}
}

func TestCarryLeavesOut(t *testing.T) {
	m := board4()
	m.msg = info("Sent to docs-api")
	m.drafts = map[string]replyBox{"gone": {pane: "%9", ed: editor{text: []rune("x")}}}
	r := Model{}.restore(strings.TrimPrefix(m.carry()[0], envState+"="))
	if r.msg.text != "" {
		t.Errorf("info %q carried: it would hide the flash of the new version", r.msg.text)
	}
	if len(r.drafts) != 0 {
		t.Errorf("a reply kept for a closed session carried: %v", r.drafts)
	}
	// Its pane replaced, a long one would hold the reload for good
	t0 := m.snap.tiles[0]
	m.snap.all = append(m.snap.all, t0.sess)
	m.drafts = map[string]replyBox{t0.sess.Name: {pane: t0.pane + "x", ed: editor{text: []rune(strings.Repeat("x", maxState))}}}
	if !m.fits() {
		t.Error("a reply kept for a replaced pane counted against the bound")
	}
}

// ctrl+c with a release held exits: a refresh before tea.Quit lands must not turn it into a reload.
func TestCtrlCBeatsHeldReload(t *testing.T) {
	next, _ := ready(t, board4()).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, quit := refreshed(next.(Model)); quit || next.(Model).reload.Quitting() {
		t.Error("a refresh after ctrl+c reloaded instead of exiting")
	}
}

func TestClickClearsReloadError(t *testing.T) {
	m := board4()
	m.reload.Err = errors.New("reload: boom")
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 300, Y: 300})
	if next.(Model).reload.Err != nil {
		t.Error("a click leaves the reload error, hiding what the click did")
	}
}

// A window resize before a reload's first refresh undoes the carried collapse, as it would a live one.
func TestResizeBeforeFirstRefreshUndoesCarriedCollapse(t *testing.T) {
	m := Model{}.restore(`{"size":2}`)
	if m.restoredSize != collapsed {
		t.Fatalf("restoredSize %v, want collapsed", m.restoredSize)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if got := next.(Model).restoredSize; got != unsized {
		t.Errorf("restoredSize %v after a wide resize, want unsized", got)
	}
}

// A kept reply too long to carry holds the reload, rather than failing the exec on the environment's size.
func TestReloadHeldByStateOverBound(t *testing.T) {
	m := ready(t, board4())
	m.drafts = map[string]replyBox{"docs-api": {ed: editor{text: []rune(strings.Repeat("x", maxState+1))}}}
	m, quit := refreshed(m)
	if quit {
		t.Fatal("reloaded with state over the bound")
	}
	m.width = 200
	if row := ansiStrip(m.bottom())[0]; !strings.Contains(row, "↻ v2 reloads once the long kept message goes") {
		t.Errorf("bottom row %q: want what holds it", row)
	}
	m.drafts = nil
	if _, quit = refreshed(m); !quit {
		t.Fatal("no reload once the long reply was gone")
	}
}

// A failed refresh decides nothing: just after a reload its sessions are unread, and carry would drop every kept reply.
func TestNoReloadOnFailedRefresh(t *testing.T) {
	m := ready(t, board4())
	next, cmd := m.Update(refreshMsg{err: errors.New("tmux hiccup")})
	if quits(cmd) {
		t.Fatal("reloaded on a failed refresh")
	}
	if _, quit := refreshed(next.(Model)); !quit {
		t.Fatal("no reload on the good refresh after it")
	}
}
