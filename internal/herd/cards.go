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

// screenRows is how much of its pane's screen an open card shows; an expanded one (v) shows half the pane.
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
	// hitCard selects the card.
	hitCard hitKind = iota
	// hitFold folds or unfolds the card.
	hitFold
	// hitOption picks option k of the card's dialog, as its number key does.
	hitOption
	// hitSubmit goes on from a multi-select question, as ⏎ does.
	hitSubmit
	// hitOpen switches to the worker, as g and ⏎ do.
	hitOpen
	// hitMessage opens the reply box, as m does.
	hitMessage
	// hitPR opens its PR, as o does.
	hitPR
	// hitClose asks to close the worker, as x does.
	hitClose
)

// hit is the cells x0 to x1 (exclusive) of board row y, on tile's card; k is the option a hitOption picks.
type hit struct {
	y, x0, x1, tile, k int
	kind               hitKind
}

// board is the cards in sections as rows of the pane's width, before scrolling: what a click on each cell
// does, and the rows each card spans (its section's header with the first).
type board struct {
	lines []string
	hits  []hit
	spans [][2]int // by tile index: first row and the row after its last
	focus int      // the row of the reply box's highlighted option; -1 for none
}

// board lays out the cards of m's tiles, which come in section order.
func (m Model) board() board {
	b := board{focus: -1}
	w := m.width
	picking := m.activePick() != nil
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
		rows, hits := m.card(t, w)
		for j := range rows {
			b.hits = append(b.hits, hit{y: len(b.lines) + j, x0: 0, x1: w, tile: i, kind: hitCard})
		}
		for _, h := range hits {
			h.y += len(b.lines)
			h.tile = i
			b.hits = append(b.hits, h)
			if picking && t.sess.Name == m.box.session && h.kind == hitOption && h.k == m.box.hl {
				b.focus = h.y
			}
		}
		b.lines = append(b.lines, rows...)
		b.spans = append(b.spans, [2]int{top, len(b.lines)})
	}
	return b
}

// offset is the first board row shown in h rows so the card of tile sel shows whole, or its top when it's
// taller than h, scrolled on to keep the reply box's highlighted option in view.
func (b board) offset(sel, h int) int {
	if sel >= len(b.spans) || h < 1 {
		return 0
	}
	top, end := b.spans[sel][0], b.spans[sel][1]
	off := max(0, min(top, end-h, len(b.lines)-h))
	if b.focus >= off+h {
		off = b.focus - h + 1
	}
	return off
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

// toggleExpand shows half the pane of t's screen on its card, or screenRows again; a folded card unfolds,
// expanded.
func (m *Model) toggleExpand(t tile) {
	if m.expands == nil {
		m.expands = map[string]bool{}
	}
	folded := t.folded(m.folds)
	m.expands[t.sess.Name] = folded || !m.expands[t.sess.Name]
	if folded {
		m.fold(t)
	}
}

// sectionRow is section s's header: its name and count in its colour, ruled to width w.
func sectionRow(s section, n, w int) string {
	label := " " + sectionNames[s] + " " + strconv.Itoa(n) + " "
	return sectionColours[s] + bold + label + reset + rule + strings.Repeat("─", max(0, w-ansi.StringWidth(label))) + reset
}

// card is t's rows, each w columns wide, and what clicks on them do (rows counted from the card's first): one
// row when folded, else a box: its name and time, title, status or the dialog it waits on, branch and cost,
// PR, what it last said, its notes, its screen, then its buttons. A Needs you or Waiting card has a bar in its
// section's colour on its left; the selected card's box is in its state's colour.
func (m Model) card(t tile, w int) (rows []string, hits []hit) {
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
	// The arrow, a cell wider each side so it's easy to hit
	// The selected card's name row is on the selection band
	band := func(row string) string {
		if !selected {
			return row
		}
		return sidebar.SelectedBG + strings.ReplaceAll(row, reset, reset+sidebar.SelectedBG) + reset
	}
	if t.folded(m.folds) {
		return []string{gutter + band(m.headRow(t, sec, true, w-1))}, []hit{{y: 0, x0: 1, x1: 4, kind: hitFold}}
	}
	hits = append(hits, hit{y: 1, x0: 2, x1: 5, kind: hitFold})
	iw := max(1, w-3)
	line := func(s string) string {
		s = ansi.Truncate(s, iw, "…")
		return gutter + frame + "│" + reset + s + strings.Repeat(" ", max(0, iw-ansi.StringWidth(s))) + frame + "│" + reset
	}
	rows = append(rows, gutter+frame+"╭"+strings.Repeat("─", iw)+"╮"+reset)
	rows = append(rows, line(band(m.headRow(t, sec, false, iw))))
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
	// Columns before a row's text: the gutter, the box's │ and the indent
	x0 := 2 + len(in)
	// rowHits places a row's hits on the row about to be added
	rowHits := func(hs []hit) {
		for _, h := range hs {
			h.y, h.x0, h.x1 = len(rows), h.x0+x0, h.x1+x0
			hits = append(hits, h)
		}
	}
	switch {
	case t.pick != nil && sec == needsYouSection:
		for _, r := range m.dialogRows(t, iw-len(in)) {
			rowHits(r.hits)
			rows = append(rows, line(in+r.text))
		}
	case sec == needsYouSection:
		text(sidebar.Red+bold, statusText(t, sec))
	default:
		text(sidebar.Muted, statusText(t, sec))
	}
	if t.sess.Branch != "" || t.sess.Cost != "" || t.context != "" {
		rows = append(rows, line(in+metaRow(t, iw-len(in))))
	}
	if pr := t.sess.PR; pr.Number != 0 {
		rows = append(rows, line(in+prColour(pr.State)+prIcon(pr.State)+" PR #"+strconv.Itoa(pr.Number)+" "+pr.State+reset+ciText(t.ci)))
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
	buttons, bhits := m.buttons(t, sec, iw-len(in))
	rowHits(bhits)
	rows = append(rows, line(in+buttons))
	rows = append(rows, gutter+frame+"╰"+strings.Repeat("─", iw)+"╯"+reset)
	return rows, hits
}

// dialogRow is a row of a card's dialog and what clicks on its cells do (columns from the row's start).
type dialogRow struct {
	text string
	hits []hit
}

// dialogRows is the dialog t waits on, w columns: the permission's tool and command and what it says it
// does, the question tabs, the question, its options side by side (wrapping; the highlighted one, the reply
// box's or the dialog's own cursor, bracketed in t's colour; a multi-select's ☐/☑ and its Submit), then a
// question's described options as `label: description`. The options dim while any answer is in flight.
func (m Model) dialogRows(t tile, w int) []dialogRow {
	p := t.pick
	var rows []dialogRow
	add := func(s string) { rows = append(rows, dialogRow{text: s}) }
	if p.tool != "" {
		add(sidebar.Red + bold + p.tool + reset + "  " + strings.Join(strings.Fields(p.detail), " "))
		if p.about != "" {
			add(rule + p.about + reset)
		}
	}
	if len(p.tabs) > 1 {
		tabs := make([]string, len(p.tabs))
		for i, tab := range p.tabs {
			tabs[i] = rule + tab + reset
			if i == p.cur {
				tabs[i] = bold + tab + reset
			}
		}
		add(strings.Join(tabs, rule+" · "+reset))
	}
	if p.d.title != "" {
		add(sidebar.Red + bold + p.d.title + reset)
	}
	bound := m.box.session == t.sess.Name && m.activePick() != nil
	hl := max(0, p.d.option(p.d.cursor))
	if bound {
		hl = m.box.hl
	}
	accent, waiting := sidebar.StateColour(t.sess.Agent, t.sess.State), m.answering != "" || bound && m.waiting(p)
	type chip struct {
		text string
		h    hit
	}
	var chips []chip
	for i, o := range p.d.opts {
		label := strconv.Itoa(o.num) + " "
		switch {
		case o.checkbox && o.checked:
			label += "☑ "
		case o.checkbox:
			label += "☐ "
		}
		label += optionLabel(p, o)
		c := " " + label + " "
		switch {
		case waiting:
			c = rule + c + reset
		case i == hl:
			c = accent + bold + "[" + label + "]" + reset
		}
		chips = append(chips, chip{c, hit{kind: hitOption, k: i}})
	}
	if p.d.multi {
		chips = append(chips, chip{sidebar.Muted + " Submit ⏎ " + reset, hit{kind: hitSubmit}})
	}
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
	rows = append(rows, row)
	for _, o := range p.d.opts {
		if o.desc != "" && p.tool == "" {
			add(rule + strings.Join(strings.Fields(o.label), " ") + ": " + o.desc + reset)
		}
	}
	return rows
}

// optionLabel is o's label on its chip; a permission's indented lines are its label's wrapped rest, a
// question's its description.
func optionLabel(p *pick, o option) string {
	if p.tool != "" && o.desc != "" {
		return strings.Join(strings.Fields(o.label+" "+o.desc), " ")
	}
	return strings.Join(strings.Fields(o.label), " ")
}

// buttons is a card's last row, w columns: Open, Message… and its PR, × at the right, and what clicks on
// them do. Open leads on a Needs you or Waiting card that shows no dialog; else they recede.
func (m Model) buttons(t tile, sec section, w int) (string, []hit) {
	type button struct {
		label string
		kind  hitKind
	}
	bs := []button{{"Open", hitOpen}, {"Message…", hitMessage}}
	if t.sess.PR.Number != 0 {
		bs = append(bs, button{"PR #" + strconv.Itoa(t.sess.PR.Number), hitPR})
	}
	var b strings.Builder
	var hits []hit
	x := 0
	for i, bt := range bs {
		colour := sidebar.Muted
		if i == 0 && (sec == needsYouSection || sec == waitingSection) && t.pick == nil {
			colour = bold
		}
		bw := ansi.StringWidth(bt.label)
		// Room for it and × after it: a narrow card drops the buttons that don't fit whole
		if x+2+bw+3 > w {
			break
		}
		if i > 0 {
			b.WriteString("  ")
			x += 2
		}
		b.WriteString(colour + bt.label + reset)
		hits = append(hits, hit{x0: x, x1: x + bw, kind: bt.kind})
		x += bw
	}
	if gap := w - x - 2; gap > 0 {
		b.WriteString(strings.Repeat(" ", gap) + sidebar.Muted + "×" + reset)
		hits = append(hits, hit{x0: w - 3, x1: w, kind: hitClose})
	}
	return b.String(), hits
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
			left += "  " + prColour(pr.State) + prIcon(pr.State) + " #" + strconv.Itoa(pr.Number) + reset
			if icon := ciIcon(t.ci.state); icon != "" {
				left += " " + icon
			}
			if t.ci.err != "" {
				// Its checks couldn't be read again: the icon may be stale
				left += sidebar.Muted + "?" + reset
			}
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

// prColour is a PR's colour by state as sidebar gives it, but a draft's at the card's muted level: sidebar's faint is too dim on a card.
func prColour(state string) string {
	if state == "draft" {
		return sidebar.Muted
	}
	return sidebar.PRColour(state)
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

// screenRows is the end of t's captured screen its card shows: screenRows rows, half the pane while expanded;
// none when not captured.
func (m Model) screenRows(t tile) []string {
	n := screenRows
	if m.expands[t.sess.Name] {
		n = max(n, m.height/2)
	}
	return tail(t.body, n)
}

// ciIcon is a CI state's glyph in its colour: ✓ passed, ✗ failed, ● running (◷ draws half height in some terminals); "" for none.
func ciIcon(state string) string {
	switch state {
	case ciPassed:
		return sidebar.Green + "✓" + reset
	case ciFailed:
		return sidebar.Red + "✗" + reset
	case ciRunning:
		return sidebar.Gold + "●" + reset
	}
	return ""
}

// ciText is what a PR row says of its checks after the PR: the icon and the checks that make it as last
// read; after a failed read the icon (if any) and why, first, so a narrow row keeps it.
func ciText(c ci) string {
	icon := ciIcon(c.state)
	switch {
	case c.err != "" && icon != "":
		return "  " + icon + " " + sidebar.Muted + "CI unknown: " + c.err + reset
	case c.err != "":
		return "  " + sidebar.Muted + "CI unknown: " + c.err + reset
	case icon != "":
		return "  " + icon + " " + sidebar.Muted + strings.Join(c.names, ", ") + reset
	}
	return ""
}
