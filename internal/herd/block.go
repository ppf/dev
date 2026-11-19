package herd

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

// optSpan is a clickable word's columns [x0, x1) on a line of the block: an answerable option n, or an action's key.
type optSpan struct {
	x0, x1, n int
	key       string
}

// minBlockRows is the fewest rows above the bottom row TO DO shows in.
const minBlockRows = 6

// namesFrom is the narrowest pane whose rows name whom their status is on: narrower ones keep the status word alone,
// as does a row whose text would keep under minTextBesideName beside it.
const (
	namesFrom         = 80
	minTextBesideName = 20
)

// statusColours repeat each status word, in the cards' section colours: working gold, waiting green, idle muted.
var statusColours = map[string]string{
	sidebar.StatusDecide:     sidebar.Red,
	sidebar.StatusYours:      "",
	sidebar.StatusStarting:   sectionColours[workingSection],
	sidebar.StatusWorking:    sectionColours[workingSection],
	sidebar.StatusBlocked:    sidebar.Red,
	sidebar.StatusWaiting:    sectionColours[waitingSection],
	sidebar.StatusIdle:       sectionColours[restingSection],
	sidebar.StatusGone:       sidebar.Red,
	sidebar.StatusCheck:      sidebar.Gold,
	sidebar.StatusBossClosed: sidebar.Muted,
}

// groupTitle is group g's title (sidebar.NeedRow.Group) and colour: Needs you for the sessions, Not read for lists.
func groupTitle(g int) (title, colour string) {
	switch {
	case g < 0:
		return "Needs you", sidebar.Red
	case g >= len(sidebar.Groups):
		return "Not read", sidebar.Gold
	}
	group := sidebar.Groups[g]
	return group.Title, statusColours[group.Statuses[0]]
}

// blockHead is TO DO's header: whose items, the counts (red where the user decides), and the own list's age at the
// right, dimmed once stale; ▸ first while folded (t), "· n" while its keys act on the cards.
func (m Model) blockHead(t sidebar.Tally, w int) string {
	whose := m.snap.name + " and its workers"
	if m.snap.name == sidebar.Manager {
		whose = "all bosses"
	}
	left := ""
	if m.todoFolded {
		left = " " + rule + "▸" + reset
	}
	counts := ""
	if t.NeedYou > 0 {
		counts += sidebar.Red + strconv.Itoa(t.NeedYou) + " " + sidebar.NeedYouWord + reset + sidebar.Muted + " · " + reset
	}
	decide := sidebar.Muted
	if t.Decide > 0 {
		decide = sidebar.Red
	}
	counts += decide + strconv.Itoa(t.Decide) + " to decide" + reset + sidebar.Muted + " · " + reset + strconv.Itoa(t.ToDo) + " yours"
	if !m.needMode {
		counts += rule + " · n" + reset
	}
	scope := sidebar.Muted + " · " + whose + reset
	if ansi.StringWidth(left+" TO DO"+scope+"   "+counts) > w {
		// Narrow: the counts and the key in keep their room, whose items goes
		scope = ""
	}
	left += " " + bold + "TO DO" + reset + scope + "   " + counts
	right := ""
	if l := m.snap.todo; l.Updated != 0 {
		colour := sidebar.Muted
		if l.Stale {
			colour = rule
		}
		right = colour + "updated " + span(m.clk.Now().Sub(time.Unix(l.Updated, 0))) + reset + " "
	}
	if ansi.StringWidth(left)+ansi.StringWidth(right)+1 > w {
		right = ""
	}
	return padRow(ansi.Truncate(left, w-ansi.StringWidth(right), "…")+reset+strings.Repeat(" ", max(0, w-ansi.StringWidth(left)-ansi.StringWidth(right)))+right, w)
}

// blockGroup is one group of the block as laid out: its title line, or for Waiting on others the fold row heading it
// (head, else -1), and its rows' indexes.
type blockGroup struct {
	title string
	head  int
	rows  []int
}

// needBlock is TO DO at the top of the pane, in about half the gh rows (more for a long selected row; none under
// 6): its header, then each group's (sidebar.Groups) title and rows (needLines), every group cut alike to fit, the
// selected row kept, "+N more" for the rest; at is each line's row, -1 for none; spans each line's clickable words.
func (m Model) needBlock(gh int) (lines []string, at []int, spans [][]optSpan) {
	rows := m.needRows()
	if len(rows) == 0 || gh < minBlockRows {
		return nil, nil, nil
	}
	w := m.width
	add := func(l string, row int, sp []optSpan) {
		lines, at, spans = append(lines, l), append(at, row), append(spans, sp)
	}
	add(m.blockHead(m.tally(), w), -1, nil)
	add(padRow("", w), -1, nil)
	if m.todoFolded {
		return lines, at, spans
	}
	labelW := labelWidth(rows, w)
	cur := -1
	if m.needMode {
		cur = min(m.needIdx, len(rows)-1)
	}
	block := make([][]string, len(rows))
	blockSpans := make([][][]optSpan, len(rows))
	for i, r := range rows {
		block[i], blockSpans[i] = m.needLines(r, labelW, w, i == cur)
	}
	var groups []blockGroup
	for i, r := range rows {
		if g := r.Group(); i == 0 || rows[i-1].Group() != g {
			if r.fold != nil {
				groups = append(groups, blockGroup{head: i})
				continue
			}
			title, colour := groupTitle(g)
			groups = append(groups, blockGroup{
				title: padRow(" "+colour+bold+title+reset+"  "+sidebar.Muted+strconv.Itoa(m.groupCount(g))+reset, w),
				head:  -1,
			})
		}
		groups[len(groups)-1].rows = append(groups[len(groups)-1].rows, i)
	}
	// Room for the groups: half the rows less the header, its blank and the blank after
	room := gh/2 - 3
	if cur >= 0 {
		// The selected row fits with its group's title, cut to the pane: the header, its blank and the blank after stay
		room = min(max(room, len(block[cur])+1), gh-3)
		if len(block[cur]) > room-1 && len(rows[cur].Context) > 0 {
			// Too tall: its context goes before its options do
			r := rows[cur]
			r.Context = nil
			block[cur], blockSpans[cur] = m.needLines(r, labelW, w, true)
		}
		if len(block[cur]) > room-1 {
			block[cur], blockSpans[cur] = block[cur][:max(1, room-1)], blockSpans[cur][:max(1, room-1)]
		}
	}
	// window is the rows group g shows with at most c of them: the selected one's among them; hidden, how many not
	window := func(g blockGroup, c int) (shown []int, hidden int) {
		pos := -1
		for j, i := range g.rows {
			if i == cur {
				pos = j
			}
		}
		if pos >= 0 {
			c = max(c, 1)
		}
		start := 0
		if pos >= c {
			start = pos - c + 1
		}
		end := min(len(g.rows), start+c)
		return g.rows[start:end], len(g.rows) - (end - start)
	}
	headLines := func(g blockGroup) int {
		if g.head >= 0 {
			return len(block[g.head])
		}
		return min(1, len(g.title))
	}
	cost := func(g blockGroup, c int) int {
		shown, hidden := window(g, c)
		n := headLines(g)
		for _, i := range shown {
			n += len(block[i])
		}
		return n + min(1, hidden)
	}
	selected := func(g blockGroup) bool { return cur >= 0 && (slices.Contains(g.rows, cur) || g.head == cur) }
	// Every group with as many rows as the others, the most that fit
	most := 0
	for _, g := range groups {
		most = max(most, len(g.rows))
	}
	c := min(most, max(0, room))
	for ; c > 0; c-- {
		total := 0
		for _, g := range groups {
			total += cost(g, c)
		}
		if total <= room {
			break
		}
	}
	// Else a row each while they fit, the rest in one "+N more" kept room for: the selected row's group always, at
	// rest the first, so a dialog its card leaves to the block shows
	first, partial := -1, c == 0
	if partial {
		c, first = 1, 0
		for i, g := range groups {
			if selected(g) {
				first = i
			}
		}
		room = min(max(room, cost(groups[first], c)+1), gh-3)
	}
	reserve := 0
	if partial {
		reserve = cost(groups[first], c) + 1
	}
	head := len(lines)
	used, more, full := 0, 0, false
	for gi, g := range groups {
		need := cost(g, c)
		if gi == first {
			reserve = min(reserve, 1)
		} else if full = full || partial && used+need+reserve > room; full {
			more += len(g.rows)
			if g.head >= 0 && !m.waitOpen {
				more += len(rows[g.head].fold)
			}
			continue
		}
		used += need
		if g.head >= 0 {
			for j, l := range block[g.head] {
				add(l, g.head, blockSpans[g.head][j])
			}
		} else if g.title != "" {
			add(g.title, -1, nil)
		}
		shown, hidden := window(g, c)
		for _, i := range shown {
			for j, l := range block[i] {
				add(l, i, blockSpans[i][j])
			}
		}
		if hidden > 0 {
			add(moreRow(hidden, w), -1, nil)
		}
	}
	if more > 0 {
		add(moreRow(more, w), -1, nil)
	}
	if len(lines)-head > room {
		// Too short for even a line per group: the rows around the selected one
		start := head
		if first := slices.Index(at, cur); cur >= 0 && first >= 0 {
			last := first
			for last+1 < len(at) && at[last+1] == cur {
				last++
			}
			start = max(head, min(first, last+1-room))
		}
		start = min(start, len(lines)-room)
		lines = append(lines[:head], lines[start:start+room]...)
		at = append(at[:head], at[start:start+room]...)
		spans = append(spans[:head], spans[start:start+room]...)
	}
	add(padRow("", w), -1, nil)
	return lines, at, spans
}

func moreRow(n, w int) string {
	return padRow(sidebar.HintRow("  +"+strconv.Itoa(n)+" more", w), w)
}

// tally is what TO DO's rows count (sidebar.Count), folded ones too.
func (m Model) tally() sidebar.Tally {
	var rows []sidebar.NeedRow
	for _, r := range m.listRows() {
		rows = append(rows, r.NeedRow)
	}
	return sidebar.Count(rows)
}

// groupCount is how many rows group g holds, folded ones too.
func (m Model) groupCount(g int) int {
	n := 0
	for _, r := range m.listRows() {
		if r.Group() == g {
			n++
		}
	}
	return n
}

// labelWidth is the rows' label column: the longest label, cut at a fifth of the pane between 12 and 20, or wider
// until no two labels cut alike (hiding which worker an answer goes to), up to 2/5 of the pane.
func labelWidth(rows []needRow, w int) int {
	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, ansi.StringWidth(r.label))
	}
	limit, capW := min(labelW, max(4, w*2/5)), min(20, max(12, w/5))
	for capW < limit && cutsAlike(rows, capW) {
		capW++
	}
	return min(limit, capW)
}

// cutsAlike says two different labels read the same cut at w.
func cutsAlike(rows []needRow, w int) bool {
	cuts := map[string]string{}
	for _, r := range rows {
		cut := ansi.Truncate(r.label, w, "…")
		if full, ok := cuts[cut]; ok && full != r.label {
			return true
		}
		cuts[cut] = r.label
	}
	return false
}

// mark is the row's mark: ● a dialog, ? a question or ask, ☐ the user's item, ● in its status's colour on a worker,
// ◌ waiting on others, ○ a check or a closed boss's, ⚠ a list not read.
func (r needRow) mark() string {
	colour := statusColours[r.Status]
	switch {
	case r.Kind == sidebar.NeedDialog:
		return sidebar.Red + "●" + reset
	case r.Kind == sidebar.NeedAsk || r.Kind == sidebar.NeedListAsk:
		return sidebar.Gold + bold + "?" + reset
	case r.Kind == sidebar.NeedUnread:
		return sidebar.Gold + "⚠" + reset
	case r.Status == sidebar.StatusWaiting:
		return colour + "◌" + reset
	case r.Status == sidebar.StatusCheck || r.Status == sidebar.StatusBossClosed:
		return colour + "○" + reset
	case r.Kind == sidebar.NeedOn && r.Status == "":
		return sidebar.Muted + "◌" + reset
	case r.Kind == sidebar.NeedOn:
		return colour + "●" + reset
	}
	return "☐"
}

// right is what the row says at its right edge, and its colour: the status word first, then whom it names (left
// off with named false): "working · boss-dev", "#18 merged 2d · boss closed", "on Carl" with no status; the user's
// own item its check, a stale list's age or when it ticks by itself ("till 10:00").
func (r needRow) right(now time.Time, named bool) (text, colour string) {
	who := ""
	if named {
		who = r.StatusOn
	}
	with := func(word, more string) string {
		if more == "" {
			return word
		}
		return word + " · " + more
	}
	switch {
	case r.fold != nil || r.Kind == sidebar.NeedDialog || r.Kind == sidebar.NeedAsk || r.Kind == sidebar.NeedUnread:
		return "", ""
	case r.Kind == sidebar.NeedOn && r.Status == "":
		return "on " + r.On, sidebar.Muted
	case r.Status == sidebar.StatusCheck && r.Check != nil:
		more := who
		if r.Closed && named {
			more = "boss closed"
		}
		return with(r.Check.Short(now), more), statusColours[r.Status]
	case r.Kind == sidebar.NeedOn && r.Check != nil && r.Check.Kind != sidebar.CheckBossClosed:
		// A check its status doesn't say (unchanged, a failing lookup, a moved PR on a blocked or gone worker)
		return with(with(r.Status, who), "⚠ "+r.Check.Short(now)), statusColours[r.Status]
	case r.Kind == sidebar.NeedOn || r.Status == sidebar.StatusBossClosed:
		return with(r.Status, who), statusColours[r.Status]
	case r.Status == sidebar.StatusStarting:
		text, colour := "sent", statusColours[r.Status]
		if who != "" {
			text = "sent to " + who
		}
		text = with(text, wait.Span(now.Sub(time.Unix(r.StartedAt, 0))))
		if r.StartLate {
			text, colour = "start not picked up", sidebar.Red
		}
		if r.Check != nil && r.Check.Kind != sidebar.CheckBossClosed {
			// Its status hides a moved PR, as a worker's does
			text = with(text, "⚠ "+r.Check.Short(now))
		}
		return text, colour
	case r.Until != 0 && r.Age(now) == "":
		return "till " + wait.Clock(time.Unix(r.Until, 0), now), sidebar.Muted
	}
	return r.Age(now), r.Tone().Colour()
}

// needLines are row r's lines, w columns: its mark, its label padded to labelW, its text (wrapped when selected,
// else cut), its status at the right edge (right), its options (optionLines), then a selected row's action words,
// on the selection band. Waiting on others' fold row is its title, count and whom it waits on.
func (m Model) needLines(r needRow, labelW, w int, selected bool) (lines []string, spans [][]optSpan) {
	if r.fold != nil {
		arrow := "▸"
		if m.waitOpen {
			arrow = "▾"
		}
		title, colour := groupTitle(r.Group())
		line := " " + rule + arrow + reset + " " + colour + bold + title + reset + "  " + sidebar.Muted + strconv.Itoa(len(r.fold)) + reset
		if !m.waitOpen {
			line += "   " + sidebar.Muted + waitingOn(r.fold) + reset
		}
		return []string{band(padRow(line, w), selected)}, make([][]optSpan, 1)
	}
	label := ansi.Truncate(r.label, labelW, "…")
	pre := " " + r.mark() + " " + sidebar.Muted + label + reset + strings.Repeat(" ", labelW-ansi.StringWidth(label)+2)
	in := 3 + labelW + 2
	room := max(1, w-in-1)
	now := m.clk.Now()
	right, colour := r.right(now, w >= namesFrom)
	if right != "" && room-ansi.StringWidth(right)-2 < minTextBesideName {
		// Narrow: the status word keeps the edge, whom it names goes
		right, colour = r.right(now, false)
	}
	if right != "" && room-ansi.StringWidth(right)-2 >= min(sidebar.MinTextAfterAge, room/2) {
		room -= ansi.StringWidth(right) + 2
	} else {
		right = ""
	}
	text := strings.Join(strings.Fields(r.Text), " ")
	if selected {
		parts := strings.Split(ansi.Wrap(text, room, ""), "\n")
		lines = []string{pre + parts[0]}
		for _, p := range parts[1:] {
			lines = append(lines, strings.Repeat(" ", in)+p)
		}
	} else {
		lines = []string{pre + ansi.Truncate(text, room, "…")}
	}
	spans = make([][]optSpan, len(lines))
	if right != "" {
		// Right-aligned on the first line, past the text's room
		lines[0] += strings.Repeat(" ", max(2, in+room+2-ansi.StringWidth(lines[0]))) + colour + right + reset
	}
	if selected {
		for _, c := range r.Context {
			bullet := "- "
			for _, p := range strings.Split(ansi.Wrap(c, max(1, room-2), ""), "\n") {
				lines, spans = append(lines, strings.Repeat(" ", in)+sidebar.Muted+bullet+p+reset), append(spans, nil)
				bullet = "  "
			}
		}
	}
	if len(r.Options) > 0 {
		lines, spans = m.optionLines(r, w, selected, lines, spans)
	}
	if selected && m.needMode {
		line, sp := m.actionLine(r, in, w)
		if line != "" {
			lines, spans = append(lines, line), append(spans, sp)
		}
	}
	for i, l := range lines {
		lines[i] = band(padRow(l, w), selected)
	}
	return lines, spans
}

// band puts a selected row's line on the selection band.
func band(l string, selected bool) string {
	if !selected {
		return l
	}
	return sidebar.SelectedBG + strings.ReplaceAll(l, reset, reset+sidebar.SelectedBG) + reset
}

// optionLines adds r's options under its lines: numbered when 1-9 answer them, ★ on the recommended one, m to reply to
// a question; a list item's by label, a selected one's one per line with its detail muted beside it when any has one;
// a selected row's one per line, wrapped, when they don't fit one.
func (m Model) optionLines(r needRow, w int, selected bool, lines []string, spans [][]optSpan) ([]string, [][]optSpan) {
	answers := m.answerable(r)
	// A list item's options are "label | detail"; a session's dialog or question's are its own
	listItem := r.Boss != ""
	opts := make([]string, len(r.Options))
	details := make([]string, len(r.Options))
	for i, o := range r.Options {
		num := sidebar.Muted + "·" + reset
		if answers {
			num = sidebar.Green + strconv.Itoa(i+1) + reset
		}
		label, rec := todo.Label(o)
		if listItem {
			label, details[i], rec = sidebar.SplitOption(o)
		}
		if rec {
			label = sidebar.Gold + "★ " + reset + label
		}
		opts[i] = num + " " + label
	}
	described := slices.ContainsFunc(details, func(d string) bool { return d != "" })
	if selected && described {
		// The details in one column, past the longest label
		col := 0
		for _, o := range opts {
			col = max(col, ansi.StringWidth(o))
		}
		for i, d := range details {
			if d != "" {
				opts[i] += strings.Repeat(" ", col-ansi.StringWidth(opts[i])+3) + sidebar.Muted + d + reset
			}
		}
	}
	if r.Kind == sidebar.NeedAsk && answers {
		opts = append(opts, sidebar.Green+"m"+reset+rule+" reply"+reset)
	}
	first := len(spans)
	line := "     " + strings.Join(opts, "   ")
	if selected && described {
		// One line each, cut to the pane, so a long detail can't push the next options off it
		for i, o := range opts {
			o = ansi.Truncate("     "+o, w, "…")
			lines = append(lines, o)
			var sp []optSpan
			if i < min(len(r.Options), 9) {
				sp = []optSpan{{x0: 5, x1: ansi.StringWidth(o), n: i}}
			}
			spans = append(spans, sp)
		}
	} else if selected && ansi.StringWidth(line) >= w {
		for i, o := range opts {
			indent := "     "
			for _, p := range strings.Split(ansi.Wrap(o, max(1, w-8), ""), "\n") {
				lines = append(lines, indent+p)
				var sp []optSpan
				if i < min(len(r.Options), 9) {
					sp = []optSpan{{x0: 5, x1: w, n: i}}
				}
				spans = append(spans, sp)
				indent = "       "
			}
		}
	} else {
		lines = append(lines, line)
		var row []optSpan
		x := 5
		for i, o := range opts[:min(len(r.Options), 9)] {
			ow := ansi.StringWidth(o)
			// The column cut off by padRow shows an ellipsis, not the option
			if end := x + ow; end <= w {
				row = append(row, optSpan{x0: x, x1: end, n: i})
			} else if x < w-1 {
				row = append(row, optSpan{x0: x, x1: w - 1, n: i})
			}
			x += ow + 3
		}
		spans = append(spans, row)
	}
	if !answers {
		clear(spans[first:])
	}
	return lines, spans
}

// action is a word on an expanded row that does what its key does.
type action struct{ word, key, hint string }

// actions are what the selected row offers: Start (on a starting row too, so the words stay put once started) and Tick
// the user's item, Reply to a question, Go to the session or the list's boss; a closed boss's row only Tick, and Start
// while the manager runs, then how to reopen it.
func (m Model) actions(r needRow) (out []action, note string) {
	tick := action{"Tick", "d", "d tick"}
	start := action{"Start", "s", "s start"}
	if r.Closed {
		if r.Kind == sidebar.NeedPlain {
			if r.Status != sidebar.StatusCheck && m.managerRuns() {
				out = append(out, start)
			}
			out = append(out, tick)
		}
		return out, r.ClosedHint()
	}
	switch r.Kind {
	case sidebar.NeedPlain:
		if r.Status != sidebar.StatusCheck {
			out = append(out, start)
		}
		out = append(out, tick)
	case sidebar.NeedDialog, sidebar.NeedAsk:
		if m.answerable(r) {
			out = append(out, action{"Reply", "m", "m reply"})
		}
		return append(out, action{"Go to session", "g", "g go"}), ""
	}
	if r.Session == m.snap.name {
		// The board's own list: its boss is here
		return out, ""
	}
	return append(out, action{"Go to boss", "g", "g go"}), ""
}

// actionLine is the selected row's action words under its text, from column in: the first bold, the rest muted, each
// clickable, then their keys, or how to reopen a closed boss.
func (m Model) actionLine(r needRow, in, w int) (string, []optSpan) {
	acts, note := m.actions(r)
	if len(acts) == 0 && note == "" {
		return "", nil
	}
	line, x := strings.Repeat(" ", in), in
	var spans []optSpan
	var keys []string
	for i, a := range acts {
		colour := sidebar.Muted
		if i == 0 {
			colour = bold
		}
		if i > 0 {
			line, x = line+"   ", x+3
		}
		line += colour + a.word + reset
		spans = append(spans, optSpan{x0: x, x1: min(w, x+ansi.StringWidth(a.word)), key: a.key})
		x += ansi.StringWidth(a.word)
		keys = append(keys, a.hint)
	}
	keys = append(keys, "esc back")
	tail := strings.Join(keys, " · ")
	if note != "" {
		tail = note
	}
	return line + "      " + rule + tail + reset, spans
}

// waitingOn is whom the Waiting on others rows wait on, in their order, "×n" for more than one: "Carl · reviewers ×3".
func waitingOn(rows []sidebar.NeedRow) string {
	var names []string
	n := map[string]int{}
	for _, r := range rows {
		who := r.StatusOn
		if n[who] == 0 {
			names = append(names, who)
		}
		n[who]++
	}
	for i, who := range names {
		if n[who] > 1 {
			names[i] += " ×" + strconv.Itoa(n[who])
		}
	}
	return strings.Join(names, " · ")
}
