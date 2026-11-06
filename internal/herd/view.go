package herd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
)

const (
	rule  = sidebar.Faint
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
	// cursorCell is a blank cell in reverse video: the cursor at a draft's end
	cursorCell = "\x1b[7m \x1b[27m"

	// maxDraftRows bounds the rows a draft grows to.
	maxDraftRows = 8
)

// View renders the cards, then the bottom row.
func (m Model) View() string {
	if m.height == 0 {
		return ""
	}
	return strings.Join(m.render(), "\n")
}

// render lays out exactly m.height lines: the board scrolled to the selected card, then the bottom rows.
func (m Model) render() []string {
	w, h := m.width, m.height
	lines := make([]string, h)
	bottom := m.bottom()
	gh := h - len(bottom)
	copy(lines[gh:], bottom)
	if len(m.snap.tiles) == 0 {
		if gh > 0 {
			lines[0] = sidebar.HintRow("No worker sessions yet: ask the coordinator to start some", w)
		}
		return lines
	}
	b := m.board()
	off := b.offset(index(m.snap.tiles, m.selected), gh)
	copy(lines[:gh], b.lines[off:])
	return lines
}

// boardHeight is the rows the board gets above the bottom rows.
func (m Model) boardHeight() int { return m.height - len(m.bottom()) }

// bottom is the rows under the cards: the reply box while open, else one row of hints or a message.
func (m Model) bottom() []string {
	w := m.width
	if m.box.session != "" {
		t, ok := m.bound()
		if !ok {
			t = tile{sess: sidebar.Session{Name: m.box.session}}
		}
		rows := m.boxRows(t)
		// A pane too short for the box keeps its bottom rows
		return rows[max(0, len(rows)-m.height):]
	}
	tiles := m.snap.tiles
	switch {
	case m.confirm != "":
		return []string{sidebar.AlertRow("Kill "+m.confirm+"? y/n", w)}
	case m.msg != "":
		return []string{sidebar.AlertRow(m.msg, w)}
	case m.err != nil:
		return []string{sidebar.AlertRow(m.err.Error(), w)}
	}
	pages := ""
	sel := index(tiles, m.selected)
	if len(tiles) > 0 && len(m.board().lines) > m.height-1 {
		// Where the selection is on a board taller than the pane; first, so a narrow row cuts the hint
		pages = fmt.Sprintf("%d/%d  ", sel+1, len(tiles))
	}
	hint := "j/k ⏎ switch  i reply  v screen  f fold  o PR  x kill"
	if sel < len(tiles) && tiles[sel].sess.State == agent.Waiting {
		hint = "1-9 answer  i reply  j/k ⏎ switch  v screen  f fold  x kill"
	}
	return []string{sidebar.HintRow(pages+hint, w)}
}

// boxRows is the reply box bound to t: a rule in t's accent naming it (agent, state, branch), then the draft
// (or, while its card's dialog is answered, the free-text answer being typed) behind an accent bar, then a
// hint row when there is one.
func (m Model) boxRows(t tile) []string {
	w := m.width
	accent := sidebar.StateColour(t.sess.Agent, t.sess.State)
	head := sidebar.Header(t.sess, max(1, w-3), false)
	top := accent + "─" + reset + head + " " + accent + strings.Repeat("─", max(0, w-2-ansi.StringWidth(head))) + reset
	inner := max(1, w-2)
	var body []string
	var hint string
	if p := m.activePick(); p != nil {
		body, hint = m.pickRows(p, inner)
	} else {
		body = m.draftRows(inner)
	}
	if m.msg != "" {
		hint = m.msg
	}
	rows := []string{top}
	for _, l := range body {
		rows = append(rows, accent+"▎"+reset+" "+l)
	}
	switch {
	case hint == "":
	case strings.HasPrefix(hint, "❌"), strings.HasPrefix(hint, "⚠"):
		rows = append(rows, sidebar.AlertRow(hint, w))
	default:
		rows = append(rows, sidebar.HintRow(hint, w))
	}
	return rows
}

// draftRows is the draft wrapped to width with its cursor, at most maxDraftRows rows (a third of a short
// pane) scrolled to keep the cursor; an empty draft shows the keys instead.
func (m Model) draftRows(width int) []string {
	if len(m.box.ed.text) == 0 {
		hint := "⏎ send · esc cancel · ⇧⏎ newline"
		if len(m.history) > 0 {
			hint += " · ↑ recent"
		}
		return []string{cursorCell + rule + ansi.Truncate(hint, max(0, width-1), "…") + reset}
	}
	rows, cur := m.box.ed.rows(width)
	if n := min(maxDraftRows, max(1, m.height/3)); len(rows) > n {
		start := min(max(0, cur-n+1), len(rows)-n)
		rows = rows[start : start+n]
	}
	return rows
}

// pickRows is the reply box's body while it answers p, whose options its card shows: the free-text option's
// typed answer while it is typed, else nothing; hint is its keys.
func (m Model) pickRows(p *pick, width int) (body []string, hint string) {
	b := m.box
	if b.other {
		row := p.d.opts[p.other].label + ": "
		if len(b.ed.text) == 0 {
			row += cursorCell + rule + "type the answer" + reset
		} else {
			rows, cur := b.ed.rows(max(1, width-ansi.StringWidth(row)))
			row += rows[cur]
		}
		body = []string{ansi.Truncate(row, width, "…")}
	}
	switch {
	case m.waiting(p):
		hint = "⏳ answering " + b.session + "…"
	case b.other:
		hint = "⏎ send · esc back"
	case p.d.multi:
		hint = "←→ move · space toggle · ⏎ next · 1-9 · esc close"
	default:
		hint = "←→ move · ⏎ pick · 1-9 · esc close"
	}
	return body, hint
}
