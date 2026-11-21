package herd

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// needRow is a row of the board's TO DO (sidebar.NeedRows); a dialog's or question's Text and Options are
// rewritten as the tile it answers through shows them.
type needRow struct {
	sidebar.NeedRow
	label string
	tile  int               // the tile it answers through; -1 for a list's row, or a session not on the board
	sig   string            // the row as shown, its dialog's too: a key acts only on the row its selection was made on
	fold  []sidebar.NeedRow // Waiting on others' fold row: the rows it stands for, shown under it only while open (waitOpen)
}

// opens reports space opens the row in its popup (dev todo show): a list's read item.
func (r needRow) opens() bool {
	return r.fold == nil && r.Boss != "" && r.Kind != sidebar.NeedUnread
}

// foldSig is the fold row's sig.
const foldSig = "fold\x00waiting"

// needLists are the lists TO DO and the roll-ups read: this boss's own, then its bosses' (a read that failed
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
		// A manager reads its closed bosses' lists too, with no boss of its own open: its own row says so
		if len(s.bosses) == 0 {
			out = append(out, sidebar.NeedList{Boss: s.name, Error: s.asksErr.Error()})
		}
	}
	for _, l := range s.asks {
		nl := sidebar.NeedList{Boss: l.Boss, Items: needItems(l.Items), Updated: l.Updated, Stale: l.Stale, Closed: l.Closed}
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
		out[i] = sidebar.NeedItem{ID: it.ID, Text: it.Text, On: it.On, Done: it.Done, Options: it.Options, Context: it.Context, Added: it.Added,
			Check: it.Check, Until: it.Until, Status: it.Status, StatusOn: it.StatusOn, StartLate: it.StartLate, Quiet: it.Quiet}
		if it.Started != nil {
			out[i].Started = &struct{ At int64 }{it.Started.At}
		}
	}
	return out
}

// needRows are TO DO's rows as the block lists them (listRows): Waiting on others folded into one row standing for
// them, which they follow only while open (waitOpen).
func (m Model) needRows() []needRow {
	rows := m.listRows()
	at := slices.IndexFunc(rows, func(r needRow) bool { return r.Status == sidebar.StatusWaiting })
	if at < 0 {
		return rows
	}
	fold := needRow{tile: -1, sig: foldSig}
	fold.Kind, fold.Status = sidebar.NeedOn, sidebar.StatusWaiting
	n := 0
	for _, r := range rows[at:] {
		if r.Status != sidebar.StatusWaiting {
			break
		}
		fold.fold = append(fold.fold, r.NeedRow)
		n++
	}
	out := append(slices.Clone(rows[:at]), fold)
	if m.waitOpen {
		out = append(out, rows[at:at+n]...)
	}
	return append(out, rows[at+n:]...)
}

// listRows are this board's TO DO rows: what waits on the user under it and the items its lists handed out,
// labelled "project › who" on a manager's board ("project" for a boss, "manager" for its own list), "who" on a boss's
// ("boss" for its own list); a dialog or question on the board says what its card does, with its options.
func (m Model) listRows() []needRow {
	s := m.snap
	manager := slices.ContainsFunc(s.all, func(a sidebar.Session) bool { return a.Name == s.name && a.Root == s.name })
	var out []needRow
	for _, r := range sidebar.NeedRows(s.all, s.needLists(), s.name) {
		n := needRow{NeedRow: r, tile: -1, label: r.Who}
		switch {
		case r.Boss == s.name && manager:
			n.label = s.name
		case r.Boss == s.name:
			n.label = r.Who
		// A list's row is a boss's, closed ones' too
		case manager && (r.Boss != "" || slices.ContainsFunc(s.all, func(a sidebar.Session) bool { return a.Name == r.Session && a.Boss })):
			n.label = r.Project
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
			strings.Join(n.Options, "\x01"), strings.Join(r.Context, "\x01"), dialog}, "\x00")
		out = append(out, n)
	}
	return out
}

// pickOptions are the labels of p's options as its card's chips say them; a question's free-text and chat entries
// are the dialog's own, not options it was given.
func pickOptions(p *pick) []string {
	var out []string
	for i, o := range p.d.opts {
		if p.question() && (i == p.other || i == len(p.d.opts)-1 && o.label == "Chat about this") {
			continue
		}
		out = append(out, optionLabel(p, o))
	}
	return out
}

// answerable reports a row whose options 1-9 send: a list ask, a dialog on screen, a question on the board.
func (m Model) answerable(r needRow) bool {
	if r.Closed {
		return false
	}
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

// tooShort reports a pane known to be too short for TO DO; before the first size message the height is unknown.
func (m Model) tooShort() bool { return m.height > 0 && !m.blockShown() }

// enterNeeds focuses TO DO on its first row.
func (m Model) enterNeeds() Model {
	rows := m.needRows()
	switch {
	case len(rows) == 0:
		m.msg = info("Nothing to do")
		return m
	case m.tooShort():
		m.msg = info("Pane too short for TO DO")
		return m
	}
	m.needMode, m.needIdx, m.needKey, m.needDone, m.todoFolded = true, 0, rows[0].sig, "", false
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
	if m.tooShort() {
		m.needMode, m.msg = false, info("Pane too short for TO DO: keys act on the cards")
		return
	}
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

// handleNeedKey acts on TO DO's selected row: j/k/↓/↑ move (wrapping), 1-9 answer it, m (i) type a reply, d tick the
// user's item, s start it (dev todo start), space open a list item in its popup (dev todo show), b the board of this
// board's lists (dev todo board), g/⏎ go to its session
// (on Waiting on others' fold row open or fold it), esc or n back to the cards, t fold the block back to the cards,
// ctrl+c quit. An answer key on a row that changed since it was chosen sends nothing.
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
	case "t":
		// Folded to its header, its rows take no keys
		m.todoFolded, m.needMode = true, false
	case "ctrl+c":
		// Before m is read: the quit marks its reload
		quit := m.reload.Quit()
		return m, quit
	case "b":
		open, client, bosses := m.openBoard, m.snap.client, m.boardBosses()
		return m, m.action(func() error { return open(client, bosses) })
	case " ":
		if !r.opens() {
			m.msg = info("space opens a list item")
			return m, nil
		}
		open, client := m.openItem, m.snap.client
		return m, m.action(func() error { return open(client, r.Boss, r.ID, r.Text) })
	case "g", "enter", "d", "s", "m", "i", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if r.fold != nil {
			if key == "g" || key == "enter" {
				m.waitOpen = !m.waitOpen
			} else {
				m.msg = info("⏎ opens or folds Waiting on others")
			}
			return m, nil
		}
		if r.Closed && key != "d" && !(key == "s" && r.Kind == sidebar.NeedPlain && m.managerRuns()) {
			m.msg = info(r.ClosedHint())
			return m, nil
		}
		if key == "g" || key == "enter" {
			return m, m.switchTo(r.Session)
		}
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
		case "s":
			return m.startNeed(r)
		case "m", "i":
			return m.replyNeed(r)
		}
		return m.answerNeed(r, int(key[0]-'1'), k)
	}
	return m, nil
}

// tickNeed ticks the user's own list item through dev todo done; like an answer, its row goes or fails.
func (m Model) tickNeed(r needRow) (tea.Model, tea.Cmd) {
	if r.Kind != sidebar.NeedPlain {
		m.msg = info("d ticks your own plain item; g/⏎ goes to " + r.Session)
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

// startNeed asks for the user's own list item to be started through dev todo start: its boss, or the manager for a
// closed boss's; a second start says when the first went.
func (m Model) startNeed(r needRow) (tea.Model, tea.Cmd) {
	if r.Kind != sidebar.NeedPlain || r.Status == sidebar.StatusCheck || r.Boss == Manager {
		m.msg = info("s starts your own plain item; g/⏎ goes to " + r.Session)
		return m, nil
	}
	if m.answering != "" {
		m.msg = info("⏳ answering " + m.answering + "…: start again once it's done")
		return m, nil
	}
	m.answering = r.Boss
	start := m.startList
	return m, listCmd(r.Boss, "Sent ", func() (string, error) { return start(r.Boss, r.ID, r.Text) })
}

// managerRuns reports the manager's session is open: it takes a closed boss's starts.
func (m Model) managerRuns() bool {
	return slices.ContainsFunc(m.snap.all, func(s sidebar.Session) bool { return s.Name == sidebar.Manager })
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

// openPicker opens the reply box on t's dialog for TO DO: bare, a draft kept for t staying kept.
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

// boardBosses are the bosses the board shows: this one and those under it; none (every boss's) on the manager's.
func (m Model) boardBosses() []string {
	if m.snap.name == sidebar.Manager {
		return nil
	}
	// Its bosses with no items yet too: the board follows their first
	return append([]string{m.snap.name}, m.snap.bosses...)
}
