package herd

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// actionMsg is an action's outcome to show on the bottom row; none clears it.
type actionMsg struct{ msg message }

// message is a one-off line on the bottom row; an alert reports something that went wrong.
type message struct {
	text  string
	alert bool
}

func info(s string) message  { return message{text: s} }
func alert(s string) message { return message{text: s, alert: true} }

const (
	// maxHistory bounds the replies ↑ recalls.
	maxHistory = 50
	// staleRefreshes is how many refreshes still showing an answered dialog the picker waits.
	staleRefreshes = 3
)

// replyBox is the reply being typed to one worker, or the picker answering its dialog.
type replyBox struct {
	session string // "" when closed
	pane    string // its agent pane
	ed      editor
	hist    int    // the history entry ↑ recalled; -1 when not browsing
	hl      int    // the picker's highlighted option
	hlSig   string // the dialog hl is for: another one starts on its own cursor
	other   bool   // ed holds the picker's free-text answer
	// sent is the key of the dialog being answered: cleared when the answer fails, once the screen shows
	// another dialog, or after staleRefreshes still showing it
	sent  string
	stale int // refreshes since the answer that still show the sent dialog
	// A question answered whole (pick.whole): the option picked on each tab (-1 none), for the question qsig
	// (pick.qsig), and the tab ←→ and 1-9 act on
	picks []int
	qsig  string
	tab   int
}

// open binds the reply box to t, with the reply typed to t's agent pane before focus moved away.
func (m *Model) open(t tile) {
	m.selected = t.sess.Name
	m.box = replyBox{session: t.sess.Name, pane: t.pane, hist: -1}
	if d, ok := m.drafts[t.sess.Name]; ok && d.pane == t.pane {
		m.box.ed = d.ed
	}
	delete(m.drafts, t.sess.Name)
	m.syncPick()
}

// blur closes the reply box as esc does once focus moves to another pane, keeping a typed reply (not a
// dialog's write-in) for its tile.
func (m *Model) blur() {
	if b := m.box; !b.other && len(b.ed.text) > 0 {
		if m.drafts == nil {
			m.drafts = map[string]replyBox{}
		}
		m.drafts[b.session] = b
	}
	m.box = replyBox{}
}

// bound is the tile the reply box is bound to.
func (m Model) bound() (tile, bool) {
	for _, t := range m.snap.tiles {
		if t.sess.Name == m.box.session {
			return t, true
		}
	}
	return tile{}, false
}

// activePick is the bound tile's dialog when the box shows it as a picker: a draft typed before it came
// stays a draft.
func (m Model) activePick() *pick {
	t, ok := m.bound()
	if !ok || t.pick == nil || len(m.box.ed.text) > 0 && !m.box.other {
		return nil
	}
	return t.pick
}

// syncPick starts the picker on a new dialog's own cursor, even under a draft (emptied, it shows the
// picker at once), and ends the wait for the screen after an answer.
func (m *Model) syncPick() {
	b := &m.box
	t, ok := m.bound()
	if !ok || t.pick == nil {
		// A capture mid-redraw may miss the dialog: free text being typed stays
		b.sent = ""
		return
	}
	p := t.pick
	if sig := p.d.sig(); sig != b.hlSig {
		if b.other {
			// Typed for another question
			b.other, b.ed = false, editor{}
		}
		b.hlSig, b.hl = sig, max(0, p.d.option(p.d.cursor))
	}
	if p.whole() {
		if qs := p.qsig(); qs != b.qsig {
			b.qsig, b.picks = qs, make([]int, len(p.asks))
			for i, a := range p.asks {
				b.picks[i] = recommended(a)
			}
			b.tab = p.cur
		}
		b.tab = min(len(p.asks)-1, max(b.tab, p.cur))
	}
	if b.sent != "" && m.answering == "" {
		// A capture mid-redraw can take an answer for done: the picker comes back after a few refreshes
		switch b.stale++; {
		case p.d.key() != b.sent:
			b.sent = ""
		case b.stale > staleRefreshes:
			b.sent, m.msg = "", alert("⚠ "+b.session+": the answer didn't take, showing its dialog again")
		}
	}
}

// waiting reports an answer whose keys are in flight, or that took while the screen still shows its dialog.
func (m Model) waiting(p *pick) bool {
	return m.answering != "" || m.box.sent != "" && p.d.key() == m.box.sent
}

// handleKey acts on the selected card: j/k/↓/↑ move (wrapping), g/⏎ switch to the worker, m (i) reply to it
// here, 1-9 answer its dialog or its question's options, o open its PR, x kill (asks y/n), v show more of its screen or less again, f
// fold or unfold it, ctrl+c quit.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.box.session != "" {
		return m.typeBox(k)
	}
	key := k.String()
	m.msg = message{}
	if m.confirm != "" {
		target := m.confirm
		m.confirm = ""
		if key == "y" {
			// The sidebar's × close: for good, archived, its worktree asked about
			return m, m.action(func() error { return sidebar.Close(m.c, m.snap.client, m.snap.name, target) })
		}
		return m, nil
	}
	i := index(m.snap.tiles, m.selected)
	switch key {
	case "j", "down":
		m.selected = m.move(i, 1)
	case "k", "up":
		m.selected = m.move(i, -1)
	case "g", "enter":
		return m, m.switchTo(m.selected)
	case "m", "i":
		if len(m.snap.tiles) > 0 {
			m.open(m.snap.tiles[i])
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if len(m.snap.tiles) == 0 {
			return m, nil
		}
		t := m.snap.tiles[i]
		if opts := t.options(); len(opts) > 0 && t.sess.State != agent.Waiting {
			n := int(key[0] - '1')
			if n >= len(opts) {
				m.msg = info(fmt.Sprintf("%s's question has %d options", t.sess.Name, len(opts)))
				return m, nil
			}
			// As a typed reply, without touching a draft kept for t
			m.box = replyBox{session: t.sess.Name, pane: t.pane, hist: -1}
			m.box.ed.set(opts[n])
			return m.sendReply()
		}
		if t.sess.State != agent.Waiting {
			m.msg = info("1-9 answer a waiting worker's dialog or a question's options; " + t.sess.Name + " has neither (m to reply)")
			return m, nil
		}
		if t.pick != nil {
			// Through the picker: checked against the screen, one answer at a time
			m.open(t)
			return m.typePick(k, t.pick)
		}
		return m, m.action(func() error { return control.SendKeys(m.c, t.pane, []string{key}) })
	case "o":
		return m.openPR(i)
	case "x":
		if len(m.snap.tiles) == 0 {
			return m, nil
		}
		m.confirm = m.snap.tiles[i].sess.Name
	case "v":
		if len(m.snap.tiles) > 0 {
			m.toggleExpand(m.snap.tiles[i])
		}
	case "f":
		if len(m.snap.tiles) > 0 {
			m.fold(m.snap.tiles[i])
		}
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// handleMouse: a left click on a card's ▸/▾ folds or unfolds it; on one of its dialog's options picks it
// (Submit goes on), as its key does; on Open switches to the worker, on Message… opens the reply box for it,
// on its PR opens it, on × asks to close it; elsewhere on a card it selects it, for the keys.
func (m Model) handleMouse(ms tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ms.Action != tea.MouseActionPress || ms.Button != tea.MouseButtonLeft {
		return m, nil
	}
	h, ok := m.hitAt(ms.X, ms.Y)
	if !ok {
		return m, nil
	}
	t := m.snap.tiles[h.tile]
	// A click is no y to a kill asked before it
	m.msg, m.confirm = message{}, ""
	switch h.kind {
	case hitCard:
		// An open reply box would take the keys meant for the card; one whose answer is out keeps its wait
		if t.sess.Name != m.box.session || m.answering != m.box.session && m.box.sent == "" {
			m.blur()
		}
		m.selected = t.sess.Name
		return m, nil
	case hitFold:
		m.fold(t)
		return m, nil
	case hitOpen:
		m.selected = t.sess.Name
		return m, m.switchTo(t.sess.Name)
	case hitPR:
		m.selected = t.sess.Name
		return m.openPR(h.tile)
	case hitClose:
		// Its y would go into a draft, and the box hides the question; a typed reply waits for its box
		m.blur()
		m.selected, m.confirm = t.sess.Name, t.sess.Name
		return m, nil
	}
	answers := h.kind == hitOption || h.kind == hitSubmit
	if answers && m.answering != "" {
		m.msg = info("⏳ answering " + m.answering + "…: pick again once it's done")
		return m, nil
	}
	if t.sess.Name != m.box.session {
		// A click on another card moves the reply box there, keeping a reply typed in it for when it's back;
		// one on the same keeps the draft and an answer's wait
		m.blur()
		m.open(t)
	}
	if !answers {
		return m, nil
	}
	p := m.activePick()
	switch {
	case p == nil:
		m.msg = alert("⚠ " + t.sess.Name + ": send or clear the typed reply to pick an option")
		return m, nil
	case m.waiting(p):
		return m, nil
	case h.kind == hitSubmit:
		return m.answer(p, submit())
	case m.box.other && h.k == p.other:
		// Already typing it
		return m, nil
	case p.whole() && h.k < len(p.asks[p.cur].Options):
		m.box.other, m.box.ed = false, editor{}
		m.pickTab(p, p.cur, h.k)
		return m, nil
	}
	m.box.other, m.box.ed, m.box.hl = false, editor{}, h.k
	return m.pickOption(p, h.k)
}

// move is the session dir tiles away from tile i, wrapping.
func (m Model) move(i, dir int) string {
	n := len(m.snap.tiles)
	if n == 0 {
		return ""
	}
	return m.snap.tiles[((i+dir)%n+n)%n].sess.Name
}

// switchTo switches the client viewing the herd to session.
func (m Model) switchTo(session string) tea.Cmd {
	if session == "" {
		return nil
	}
	args := []string{"switch-client"}
	if m.snap.client != "" {
		args = append(args, "-c", m.snap.client)
	}
	return m.action(func() error {
		_, err := m.c.Run(append(args, "-t", tmux.SessionTarget(session))...)
		return err
	})
}

// openPR opens tile i's PR, or says why there is none.
func (m Model) openPR(i int) (tea.Model, tea.Cmd) {
	if len(m.snap.tiles) == 0 {
		return m, nil
	}
	t := m.snap.tiles[i]
	switch pr := t.sess.PR; {
	case pr.Number != 0:
		return m, m.action(func() error { return m.look.OpenPR(pr.URL) })
	case pr.Err != "":
		m.msg = alert(pr.Err)
	default:
		m.msg = info("No pull request for " + t.sess.Name)
	}
	return m, nil
}

// action runs fn off the update loop and reports its error on the bottom row.
func (m Model) action(fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return actionMsg{alert("❌ " + err.Error())}
		}
		return actionMsg{}
	}
}

// typeBox edits the reply in the box: ⏎ sends it to the worker as a prompt (as dev agent prompt does, so one
// waiting on a decision refuses it), ⇧⏎ (alt+⏎, ctrl+j) adds a line, ↑ on an empty draft recalls the last
// replies, esc closes, ctrl+c quits. A dialog the worker waits on shows as a picker instead.
func (m Model) typeBox(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	m.msg = message{}
	b := &m.box
	if p := m.activePick(); p != nil && !b.other {
		return m.typePick(k, p)
	}
	switch {
	case k.Type == tea.KeyEsc && b.other:
		b.other, b.ed = false, editor{}
	case k.Type == tea.KeyEsc:
		m.box = replyBox{}
	case k.Type == tea.KeyEnter && !k.Alt && b.other:
		return m.sendWriteIn()
	case k.Type == tea.KeyEnter && !k.Alt:
		return m.sendReply()
	case k.Type == tea.KeyUp:
		if !b.ed.up() {
			m.recall(-1)
		}
	case k.Type == tea.KeyDown:
		if !b.ed.down() {
			m.recall(1)
		}
	default:
		edit(&b.ed, k)
		b.hist = -1
	}
	return m, nil
}

// edit applies an editing key to ed.
func edit(ed *editor, k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEnter, tea.KeyCtrlJ:
		ed.insert([]rune{'\n'})
	case tea.KeyRunes:
		r := k.Runes
		if k.Paste {
			r = []rune(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(r)))
		}
		ed.insert(r)
	case tea.KeySpace:
		ed.insert([]rune{' '})
	case tea.KeyBackspace:
		ed.backspace()
	case tea.KeyDelete:
		ed.del()
	case tea.KeyLeft:
		ed.left()
	case tea.KeyRight:
		ed.right()
	case tea.KeyHome, tea.KeyCtrlA:
		ed.home()
	case tea.KeyEnd, tea.KeyCtrlE:
		ed.end()
	case tea.KeyCtrlU:
		ed.killLine()
	case tea.KeyCtrlW:
		ed.deleteWord()
	}
}

// recall steps through the sent replies: dir -1 older (only from an empty draft), +1 newer, past the newest
// back to an empty draft.
func (m *Model) recall(dir int) {
	b := &m.box
	if b.hist < 0 {
		if dir > 0 || b.other || len(b.ed.text) > 0 || len(m.history) == 0 {
			return
		}
		b.hist = len(m.history)
	}
	b.hist = max(0, b.hist+dir)
	if b.hist >= len(m.history) {
		b.hist, b.ed = -1, editor{}
		return
	}
	b.ed.set(m.history[b.hist])
}

// sendReply sends the draft to the bound worker as a prompt and closes the box.
func (m Model) sendReply() (tea.Model, tea.Cmd) {
	text, target, pane := m.box.ed.String(), m.box.session, m.box.pane
	m.box = replyBox{}
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	if n := len(m.history); n == 0 || m.history[n-1] != text {
		m.history = append(m.history, text)[max(0, n+1-maxHistory):]
	}
	return m, func() tea.Msg {
		// The worker may have exited while the reply was typed: its pane's shell would run the text
		if a, err := control.Resolve(m.c, target); err != nil || a.Pane != pane {
			return actionMsg{alert("❌ " + target + ": its agent is gone, reply not sent")}
		}
		queued, err := control.Prompt(m.c, control.RealClock, pane, text)
		if err != nil {
			return actionMsg{alert("❌ " + target + ": " + err.Error())}
		}
		if queued {
			return actionMsg{info("Queued for " + target + ": it reads it at its turn's next step")}
		}
		return actionMsg{info("Sent to " + target)}
	}
}

// typePick answers the bound worker's dialog: ←→ (↑↓) move, ⏎ picks (a multi-select's ⏎ goes on to the next
// question or the review), 1-9 pick that option, space toggles, the free-text option takes typed text, esc
// closes. Keys but esc are ignored while an answer is in flight or the screen still shows its dialog.
func (m Model) typePick(k tea.KeyMsg, p *pick) (tea.Model, tea.Cmd) {
	b := &m.box
	if k.Type == tea.KeyEsc {
		m.box = replyBox{}
		return m, nil
	}
	if m.waiting(p) {
		return m, nil
	}
	if p.whole() && !b.other {
		return m.typeWhole(k, p)
	}
	n := len(p.d.opts)
	switch k.Type {
	case tea.KeyUp, tea.KeyLeft:
		b.hl = (b.hl - 1 + n) % n
	case tea.KeyDown, tea.KeyRight:
		b.hl = (b.hl + 1) % n
	case tea.KeySpace:
		if p.d.opts[b.hl].checkbox {
			return m.pickOption(p, b.hl)
		}
	case tea.KeyEnter:
		if p.d.opts[b.hl].checkbox {
			return m.answer(p, submit())
		}
		return m.pickOption(p, b.hl)
	case tea.KeyRunes:
		// A pasted digit isn't a pick
		if len(k.Runes) == 1 && k.Runes[0] >= '1' && k.Runes[0] <= '9' && !k.Paste {
			if i := int(k.Runes[0] - '1'); i < n {
				b.hl = i
				return m.pickOption(p, i)
			}
		}
	}
	return m, nil
}

// pickOption picks option i: the free-text option opens for typing, a checkbox toggles, any other answers.
func (m Model) pickOption(p *pick, i int) (tea.Model, tea.Cmd) {
	switch {
	case i == p.other:
		m.box.other, m.box.ed = true, editor{}
		return m, nil
	case p.d.opts[i].checkbox:
		return m.answer(p, toggle(i))
	}
	return m.answer(p, choose(i))
}

// sendWriteIn answers the dialog with the free-text option's typed text.
func (m Model) sendWriteIn() (tea.Model, tea.Cmd) {
	text := strings.Join(strings.Fields(m.box.ed.String()), " ")
	if text == "" {
		return m, nil
	}
	t, ok := m.bound()
	if !ok || t.pick == nil || t.pick.other < 0 {
		m.msg = alert("⚠ " + m.box.session + "'s dialog is gone: nothing sent")
		return m, nil
	}
	p := t.pick
	m.box.other, m.box.ed = false, editor{}
	if p.d.opts[p.other].checkbox {
		return m.answer(p, writeInMulti(p.other, text))
	}
	return m.answer(p, writeIn(p.other, text))
}

// answer sends a to the bound worker; the picker waits for it, then for the screen to show what's next.
func (m Model) answer(p *pick, a answer) (tea.Model, tea.Cmd) {
	m.answering, m.box.sent, m.box.stale = m.box.session, p.d.key(), 0
	return m, m.send(m.box.session, m.box.pane, p.d, a)
}
