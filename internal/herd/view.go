package herd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
)

const (
	minTileW = 30
	minTileH = 5
	// maxTiles caps the captures per refresh.
	maxTiles = 12

	rule      = "\x1b[38;2;84;84;109m" // the sidebar's faint
	headerBG  = "\x1b[48;2;30;30;38m"
	waitingBG = "\x1b[48;2;48;28;32m"
	ink       = "\x1b[38;2;31;31;40m" // text on an accent background
	bold      = "\x1b[1m"
	reset     = "\x1b[0m"
	// cursorCell is a blank cell in reverse video: the cursor at a draft's end
	cursorCell = "\x1b[7m \x1b[27m"

	// maxDraftRows bounds the rows a draft grows to.
	maxDraftRows = 8
)

// tileMode is how a tile stands out: the reply box's tile in its accent, the others dimmed behind it.
type tileMode int

const (
	plainTile tileMode = iota
	selectedTile
	boundTile
	dimTile
)

// grid is the tile layout: cols×rows tiles of tileW×tileH, one column and one row apart; perPage
// tiles show at once.
type grid struct {
	cols, rows, tileW, tileH, perPage int
}

// layout fits n tiles into w×h: the most tiles on one page first, then the squarest tiles (a cell is
// about twice as tall as wide), then fewer columns.
func layout(n, w, h int) grid {
	if n == 0 || w < 1 || h < 1 {
		return grid{}
	}
	var best grid
	bestScore := -1
	for cols := 1; cols <= min(n, maxTiles); cols++ {
		tileW := (w - (cols - 1)) / cols
		if cols > 1 && tileW < minTileW {
			break
		}
		rows := (n + cols - 1) / cols
		if fit := max(1, (h+1)/(minTileH+1)); rows > fit {
			rows = fit
		}
		rows = max(1, min(rows, maxTiles/cols))
		g := grid{cols: cols, rows: rows, tileW: tileW, tileH: (h - (rows - 1)) / rows, perPage: min(n, cols*rows)}
		score := g.perPage*10000 + min(g.tileW, 2*g.tileH)
		if score > bestScore {
			best, bestScore = g, score
		}
	}
	return best
}

// page is the range of tile indexes on the page holding tile i.
func (g grid) page(i int) (from, to int) {
	if g.perPage == 0 {
		return 0, 0
	}
	from = i / g.perPage * g.perPage
	return from, from + g.perPage
}

// View renders the tiles of the selected page, then the bottom row.
func (m Model) View() string {
	if m.height == 0 {
		return ""
	}
	return strings.Join(m.render(), "\n")
}

// render lays out exactly m.height lines: the tiles (or the compact list), then the bottom rows.
func (m Model) render() []string {
	w, h := m.width, m.height
	lines := make([]string, h)
	bottom := m.bottom()
	gh := h - len(bottom)
	tiles := m.snap.tiles
	if len(tiles) == 0 && gh > 0 {
		lines[0] = sidebar.HintRow("No worker sessions yet: ask the coordinator to start some", w)
	}
	copy(lines[gh:], bottom)
	if m.compact {
		m.renderList(lines[:gh])
		return lines
	}
	g := layout(len(tiles), w, gh)
	sel := index(tiles, m.selected)
	from, to := g.page(sel)
	to = min(to, len(tiles))
	// The reply box's tile, framed in its accent: -1 when the box is closed or its tile on another page
	hot, accent := -1, ""
	if m.box.session != "" {
		if i := slices.IndexFunc(tiles, func(t tile) bool { return t.sess.Name == m.box.session }); i >= from && i < to {
			hot, accent = i, sidebar.StateColour(tiles[i].sess.Agent, tiles[i].sess.State)
		}
	}
	col := func(rowStart int) int {
		if hot >= rowStart && hot < rowStart+g.cols {
			return hot - rowStart
		}
		return -1
	}
	for k := 0; from+k < to; k += g.cols {
		y := k / g.cols * (g.tileH + 1)
		hotCol := col(from + k)
		var row [][]string
		for i := from + k; i < min(from+k+g.cols, to); i++ {
			row = append(row, tileLines(tiles[i], g.tileW, g.tileH, m.mode(i, hot), accent))
		}
		for j := range min(g.tileH, gh-y) {
			var b strings.Builder
			for c, tl := range row {
				if c > 0 {
					b.WriteString(paint(rule, accent, c-1 == hotCol || c == hotCol) + "│" + reset)
				}
				b.WriteString(tl[j])
			}
			lines[y+j] = b.String()
		}
		if below := from + k + g.cols; below < to && y+g.tileH < gh {
			lines[y+g.tileH] = divider(g, to-below, max(hotCol, col(below)), accent)
		}
	}
	return lines
}

// mode is how tile i shows, hot being the reply box's tile (-1 for none).
func (m Model) mode(i, hot int) tileMode {
	switch {
	case i == hot:
		return boundTile
	case m.box.session != "":
		return dimTile
	case m.snap.tiles[i].sess.Name == m.selected:
		return selectedTile
	}
	return plainTile
}

// renderList fills lines with the compact view: per worker its tile header, then its latest note.
func (m Model) renderList(lines []string) {
	tiles := m.snap.tiles
	hot, accent := -1, ""
	if i := slices.IndexFunc(tiles, func(t tile) bool { return t.sess.Name == m.box.session }); i >= 0 {
		hot, accent = i, sidebar.StateColour(tiles[i].sess.Agent, tiles[i].sess.State)
	}
	rows := m.listRows(len(lines))
	for y := 0; y < len(rows); {
		i, n := rows[y], 1
		for y+n < len(rows) && rows[y+n] == i {
			n++
		}
		t := tiles[i]
		t.body = nil
		if t.note != "" {
			t.body = []string{rule + "   " + t.note + reset}
		}
		copy(lines[y:], tileLines(t, m.width, n, m.mode(i, hot), accent))
		y += n
	}
}

// rows is the tile's height in the compact list: its header, and its note when it has one.
func (t tile) rows() int {
	if t.note == "" {
		return 1
	}
	return 2
}

// listHeight is the compact list's full height.
func (m Model) listHeight() int {
	h := 0
	for _, t := range m.snap.tiles {
		h += t.rows()
	}
	return h
}

// listRows is the compact list's tile index per row, at most h rows: each tile takes a row, two with a note,
// from the first tile that keeps the selected one in view.
func (m Model) listRows(h int) []int {
	tiles := m.snap.tiles
	if len(tiles) == 0 || h < 1 {
		return nil
	}
	size := func(i int) int { return tiles[i].rows() }
	sel := index(tiles, m.selected)
	start, used := sel, size(sel)
	for start > 0 && used+size(start-1) <= h {
		start--
		used += size(start)
	}
	var rows []int
	for i := start; i < len(tiles) && len(rows) < h; i++ {
		for range size(i) {
			rows = append(rows, i)
		}
	}
	return rows[:min(len(rows), h)]
}

// paint is accent when hot, else base.
func paint(base, accent string, hot bool) string {
	if hot {
		return accent
	}
	return base
}

// gridHeight is the rows the tiles get above the bottom rows.
func (m Model) gridHeight() int { return m.height - len(m.bottom()) }

// bottom is the rows under the tiles: the reply box while open, else one row of hints or a message.
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
	g := layout(len(tiles), w, m.height-1)
	sel := index(tiles, m.selected)
	switch {
	case m.compact && len(m.listRows(m.height-1)) < m.listHeight():
		// Where the selection is in a list longer than the pane
		pages = fmt.Sprintf("%d/%d  ", sel+1, len(tiles))
	case !m.compact && g.perPage > 0 && len(tiles) > g.perPage:
		// First, so a narrow row cuts the hint, not the page
		from, _ := g.page(sel)
		pages = fmt.Sprintf("%d/%d  ", from/g.perPage+1, (len(tiles)+g.perPage-1)/g.perPage)
	}
	// v early: the compact view is narrow
	hint := "j/k ⏎ switch  v view  i reply  o PR  x kill"
	if sel < len(tiles) && tiles[sel].sess.State == agent.Waiting {
		hint = "1-9 answer  v view  i reply  j/k ⏎ switch  x kill"
	}
	return []string{sidebar.HintRow(pages+hint, w)}
}

// boxRows is the reply box bound to t: a rule in t's accent naming it (agent, state, branch), then the draft
// or the picker for its dialog behind an accent bar, then a hint row when there is one.
func (m Model) boxRows(t tile) []string {
	w := m.width
	accent := sidebar.StateColour(t.sess.Agent, t.sess.State)
	head := sidebar.Header(t.sess, max(1, w-3), false)
	top := accent + "─" + reset + head + " " + accent + strings.Repeat("─", max(0, w-2-ansi.StringWidth(head))) + reset
	inner := max(1, w-2)
	// The tiles keep at least half the height
	room := max(1, m.height/2-2)
	var body []string
	var hint string
	if p := m.activePick(); p != nil {
		body, hint = m.pickRows(p, accent, inner, room)
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

// pickRows is the picker for p in at most room rows: the permission's tool and command, the question tabs,
// the question, then one row per option, scrolled to keep the highlighted one; hint is its keys.
func (m Model) pickRows(p *pick, accent string, width, room int) (body []string, hint string) {
	b := m.box
	line := func(s string) { body = append(body, ansi.Truncate(s, width, "…")) }
	if p.tool != "" {
		line(bold + p.tool + reset + "  " + strings.Join(strings.Fields(p.detail), " "))
		if p.about != "" {
			line(rule + p.about + reset)
		}
	}
	if len(p.tabs) > 1 {
		tabs := make([]string, len(p.tabs))
		for i, tab := range p.tabs {
			tabs[i] = rule + tab + reset
			if i == p.cur {
				tabs[i] = bold + accent + tab + reset
			}
		}
		line(strings.Join(tabs, rule+" · "+reset))
	}
	if p.d.title != "" {
		line(bold + p.d.title + reset)
	}
	head := len(body)
	labelW := 0
	for _, o := range p.d.opts {
		labelW = max(labelW, ansi.StringWidth(o.label))
	}
	labelW = min(labelW, width/2)
	for i, o := range p.d.opts {
		row := "  "
		if i == b.hl {
			row = accent + "❯ " + reset
		}
		row += rule + strconv.Itoa(o.num) + "." + reset + " "
		if o.checkbox {
			if o.checked {
				row += accent + "☑" + reset + " "
			} else {
				row += "☐ "
			}
		}
		typing := i == b.hl && b.other
		switch {
		case typing && len(b.ed.text) == 0:
			row += cursorCell + rule + "type the answer" + reset
		case typing:
			rows, cur := b.ed.rows(max(1, width-ansi.StringWidth(row)))
			row += rows[cur]
		case i == b.hl:
			row += bold + o.label + reset
		default:
			row += o.label
		}
		if o.desc != "" && !typing {
			row += strings.Repeat(" ", max(0, labelW-ansi.StringWidth(o.label))) + "  " + rule + o.desc + reset
		}
		line(row)
	}
	if len(body) > room {
		// Options first: as many as fit under what of the head fits, from the highlighted one up
		opts := body[head:]
		n := min(len(opts), room)
		start := min(max(0, b.hl-n+1), len(opts)-n)
		body = append(body[max(0, head-(room-n)):head:head], opts[start:start+n]...)
	}
	switch {
	case m.waiting(p):
		hint = "⏳ answering " + b.session + "…"
	case b.other:
		hint = "⏎ send · esc back"
	case p.d.multi:
		hint = "↑↓ move · space toggle · ⏎ next · 1-9 · esc close"
	default:
		hint = "↑↓ move · ⏎ pick · 1-9 · esc close"
	}
	return body, hint
}

// divider is the ─ rule between two rows of tiles, crossing the │ rules of a next row holding n tiles; the
// stretch over or under column hot (-1 for none) is in accent.
func divider(g grid, n, hot int, accent string) string {
	var b strings.Builder
	for c := range g.cols {
		b.WriteString(paint(rule, accent, c == hot) + strings.Repeat("─", g.tileW))
		if c == g.cols-1 {
			continue
		}
		b.WriteString(paint(rule, accent, c == hot || c+1 == hot))
		if c+1 < n {
			b.WriteString("┼")
		} else {
			b.WriteString("┴")
		}
	}
	return b.String() + reset
}

// tileLines is a tile's header and body, each exactly w columns wide. A plain header sits on a faint
// background, red-tinted while waiting, so each tile's start shows; a selected one on the selection
// background; the reply box's on its accent. Behind the box the other tiles are dimmed.
func tileLines(t tile, w, h int, mode tileMode, accent string) []string {
	header := pad(sidebar.Header(t.sess, w, mode == selectedTile), w)
	switch mode {
	case plainTile:
		bg := headerBG
		if t.sess.State == agent.Waiting {
			bg = waitingBG
		}
		header = bg + strings.ReplaceAll(header, reset, reset+bg) + reset
	case boundTile:
		header = strings.Replace(accent, "[38;", "[48;", 1) + ink + bold + ansi.Strip(header) + reset
	case dimTile:
		header = headerBG + rule + ansi.Strip(header) + reset
	}
	lines := []string{header}
	for _, l := range t.body {
		l = ansi.Truncate(l, w, "")
		if mode == dimTile {
			l = rule + ansi.Strip(l)
		}
		lines = append(lines, reset+l+reset+strings.Repeat(" ", w-ansi.StringWidth(l)))
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}

// pad extends line with spaces to x columns.
func pad(line string, x int) string {
	return line + strings.Repeat(" ", max(0, x-ansi.StringWidth(line)))
}

// tileAt is the index of the tile at column x, row y, or -1.
func (m Model) tileAt(x, y int) int {
	gh := m.gridHeight()
	if m.compact {
		if rows := m.listRows(gh); y >= 0 && y < len(rows) {
			return rows[y]
		}
		return -1
	}
	g := layout(len(m.snap.tiles), m.width, gh)
	if g.perPage == 0 || y >= gh || x%(g.tileW+1) == g.tileW || y%(g.tileH+1) == g.tileH {
		return -1
	}
	col, row := x/(g.tileW+1), y/(g.tileH+1)
	if col >= g.cols || row >= g.rows {
		return -1
	}
	from, _ := g.page(index(m.snap.tiles, m.selected))
	if i := from + row*g.cols + col; i < len(m.snap.tiles) {
		return i
	}
	return -1
}
