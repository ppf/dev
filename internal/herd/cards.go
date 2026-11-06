package herd

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
)

// screenRows is how much of its pane's screen an open card shows; the selected one shows half the pane while
// expanded (v).
const screenRows = 5

// sectionNames and sectionColours label the sections, in their order.
var (
	sectionNames   = [...]string{"NEEDS YOU", "WORKING", "WAITING", "IDLE"}
	sectionColours = [...]string{sidebar.Red, sidebar.Gold, sidebar.Green, sidebar.Muted}
)

// healthNotes are the note kinds a card flags with ⚠: something went wrong, or will.
var healthNotes = map[string]bool{inbox.Failed: true, inbox.Stuck: true, inbox.Context: true}

// hitKind is what a click on a board cell does.
type hitKind int

const (
	// hitCard selects the card and opens the reply box for it.
	hitCard hitKind = iota
	// hitFold folds or unfolds the card.
	hitFold
)

// hit is the cells x0 to x1 (exclusive) of board row y, on tile's card.
type hit struct {
	y, x0, x1, tile int
	kind            hitKind
}

// board is the cards in sections as rows of the pane's width, before scrolling: what a click on each cell
// does, and the rows each card spans (its section's header with the first).
type board struct {
	lines []string
	hits  []hit
	spans [][2]int // by tile index: first row and the row after its last
}

// board lays out the cards of m's tiles, which come in section order.
func (m Model) board() board {
	var b board
	w := m.width
	last := section(-1)
	for i, t := range m.snap.tiles {
		top := len(b.lines)
		if s := t.section(); s != last {
			last = s
			n := 0
			for _, u := range m.snap.tiles {
				if u.section() == s {
					n++
				}
			}
			b.lines = append(b.lines, sectionRow(s, n, w))
		}
		rows, arrow := m.card(t, w)
		for j := range rows {
			y := len(b.lines) + j
			b.hits = append(b.hits, hit{y: y, x0: 0, x1: w, tile: i, kind: hitCard})
			if j == arrow.y {
				// The arrow, a cell wider each side so it's easy to hit
				b.hits = append(b.hits, hit{y: y, x0: arrow.x - 1, x1: arrow.x + 2, tile: i, kind: hitFold})
			}
		}
		b.lines = append(b.lines, rows...)
		b.spans = append(b.spans, [2]int{top, len(b.lines)})
	}
	return b
}

// offset is the first board row shown in h rows so the card of tile sel shows whole, or its top when it's
// taller than h.
func (b board) offset(sel, h int) int {
	if sel >= len(b.spans) || h < 1 {
		return 0
	}
	top, end := b.spans[sel][0], b.spans[sel][1]
	return max(0, min(top, end-h, len(b.lines)-h))
}

// hitAt is what's under column x, row y of the pane, on the board as scrolled.
func (m Model) hitAt(x, y int) (hit, bool) {
	gh := m.boardHeight()
	if len(m.snap.tiles) == 0 || y < 0 || y >= gh {
		return hit{}, false
	}
	b := m.board()
	y += b.offset(index(m.snap.tiles, m.selected), gh)
	// The last match wins: a fold arrow over its card's row
	for _, h := range slices.Backward(b.hits) {
		if h.y == y && x >= h.x0 && x < h.x1 {
			return h, true
		}
	}
	return hit{}, false
}

// folded reports whether t's card shows as one row: never under Needs you, else the user's choice, else an
// idle card without a warning is.
func (t tile) folded(folds map[string]bool) bool {
	if t.section() == needsYouSection {
		return false
	}
	if f, ok := folds[t.sess.Name]; ok {
		return f
	}
	return t.section() == restingSection && len(warnings(t)) == 0
}

// fold flips t's card between one row and open, for as long as its session is listed; a Needs you card stays
// open.
func (m *Model) fold(t tile) {
	if t.section() == needsYouSection {
		return
	}
	if m.folds == nil {
		m.folds = map[string]bool{}
	}
	m.folds[t.sess.Name] = !t.folded(m.folds)
}

// sectionRow is section s's header: its name and count in its colour, ruled to width w.
func sectionRow(s section, n, w int) string {
	label := " " + sectionNames[s] + " " + strconv.Itoa(n) + " "
	return sectionColours[s] + bold + label + reset + rule + strings.Repeat("─", max(0, w-ansi.StringWidth(label))) + reset
}

// card is t's rows, each w columns wide, and where its fold arrow is: one row when folded, else a
// box: its name and time, title, status, branch and cost, PR, what it last said, its notes, then its screen.
// A Needs you or Waiting card has a bar in its section's colour on its left; the selected card's box is in
// its state's colour.
func (m Model) card(t tile, w int) (rows []string, arrow struct{ x, y int }) {
	sec := t.section()
	gutter := " "
	if sec == needsYouSection || sec == waitingSection {
		gutter = sectionColours[sec] + "┃" + reset
	}
	selected := t.sess.Name == m.selected
	frame := rule
	if selected {
		frame = sidebar.StateColour(t.sess.Agent, t.sess.State)
	}
	if t.folded(m.folds) {
		arrow.x = 2
		row := m.headRow(t, sec, true, w-1)
		if selected {
			row = sidebar.SelectedBG + strings.ReplaceAll(row, reset, reset+sidebar.SelectedBG) + reset
		}
		return []string{gutter + row}, arrow
	}
	iw := max(1, w-3)
	line := func(s string) string {
		s = ansi.Truncate(s, iw, "…")
		return gutter + frame + "│" + reset + s + strings.Repeat(" ", max(0, iw-ansi.StringWidth(s))) + frame + "│" + reset
	}
	rows = append(rows, gutter+frame+"╭"+strings.Repeat("─", iw)+"╮"+reset)
	rows = append(rows, line(m.headRow(t, sec, false, iw)))
	// The rows under the name start under it
	in := "    "
	text := func(colour, s string) {
		if s != "" {
			rows = append(rows, line(in+colour+s+reset))
		}
	}
	if t.sess.Title != "" && t.sess.Title != t.sess.Name {
		text(rule, t.sess.Title)
	}
	status := statusText(t, sec)
	if sec == needsYouSection {
		text(sidebar.Red+bold, status)
	} else {
		text(sidebar.Muted, status)
	}
	if t.sess.Branch != "" || t.sess.Cost != "" || t.context != "" {
		rows = append(rows, line(in+metaRow(t, iw-len(in))))
	}
	if pr := t.sess.PR; pr.Number != 0 {
		text(sidebar.PRColour(pr.State), prIcon(pr.State)+" PR #"+strconv.Itoa(pr.Number)+" "+pr.State)
	}
	for _, l := range said(t) {
		text(rule, l)
	}
	for _, n := range warnings(t) {
		text(sidebar.Gold, "⚠ "+n)
	}
	if shown := m.screenRows(t); len(shown) > 0 {
		rows = append(rows, line(in+rule+"┄ screen "+strings.Repeat("┄", max(0, iw-len(in)-10))+reset))
		for _, l := range shown {
			rows = append(rows, line(in+reset+l+reset))
		}
	}
	rows = append(rows, gutter+frame+"╰"+strings.Repeat("─", iw)+"╯"+reset)
	arrow.x, arrow.y = 3, 1
	return rows, arrow
}

// headRow is a card's first row, w columns: its fold arrow, state dot and name, then at the right its time in
// its state. Folded, the status, PR and a ⚠ for a warning follow the name.
func (m Model) headRow(t tile, sec section, folded bool, w int) string {
	arrow := "▾"
	if folded {
		arrow = "▸"
	}
	left := rule + arrow + reset + " " + sidebar.StateColour(t.sess.Agent, t.sess.State) + "●" + reset + " " + bold + t.sess.Name + reset
	if folded {
		if s := statusText(t, sec); s != "" {
			left += "  " + sidebar.Muted + s + reset
		}
		if pr := t.sess.PR; pr.Number != 0 {
			left += "  " + sidebar.PRColour(pr.State) + prIcon(pr.State) + " #" + strconv.Itoa(pr.Number) + reset
		}
		if len(warnings(t)) > 0 {
			left += " " + sidebar.Gold + "⚠" + reset
		}
	}
	right := ""
	if !t.since.IsZero() {
		right = sidebar.Muted + span(m.clk.Now().Sub(t.since)) + reset + " "
	}
	room := max(1, w-ansi.StringWidth(right)-1)
	left = ansi.Truncate(" "+left, room, "…")
	return left + strings.Repeat(" ", max(1, w-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}

// span is d as a card's time: now under a minute, else inbox's 12m, 3h.
func span(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	return inbox.Span(d)
}

// statusText is what a card says about its worker, leaving out what its section says: the dialog it waits on
// or the question its turn ended on, what its tool call does (else the tool), what it waits on, or how its
// last turn ended.
func statusText(t tile, sec section) string {
	clean := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	switch sec {
	case needsYouSection:
		switch {
		// The dialog on screen first: the hook's record may be of an earlier question or command
		case t.pick != nil && t.pick.tool != "":
			return clean("allow " + t.pick.tool + ": " + t.pick.detail)
		case t.pick != nil && t.pick.d.title != "":
			return clean(t.pick.d.title)
		case t.deciding && t.question.Tool != "":
			return clean(t.question.Ask())
		case t.deciding:
			return "waiting for you"
		}
		_, ask, _ := strings.Cut(t.note, ": ")
		return clean(cmp.Or(ask, "asks"))
	case workingSection:
		return clean(cmp.Or(t.tail.ToolDescription, t.tail.Tool, "working"))
	case waitingSection:
		_, what, _ := desktop.Marker(t.tail.Lines)
		return clean(what)
	}
	if healthNotes[noteKind(t.note)] || staleNote(t) {
		return ""
	}
	return noteKind(t.note)
}

// staleNote reports a needs a decision note outliving its dialog (answered No or Esc, which fires no hook):
// the worker no longer asks anything.
func staleNote(t tile) bool {
	return !t.deciding && noteKind(t.note) == inbox.Decision
}

// metaRow is the branch, cut at its start so its end shows, and at the right the cost and context use, w columns.
func metaRow(t tile, w int) string {
	var right []string
	if t.sess.Cost != "" {
		right = append(right, t.sess.Cost)
	}
	if t.context != "" {
		right = append(right, t.context+"%")
	}
	r := strings.Join(right, " · ")
	room := max(0, w-ansi.StringWidth(r)-2)
	branch := t.sess.Branch
	switch {
	case room == 0:
		branch = ""
	case ansi.StringWidth(branch) > room:
		branch = ansi.TruncateLeft(branch, ansi.StringWidth(branch)-room+1, "…")
	}
	gap := max(1, w-ansi.StringWidth(branch)-ansi.StringWidth(r)-1)
	return rule + branch + reset + strings.Repeat(" ", gap) + sidebar.Muted + r + reset
}

// prIcon is a PR state's glyph, each its own shape.
func prIcon(state string) string {
	switch state {
	case "merged":
		return "◆"
	case "closed":
		return "✕"
	case "draft":
		return "◌"
	}
	return "○"
}

// lastSaid is how many of its newest reply's lines a card shows.
const lastSaid = 2

// said is the last lines of t's newest reply, without its markdown's backticks and bold or a turn-end marker
// line (the status says it); with no reply read, its note when that isn't a warning.
func said(t tile) []string {
	strip := strings.NewReplacer("**", "", "`", "")
	var lines []string
	for _, l := range t.tail.Lines {
		if _, _, marker := desktop.Marker([]string{l}); !marker {
			lines = append(lines, strip.Replace(l))
		}
	}
	if len(lines) == 0 && t.note != "" && !healthNotes[noteKind(t.note)] && !staleNote(t) {
		if _, text, ok := strings.Cut(t.note, ": "); ok {
			lines = []string{text}
		}
	}
	return tail(lines, lastSaid)
}

// warnings are what a card flags with ⚠: a health note, a transcript it could not read or whose rows didn't parse.
func warnings(t tile) []string {
	var ws []string
	if healthNotes[noteKind(t.note)] {
		ws = append(ws, t.note)
	}
	if t.tailErr != nil {
		// The cause before the path, which a card's width would cut it off behind
		var pe *fs.PathError
		if errors.As(t.tailErr, &pe) {
			ws = append(ws, "transcript: "+pe.Op+": "+pe.Err.Error()+" ("+pe.Path+")")
		} else {
			ws = append(ws, "transcript: "+t.tailErr.Error())
		}
	}
	if t.tail.Broken > 0 {
		ws = append(ws, fmt.Sprintf("transcript: %d rows did not parse", t.tail.Broken))
	}
	return ws
}

// screenRows is the end of t's captured screen its card shows: screenRows rows, half the pane for the
// selected card while expanded; none when not captured.
func (m Model) screenRows(t tile) []string {
	n := screenRows
	if m.expand && t.sess.Name == m.selected {
		n = max(n, m.height/2)
	}
	return tail(t.body, n)
}
