package herd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
)

const (
	minTileW = 30
	minTileH = 5
	// maxTiles caps the captures per refresh.
	maxTiles = 12
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

// render lays out exactly m.height lines.
func (m Model) render() []string {
	w, h := m.width, m.height
	lines := make([]string, h)
	tiles := m.snap.tiles
	g := layout(len(tiles), w, h-1)
	sel := index(tiles, m.selected)
	from, to := g.page(sel)
	to = min(to, len(tiles))
	if len(tiles) == 0 {
		lines[0] = sidebar.HintRow("No worker sessions yet: ask the coordinator to start some", w)
	}
	for i := from; i < to; i++ {
		k := i - from
		x, y := k%g.cols*(g.tileW+1), k/g.cols*(g.tileH+1)
		for j, l := range tileLines(tiles[i], g.tileW, g.tileH, tiles[i].sess.Name == m.selected) {
			if y+j < h-1 {
				lines[y+j] = pad(lines[y+j], x) + l
			}
		}
	}
	pages := ""
	if g.perPage > 0 && len(tiles) > g.perPage {
		pages = fmt.Sprintf("  %d/%d", from/g.perPage+1, (len(tiles)+g.perPage-1)/g.perPage)
	}
	switch {
	case m.confirm != "":
		lines[h-1] = sidebar.AlertRow("Kill "+m.confirm+"? y/n", w)
	case m.msg != "":
		lines[h-1] = sidebar.AlertRow(m.msg, w)
	case m.err != nil:
		lines[h-1] = sidebar.AlertRow(m.err.Error(), w)
	default:
		lines[h-1] = sidebar.HintRow("j/k ⏎ switch  o PR  x kill"+pages, w)
	}
	return lines
}

// tileLines is a tile's header and body, each exactly w columns wide.
func tileLines(t tile, w, h int, selected bool) []string {
	lines := []string{pad(sidebar.Header(t.sess, w, selected), w)}
	for _, l := range t.body {
		l = ansi.Truncate(l, w, "")
		lines = append(lines, "\x1b[0m"+l+"\x1b[0m"+strings.Repeat(" ", w-ansi.StringWidth(l)))
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
	g := layout(len(m.snap.tiles), m.width, m.height-1)
	if g.perPage == 0 || x%(g.tileW+1) == g.tileW || y%(g.tileH+1) == g.tileH {
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
