package herd

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// needRow is a row of the board's NEEDS YOU (sidebar.NeedRows); a dialog's or question's Text and Options are
// rewritten as the tile it answers through shows them.
type needRow struct {
	sidebar.NeedRow
	label string
	tile  int    // the tile it answers through; -1 for a list's row, or a session not on the board
	sig   string // the row as shown, its dialog's too: a key acts only on the row its selection was made on
}

// needLists are the lists NEEDS YOU and the roll-ups read: this boss's own, then its bosses' (a read that failed
// marks each unread).
func (s snapshot) needLists() []sidebar.NeedList {
	own := sidebar.NeedList{Boss: s.name, Items: needItems(s.todo.Items), Updated: s.todo.Updated, Stale: s.todo.Stale}
	if s.todoErr != nil {
		own.Error = s.todoErr.Error()
	}
	out := []sidebar.NeedList{own}
	if s.asksErr != nil {
		for _, b := range s.bosses {
			out = append(out, sidebar.NeedList{Boss: b, Error: s.asksErr.Error()})
		}
	}
	for _, l := range s.asks {
		nl := sidebar.NeedList{Boss: l.Boss, Items: needItems(l.Items), Updated: l.Updated, Stale: l.Stale}
		if l.Err != nil {
			nl.Error = l.Err.Error()
		}
		out = append(out, nl)
	}
	return out
}

func needItems(items []todo.Shown) []sidebar.NeedItem {
	out := make([]sidebar.NeedItem, len(items))
	for i, it := range items {
		out[i] = sidebar.NeedItem{ID: it.ID, Text: it.Text, On: it.On, Done: it.Done, Options: it.Options}
	}
	return out
}

// needRows are this board's NEEDS YOU rows: what waits on the user under it, labelled "project › who" on a
// manager's board, "who" on a boss's and "to do" for its own list; a dialog or question on the board says what its
// card does, with its options.
func (m Model) needRows() []needRow {
	s := m.snap
	manager := slices.ContainsFunc(s.all, func(a sidebar.Session) bool { return a.Name == s.name && a.Root == s.name })
	var out []needRow
	for _, r := range sidebar.NeedRows(s.all, s.needLists(), s.name) {
		n := needRow{NeedRow: r, tile: -1, label: r.Who}
		switch {
		case r.Boss == s.name:
			n.label = "to do"
		case manager:
			n.label = r.Project + " › " + r.Who
		}
		dialog := ""
		if i := index(s.tiles, r.Session); r.Boss == "" && i < len(s.tiles) && s.tiles[i].sess.Name == r.Session {
			t := s.tiles[i]
			n.tile = i
			switch {
			case r.Kind == sidebar.NeedDialog && t.pick != nil:
				n.Text, n.Options, dialog = statusText(t, needsYouSection), pickOptions(t.pick), t.pick.d.key()
			case r.Kind == sidebar.NeedAsk:
				n.Text, n.Options = statusText(t, needsYouSection), t.options()
			}
		}
		n.sig = strings.Join([]string{strconv.Itoa(int(r.Kind)), r.Session, r.Boss, strconv.Itoa(r.ID), n.Text,
			strings.Join(n.Options, "\x01"), dialog}, "\x00")
		out = append(out, n)
	}
	return out
}

// pickOptions are the labels of p's options as its card's chips say them.
func pickOptions(p *pick) []string {
	out := make([]string, len(p.d.opts))
	for i, o := range p.d.opts {
		out[i] = optionLabel(p, o)
	}
	return out
}

// answerable reports a row whose options 1-9 send: a list ask, a dialog on screen, a question on the board.
func (m Model) answerable(r needRow) bool {
	switch r.Kind {
	case sidebar.NeedListAsk:
		return true
	case sidebar.NeedDialog:
		return r.tile >= 0 && m.snap.tiles[r.tile].pick != nil
	case sidebar.NeedAsk:
		return r.tile >= 0
	}
	return false
}

// enterNeeds focuses NEEDS YOU on its first row.
func (m Model) enterNeeds() Model {
	rows := m.needRows()
	if len(rows) == 0 {
		m.msg = info("Nothing needs you")
		return m
	}
	m.needMode, m.needIdx, m.needKey, m.needDone = true, 0, rows[0].sig, ""
	return m
}

// fixNeeds keeps the cursor on its row after a refresh; a row that changed or went leaves it in place with the
// key it was chosen on, so the next answer key is refused rather than land on another row; after the user's
// answer it takes the row now shown.
func (m *Model) fixNeeds() {
	if !m.needMode {
		return
	}
	rows := m.needRows()
	if len(rows) == 0 {
		m.needMode = false
		return
	}
	if i := slices.IndexFunc(rows, func(r needRow) bool { return r.sig == m.needKey }); i >= 0 {
		m.needIdx = i
		return
	}
	m.needIdx = min(m.needIdx, len(rows)-1)
	if i := slices.IndexFunc(rows, func(r needRow) bool { return r.sig == m.needDone }); i >= 0 {
		// Still shown: wait for it to go
		m.needIdx = i
		return
	}
	m.needDone = ""
	if m.needKey == "" {
		m.needKey = rows[m.needIdx].sig
	}
}

// handleNeedKey acts on NEEDS YOU's selected row: j/k/↓/↑ move (wrapping), 1-9 answer it, m (i) type a reply, d tick a
// plain item, g/⏎ go to its session, esc or n back to the cards, ctrl+c quit. An answer key on a row that changed
// since it was chosen sends nothing.
func (m Model) handleNeedKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.needRows()
	if len(rows) == 0 {
		m.needMode = false
		return m, nil
	}
	i := min(m.needIdx, len(rows)-1)
	r := rows[i]
	switch key := k.String(); key {
	case "j", "down", "k", "up":
		dir := 1
		if key == "k" || key == "up" {
			dir = -1
		}
		m.needIdx = ((i+dir)%len(rows) + len(rows)) % len(rows)
		m.needKey = rows[m.needIdx].sig
	case "esc", "n":
		m.needMode = false
	case "ctrl+c":
		return m, tea.Quit
	case "g", "enter":
		return m, m.switchTo(r.Session)
	case "d", "m", "i", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if r.sig == m.needDone {
			m.needKey, m.needDone = r.sig, ""
			m.msg = info("⏳ " + r.label + " answered: press again to answer it anew")
			return m, nil
		}
		if r.sig != m.needKey {
			m.needIdx, m.needKey = i, r.sig
			m.msg = alert("⚠ " + r.label + "'s item changed: nothing sent, check it and press again")
			return m, nil
		}
		switch key {
		case "d":
			return m.tickNeed(r)
		case "m", "i":
			return m.replyNeed(r)
		}
		return m.answerNeed(r, int(key[0]-'1'), k)
	}
	return m, nil
}

// tickNeed ticks a plain list item through dev todo done; like an answer, its row goes or fails.
func (m Model) tickNeed(r needRow) (tea.Model, tea.Cmd) {
	if r.Kind != sidebar.NeedPlain {
		m.msg = info("d ticks a plain item; g/⏎ goes to " + r.Session)
		return m, nil
	}
	if m.answering != "" {
		m.msg = info("⏳ answering " + m.answering + "…: tick again once it's done")
		return m, nil
	}
	m.answering = r.Boss
	m.needKey, m.needDone, m.needFor = "", r.sig, r.Boss
	tick := m.tickList
	return m, listCmd(r.Boss, "Ticked ", func() (string, error) { return tick(r.Boss, r.ID, r.Text) })
}

// listCmd runs a dev todo call on boss's list; its output, or done+boss when it prints none, or its failure.
func listCmd(boss, done string, run func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := run()
		if err != nil {
			return answeredMsg{session: boss, msg: alert("❌ " + boss + ": " + err.Error())}
		}
		return answeredMsg{session: boss, msg: info(cmp.Or(out, done+boss)), took: true}
	}
}

// replyNeed opens the reply box on a dialog's or question's session: its picker, or a typed reply.
func (m Model) replyNeed(r needRow) (tea.Model, tea.Cmd) {
	if r.Kind != sidebar.NeedDialog && r.Kind != sidebar.NeedAsk || !m.answerable(r) {
		m.msg = info("m replies to a dialog or question on the board; g/⏎ goes to " + r.Session)
		return m, nil
	}
	t := m.snap.tiles[r.tile]
	if r.Kind == sidebar.NeedDialog {
		m.openPicker(t)
	} else {
		m.open(t)
	}
	return m, nil
}

// openPicker opens the reply box on t's dialog for NEEDS YOU: bare, a draft kept for t staying kept.
func (m *Model) openPicker(t tile) {
	m.selected = t.sess.Name
	m.box = replyBox{session: t.sess.Name, pane: t.pane, hist: -1, block: true}
	m.syncPick()
}

// answerNeed answers r with its option n (k the key): a list ask through dev todo answer, a question as a typed
// reply, a dialog through its picker, which checks the screen still shows it before each key.
func (m Model) answerNeed(r needRow, n int, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case !m.answerable(r):
		m.msg = info("Nothing to answer here: g/⏎ goes to " + r.Session)
		return m, nil
	case n >= len(r.Options):
		m.msg = info(fmt.Sprintf("%s has %d options", r.label, len(r.Options)))
		return m, nil
	case m.answering != "" && r.Kind != sidebar.NeedAsk:
		m.msg = info("⏳ answering " + m.answering + "…: pick again once it's done")
		return m, nil
	}
	next, cmd := m.sendNeed(r, n, k)
	if cmd != nil {
		// Answered, the row goes or changes: the cursor takes the one shown next (fixNeeds)
		next.needKey, next.needDone, next.needFor = "", r.sig, cmp.Or(r.Boss, r.Session)
	}
	return next, cmd
}

// sendNeed sends r's option n (k the key) as its kind is answered; no command when nothing went out.
func (m Model) sendNeed(r needRow, n int, k tea.KeyMsg) (Model, tea.Cmd) {
	switch r.Kind {
	case sidebar.NeedListAsk:
		m.answering = r.Boss
		answer := m.answerList
		return m, listCmd(r.Boss, "Answered ", func() (string, error) { return answer(r.Boss, r.ID, r.Text, r.Options[n]) })
	case sidebar.NeedAsk:
		next, cmd := m.sendOption(m.snap.tiles[r.tile], n)
		return next.(Model), cmd
	}
	t := m.snap.tiles[r.tile]
	m.openPicker(t)
	next, cmd := m.typePick(k, t.pick)
	return next.(Model), cmd
}

// needBlock is NEEDS YOU at the top of the pane, in about half the gh rows (more for a long selected row; none under
// 6): its header, then each row (its
// mark, label and text, the selected one's text wrapped) with its options under it, scrolled to keep the selected
// row, "+N more" for the rest; at is each line's row, -1 for none.
func (m Model) needBlock(gh int) (lines []string, at []int) {
	rows := m.needRows()
	if len(rows) == 0 || gh < minBlockRows {
		return nil, nil
	}
	w := m.width
	head := " " + sidebar.Red + bold + "NEEDS YOU " + strconv.Itoa(len(rows)) + reset
	if !m.needMode {
		head += rule + " · n" + reset
	}
	lines, at = []string{padRow(head, w), padRow("", w)}, []int{-1, -1}
	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, ansi.StringWidth(r.label))
	}
	labelW = min(labelW, 36, max(4, w*2/5))
	cur := -1
	if m.needMode {
		cur = min(m.needIdx, len(rows)-1)
	}
	block := make([][]string, len(rows))
	total := 0
	for i, r := range rows {
		block[i] = m.needLines(r, labelW, w, i == cur)
		total += len(block[i])
	}
	// Room for rows: half the rows less the header, its blank and the blank after, one of them for "+N more"
	room := gh/2 - 3
	if total > room {
		room--
	}
	// The selected row fits, cut to the pane: the header, its blank, "+N more" and the blank after stay
	room = min(max(room, len(block[max(cur, 0)])), gh-4)
	if cur >= 0 && len(block[cur]) > room {
		block[cur] = block[cur][:room]
	}
	span := func(from int) int {
		used := 0
		for i := from; i <= cur; i++ {
			used += len(block[i])
		}
		return used
	}
	start := 0
	for start < cur && span(start) > room {
		start++
	}
	used, shown := 0, 0
	for i := start; i < len(rows) && used+len(block[i]) <= room; i++ {
		for _, l := range block[i] {
			lines, at = append(lines, l), append(at, i)
		}
		used, shown = used+len(block[i]), shown+1
	}
	if more := len(rows) - shown; more > 0 {
		lines, at = append(lines, padRow(sidebar.HintRow("  +"+strconv.Itoa(more)+" more", w), w)), append(at, -1)
	}
	return append(lines, padRow("", w)), append(at, -1)
}

// minBlockRows is the fewest rows above the bottom row NEEDS YOU shows in.
const minBlockRows = 6

// needLines are row r's lines, w columns: its mark (● dialog, ? question or list ask, ☐ list item, ⚠ list not read), its
// label padded to labelW, its text (wrapped when selected, else cut), then a stale list's age at the right edge, then its options numbered when 1-9 answer
// them (★ on the recommended one; m to reply to a question; a selected row's go one per line, wrapped, when they
// don't fit one); the selected row on the selection band.
func (m Model) needLines(r needRow, labelW, w int, selected bool) []string {
	mark := "☐"
	switch r.Kind {
	case sidebar.NeedDialog:
		mark = sidebar.Red + "●" + reset
	case sidebar.NeedAsk, sidebar.NeedListAsk:
		mark = sidebar.Gold + bold + "?" + reset
	case sidebar.NeedUnread:
		mark = sidebar.Gold + "⚠" + reset
	}
	label := ansi.Truncate(r.label, labelW, "…")
	pre := " " + mark + " " + sidebar.Muted + label + reset + strings.Repeat(" ", labelW-ansi.StringWidth(label)+2)
	in := 3 + labelW + 2
	room, age := max(1, w-in-1), r.Age(m.clk.Now())
	if age != "" && room-len(age)-1 >= sidebar.MinTextAfterAge {
		room -= len(age) + 1
	} else {
		age = ""
	}
	text := strings.Join(strings.Fields(r.Text), " ")
	var lines []string
	if selected {
		parts := strings.Split(ansi.Wrap(text, room, ""), "\n")
		lines = []string{pre + parts[0]}
		for _, p := range parts[1:] {
			lines = append(lines, strings.Repeat(" ", in)+p)
		}
	} else {
		lines = []string{pre + ansi.Truncate(text, room, "…")}
	}
	if age != "" {
		// Right-aligned on the first line, past the text's room
		lines[0] += strings.Repeat(" ", max(1, in+room+1-ansi.StringWidth(lines[0]))) + rule + age + reset
	}
	if len(r.Options) > 0 {
		answers := m.answerable(r)
		opts := make([]string, len(r.Options))
		for i, o := range r.Options {
			num := sidebar.Muted + "·" + reset
			if answers {
				num = sidebar.Green + strconv.Itoa(i+1) + reset
			}
			label, rec := todo.Label(o)
			if rec {
				label = sidebar.Gold + "★ " + reset + label
			}
			opts[i] = num + " " + label
		}
		if r.Kind == sidebar.NeedAsk && answers {
			opts = append(opts, sidebar.Green+"m"+reset+rule+" reply"+reset)
		}
		line := "     " + strings.Join(opts, "   ")
		if selected && ansi.StringWidth(line) >= w {
			for _, o := range opts {
				indent := "     "
				for _, p := range strings.Split(ansi.Wrap(o, max(1, w-8), ""), "\n") {
					lines = append(lines, indent+p)
					indent = "       "
				}
			}
		} else {
			lines = append(lines, line)
		}
	}
	for i, l := range lines {
		l = padRow(l, w)
		if selected {
			l = sidebar.SelectedBG + strings.ReplaceAll(l, reset, reset+sidebar.SelectedBG) + reset
		}
		lines[i] = l
	}
	return lines
}
