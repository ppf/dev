package herd

import (
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
)

// chip is an option on a card's dialog and what a click on it does.
type chip struct {
	text string
	h    hit
}

// wrapChips lays chips side by side in rows of w columns, a chip wider than w cut.
func wrapChips(chips []chip, w int) []dialogRow {
	var rows []dialogRow
	var row dialogRow
	x := 0
	for _, c := range chips {
		cw := min(ansi.StringWidth(c.text), w)
		if x > 0 && x+1+cw > w {
			rows, row, x = append(rows, row), dialogRow{}, 0
		}
		if x > 0 {
			row.text += " "
			x++
		}
		c.h.x0, c.h.x1 = x, x+cw
		row.text += ansi.Truncate(c.text, cw, "…")
		row.hits = append(row.hits, c.h)
		x += cw
	}
	return append(rows, row)
}

// starred is label of option k on tab i, marked ★ in place of its "(Recommended)" when the hook's question
// recommends it (agent.Choice.Recommended).
func starred(p *pick, i, k int, label string) string {
	if i < 0 || i >= len(p.asks) || k >= len(p.asks[i].Options) || !p.asks[i].Options[k].Recommended() {
		return label
	}
	return "★ " + strings.TrimSpace(strings.Replace(label, "(Recommended)", "", 1))
}

// picked is the option picked on tab i of t's question, answered whole in the reply box bound to it; -1
// for none, or when the box isn't answering it whole.
func (m Model) picked(t tile, i int) int {
	if m.box.session != t.sess.Name || m.activePick() == nil || !t.pick.whole() || i >= len(m.box.picks) {
		return -1
	}
	return m.box.picks[i]
}

// focusMark marks tab i of t's question when ←→ and 1-9 act on it.
func (m Model) focusMark(t tile, i int) string {
	if m.box.session != t.sess.Name || m.activePick() == nil || !t.pick.whole() || m.box.tab != i {
		return ""
	}
	return sidebar.StateColour(t.sess.Agent, t.sess.State) + bold + "▸ " + reset
}

// tabRows are the tabs of t's question but the open one, w columns: those after it, each its header, its
// question and its options (★ the recommended one; the board's pick bracketed while answering it whole),
// then the ones answered before it.
func (m Model) tabRows(t tile, w int) []dialogRow {
	p := t.pick
	if !p.question() || len(p.asks) < 2 || p.cur >= len(p.asks) {
		return nil
	}
	var rows []dialogRow
	accent, waiting := sidebar.StateColour(t.sess.Agent, t.sess.State), m.answering != "" || m.box.session == t.sess.Name && m.waiting(p)
	for i, a := range p.asks[p.cur+1:] {
		i += p.cur + 1
		q := strings.Join(strings.Fields(a.Question), " ")
		rows = append(rows, dialogRow{text: m.focusMark(t, i) + bold + a.Header + reset + "  " + q})
		chips := make([]chip, len(a.Options))
		for k, c := range a.Options {
			label := strconv.Itoa(k+1) + " " + starred(p, i, k, strings.Join(strings.Fields(c.Label), " "))
			switch {
			case waiting:
				chips[k].text = rule + " " + label + " " + reset
			case k == m.picked(t, i):
				chips[k].text = accent + bold + "[" + label + "]" + reset
			default:
				chips[k].text = " " + label + " "
			}
		}
		// No hits: a click picks only on the open tab
		chipRows := wrapChips(chips, w)
		for r := range chipRows {
			chipRows[r].hits = nil
		}
		if i == m.box.tab && m.focusMark(t, i) != "" {
			chipRows[len(chipRows)-1].hits = []hit{{kind: hitFocus}}
		}
		rows = append(rows, chipRows...)
	}
	for i, a := range p.asks[:p.cur] {
		state := "  not answered"
		if p.answered()[i] {
			state = "  answered"
		}
		rows = append(rows, dialogRow{text: rule + a.Header + state + reset})
	}
	return rows
}

// typeWhole picks the answers of a question answered whole (pick.whole): ←→ move between its tabs from the
// open one, ↑↓ change the tab's pick, 1-9 pick an option and move to the next tab, ⏎ sends every pick and
// submits them (sendAll). The open tab's free-text and other options past the question's own answer it
// alone, at once, as typePick does.
func (m Model) typeWhole(k tea.KeyMsg, p *pick) (tea.Model, tea.Cmd) {
	b := &m.box
	n := len(p.asks[b.tab].Options)
	switch k.Type {
	case tea.KeyLeft:
		b.tab = max(p.cur, b.tab-1)
	case tea.KeyRight:
		b.tab = min(len(p.asks)-1, b.tab+1)
	case tea.KeyUp:
		b.picks[b.tab] = (max(b.picks[b.tab], 0) - 1 + n) % n
	case tea.KeyDown:
		b.picks[b.tab] = (b.picks[b.tab] + 1) % n
	case tea.KeyEnter:
		if i := slices.Index(b.picks[p.cur:], -1); i >= 0 {
			b.tab = p.cur + i
			m.msg = info("pick " + p.asks[b.tab].Header + " first")
			return m, nil
		}
		m.answering, b.sent, b.stale = b.session, p.d.key(), 0
		return m, m.sendAll(b.session, b.pane, p.d, p.asks, p.cur, slices.Clone(b.picks))
	case tea.KeyRunes:
		// A pasted digit isn't a pick
		if len(k.Runes) != 1 || k.Runes[0] < '1' || k.Runes[0] > '9' || k.Paste {
			return m, nil
		}
		switch i := int(k.Runes[0] - '1'); {
		case i < n:
			m.pickTab(p, b.tab, i)
		case b.tab == p.cur && i < len(p.d.opts):
			b.hl = i
			return m.pickOption(p, i)
		}
	}
	return m, nil
}

// pickTab picks option k on tab i of p, a question answered whole, and moves on to the next tab.
func (m *Model) pickTab(p *pick, i, k int) {
	m.box.picks[i] = k
	m.box.tab = min(len(p.asks)-1, i+1)
}
