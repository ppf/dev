// Package kanban is the to do board as the web page's five columns (sidebar.Columns), the item view (itemview) drawn
// over it. It draws and takes keys and clicks only; its host (dev todo board, in a full-window tmux popup) loads the lists and
// runs the actions.
package kanban

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/itemview"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

const (
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
	// cardW is a card's narrowest text: a 2-line title then holds about 48 characters
	cardW = 24
	// gap is the room between columns, a dim rule in its middle
	gap = 3
	// cardH is a card's lines: project #id and its state, the title on 2, the assignee and marks, what is done on it now
	cardH = 5
	// refresh is how often the board reads the lists again
	refresh = 2 * time.Second
	// doubleClick is how long after a click opened an item the view takes no clicks on it
	doubleClick = 500 * time.Millisecond
)

// List is a boss's list as dev todo --all --json gives it, columns set, with its finished items; Error for one not read.
type List struct {
	Boss     string
	Closed   bool
	Error    string
	Items    []todo.Shown
	Finished []todo.Finished
}

// Loaded is a read of the lists: them or why not, its warning, and whether the manager runs (takes closed bosses' starts).
type Loaded struct {
	Lists       []List
	Warn        string
	Err         error
	ManagerRuns bool
}

// fitCols is how many of the five columns (sidebar.Columns) fit side by side at inner text width w, all from 132 on;
// h/l scrolls the rest in.
func fitCols(w int) int { return max(1, min(len(sidebar.Columns), (w+gap)/(cardW+gap))) }

// card is an item on the board: an open one, or a ticked one (done: still in its list's items, or kept in its finished).
type card struct {
	boss   string
	closed bool
	it     todo.Shown
	done   bool
	doneAt int64 // when it was ticked, unix seconds; 0 for one not yet moved out of its list's items
}

// key is the card's identity across reads: ids restart, so a finished #3 is not an open #3.
func (c card) key() string {
	return c.boss + "\x00" + strconv.Itoa(c.it.ID) + "\x00" + strconv.FormatBool(c.done)
}

// column is the card's sidebar column as the model placed it (todo.Owner.place); one it didn't name is In progress,
// as an unknown status is on the boards.
func (c card) column() int {
	if c.done {
		return slices.Index(sidebar.Columns, sidebar.ColumnDone)
	}
	if i := slices.Index(sidebar.Columns, c.it.Column); i >= 0 {
		return i
	}
	return slices.Index(sidebar.Columns, sidebar.ColumnProgress)
}

// needsYou is whether the card waits on the user (a red mark): never a ticked one.
func (c card) needsYou() bool { return !c.done && c.it.NeedsYou }

// mine is whether the card is the user's: its assignee, else its owner by the list.
func (c card) mine() bool {
	if c.it.Assignee.Kind != "" {
		return c.it.Assignee.Kind == sidebar.AssigneeYou
	}
	return todo.Own(c.it.On, c.it.Session, c.boss)
}

// Model is the board. Done once the user closed it or went to a session from its item view.
type Model struct {
	whose       string
	load        func() Loaded
	acts        itemview.Actions
	managerRuns bool
	switched    *atomic.Bool // the item view's last switch went through: the board closes
	reads       int          // the reads started
	shown       int          // the read the board shows: an older one landing late is dropped

	lists   []List
	warn    string
	loadErr error
	project string // the project filter, "" for all
	col     int    // the selected column, in layout's columns
	card    int    // the selected card in it
	off     int    // the first column shown
	top     int    // the selected column's first card shown
	selKey  string // the selected card's key, kept across reads
	item    *itemview.Model
	clicked time.Time // when a click opened the item view
	msg     string
	w, h    int
	Done    bool
}

// loadedMsg is read seq's outcome; tickMsg asks for the next read.
type (
	loadedMsg struct {
		Loaded
		seq int
	}
	tickMsg struct{}
)

// New is the board of whose lists ("all bosses", or a boss), read by load; acts are the item view's.
func New(whose string, load func() Loaded, acts itemview.Actions) Model {
	switched := &atomic.Bool{}
	sw := acts.Switch
	acts.Switch = func(session string) error {
		err := sw(session)
		if err == nil {
			switched.Store(true)
		}
		return err
	}
	return Model{whose: whose, load: load, acts: acts, switched: switched}
}

func (m Model) Init() tea.Cmd { return func() tea.Msg { return tickMsg{} } }

func (m *Model) read() tea.Cmd {
	m.reads++
	load, seq := m.load, m.reads
	return func() tea.Msg { return loadedMsg{load(), seq} }
}

func tick() tea.Cmd { return tea.Tick(refresh, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.fix()
		return m, nil
	case loadedMsg:
		if msg.seq < m.shown {
			return m, nil
		}
		m.shown, m.loadErr = msg.seq, msg.Err
		// A read that failed keeps the lists last read, and the filter on them
		if msg.Err == nil {
			m.lists, m.warn, m.managerRuns = msg.Lists, msg.Warn, msg.ManagerRuns
		}
		m.fix()
		return m, nil
	case tickMsg:
		// A read still under way is not stacked on: a slow server would pile them up
		if m.reads > m.shown {
			return m, tick()
		}
		read := m.read()
		return m, tea.Batch(read, tick())
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.Done = true
			return m, tea.Quit
		}
	case tea.MouseMsg:
		return m.mouse(msg, time.Now())
	}
	if m.item != nil {
		return m.updateItem(msg)
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if k.Type != tea.KeyRunes || k.Paste || len(k.Runes) < 2 {
		return m.key(k.String())
	}
	// Keys pressed fast come as one: each runs, until one opens the item view or closes the board
	var next tea.Model = m
	var cmd tea.Cmd
	for _, r := range k.Runes {
		if b := next.(Model); b.item != nil || b.Done {
			break
		}
		next, cmd = next.(Model).key(string(r))
	}
	return next, cmd
}

// updateItem passes msg to the item view and settles it.
func (m Model) updateItem(msg tea.Msg) (tea.Model, tea.Cmd) {
	v, cmd := m.item.Update(msg)
	return m.settle(v, cmd)
}

// settle takes the item view after a key or click: kept while open; once done the board reads the lists again (an
// action changed them) and says what went, or closes after a switch to a session.
func (m Model) settle(v itemview.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if !v.Done {
		m.item = &v
		return m, cmd
	}
	m.item = nil
	if m.switched.Load() {
		m.Done = true
		return m, tea.Quit
	}
	if v.Result != "" {
		m.msg = v.Result
	}
	read := m.read()
	return m, read
}

// key runs k: h/l (←/→) move between columns, j/k (↓/↑) within one, ⏎ or space open the selected card's item view,
// p the next project, esc/q close.
func (m Model) key(k string) (tea.Model, tea.Cmd) {
	m.msg = ""
	cols := m.columns()
	switch k {
	case "esc", "q":
		m.Done = true
		return m, tea.Quit
	case "h", "left":
		m.col = max(0, m.col-1)
	case "l", "right":
		m.col = min(len(cols)-1, m.col+1)
	case "j", "down":
		m.card++
	case "k", "up":
		m.card--
	case "enter", " ":
		cs := cols[m.col]
		if len(cs) == 0 {
			m.msg = "No card in this column"
			return m, nil
		}
		c := cs[m.card]
		v := itemview.New(itemview.Item{Boss: c.boss, Closed: c.closed, Shown: c.it}, m.acts, m.managerRuns)
		m.switched.Store(false)
		m.item = &v
		return m, nil
	case "p":
		ps := m.projects()
		i := slices.Index(ps, m.project)
		m.project = ps[(i+1)%len(ps)]
	default:
		return m, nil
	}
	m.selKey = ""
	m.fix()
	return m, nil
}

// mouse runs a click or the wheel. With the item view open a left click on it is the view's (Click), one beside it
// closes it as esc does. Else a click on a card selects and opens it, on a column's head, rule or empty part selects
// the column, on "↑ n more" or "+n more" pages it, on a project shows that project; the wheel moves the selection in
// the column under it (selecting that column first), the column scrolling with it.
func (m Model) mouse(ms tea.MouseMsg, now time.Time) (tea.Model, tea.Cmd) {
	left := ms.Action == tea.MouseActionPress && ms.Button == tea.MouseButtonLeft
	wheel := ms.Button == tea.MouseButtonWheelUp || ms.Button == tea.MouseButtonWheelDown
	if m.item != nil {
		if !left {
			return m, nil
		}
		x, y, bw, rows := m.box(now)
		if ms.X < x || ms.X >= x+bw || ms.Y < y || ms.Y >= y+len(rows) {
			return m.updateItem(tea.KeyMsg{Type: tea.KeyEsc})
		}
		// A double click's second press would land on the view the first opened
		if now.Sub(m.clicked) < doubleClick {
			return m, nil
		}
		v, cmd := m.item.Click(ms.X-x, ms.Y-y, bw, m.h, now)
		return m.settle(v, cmd)
	}
	if !left && !wheel {
		return m, nil
	}
	// The board's text starts 2 in, under the top border
	x, y := ms.X-2, ms.Y-1
	if y == 0 && left {
		_, spans := m.filterLine(len(sidebar.Columns), fitCols(m.inner()), m.inner())
		for _, sp := range spans {
			if x >= sp.from && x < sp.to {
				if sp.p < 0 {
					return m.key("p")
				}
				m.msg, m.project, m.selKey = "", m.projects()[sp.p], ""
				m.fix()
				return m, nil
			}
		}
		return m, nil
	}
	r := y - 2
	if r < 0 || r >= 2+m.cardRows() {
		return m, nil
	}
	cards := m.columns()
	for _, sc := range m.shownCols() {
		if x < sc.x || x >= sc.x+sc.w {
			continue
		}
		sel := sc.i == m.col
		top := 0
		if sel {
			top = m.top
		}
		if wheel {
			card := 0
			if sel {
				card = m.card + 1
				if ms.Button == tea.MouseButtonWheelUp {
					card = m.card - 1
				}
			}
			return m.pick(sc.i, card, top), nil
		}
		_, at := m.column(sc.i, cards[sc.i], sel, sc.w, now)
		hit := atColumn
		if r < len(at) {
			hit = at[r]
		}
		fit := m.fits(len(cards[sc.i]))
		switch hit {
		case atColumn:
			card := m.card
			if !sel {
				card = 0
			}
			return m.pick(sc.i, card, top), nil
		case atUp:
			return m.pick(sc.i, top-1, max(0, top-fit)), nil
		case atMore:
			return m.pick(sc.i, top+fit, top+fit), nil
		}
		next, cmd := m.pick(sc.i, hit, top).key("enter")
		b := next.(Model)
		b.clicked = now
		return b, cmd
	}
	return m, nil
}

// pick selects card of column col, top its first card shown where it can be, as a key would.
func (m Model) pick(col, card, top int) Model {
	m.msg, m.col, m.card, m.top, m.selKey = "", col, card, top, ""
	m.fix()
	return m
}

// all are the board's cards: each list's items (open ones, and ticked ones not yet moved out) and its finished items.
func (m Model) all() []card {
	var out []card
	for _, l := range m.lists {
		for _, it := range l.Items {
			out = append(out, card{boss: l.Boss, closed: l.Closed, it: it, done: it.Done})
		}
		for _, f := range l.Finished {
			it := todo.Shown{ID: f.ID, Text: f.Text, Link: f.Link, Answer: f.Answer, On: f.On, Quiet: f.Quiet, Done: true,
				Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: f.Assignee}}
			out = append(out, card{boss: l.Boss, closed: l.Closed, it: it, done: true, doneAt: f.DoneAt})
		}
	}
	return out
}

func (m Model) cards() []card {
	if m.project == "" {
		return m.all()
	}
	return slices.DeleteFunc(m.all(), func(c card) bool { return project(c.boss) != m.project })
}

// columns are the cards by column, each in the web page's order: Done newest first; the others what needs the user
// first, then (To do) the user's, then the soonest due, then the oldest.
func (m Model) columns() [][]card {
	out := make([][]card, len(sidebar.Columns))
	for _, c := range m.cards() {
		i := c.column()
		out[i] = append(out[i], c)
	}
	done := slices.Index(sidebar.Columns, sidebar.ColumnDone)
	todoCol := slices.Index(sidebar.Columns, sidebar.ColumnTodo)
	for i, cs := range out {
		slices.SortStableFunc(cs, func(a, b card) int {
			switch {
			case i == done:
				return cmp.Compare(doneAt(b), doneAt(a))
			case a.needsYou() != b.needsYou():
				return boolRank(b.needsYou()) - boolRank(a.needsYou())
			case i == todoCol && a.mine() != b.mine():
				return boolRank(b.mine()) - boolRank(a.mine())
			}
			return urgency(a.it, b.it)
		})
	}
	return out
}

// doneAt is when a card was ticked; one still in its list's items has none yet: the newest.
func doneAt(c card) int64 {
	if c.doneAt == 0 {
		return math.MaxInt64
	}
	return c.doneAt
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// urgency orders by the soonest due first, then the oldest.
func urgency(a, b todo.Shown) int {
	if (a.Until != 0) != (b.Until != 0) {
		return boolRank(b.Until != 0) - boolRank(a.Until != 0)
	}
	return cmp.Or(cmp.Compare(a.Until, b.Until), cmp.Compare(a.Added, b.Added))
}

// projects are "" (all) then every list's project, sorted.
func (m Model) projects() []string {
	var ps []string
	for _, l := range m.lists {
		if p := project(l.Boss); !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	slices.Sort(ps)
	return append([]string{""}, ps...)
}

// fix keeps the selection on its card (selKey) across reads, resizes and the filter, else in its column and in
// range; then scrolls the columns and the selected column's cards so it shows.
func (m *Model) fix() {
	if !slices.Contains(m.projects(), m.project) {
		m.project = ""
	}
	cols := m.columns()
	if m.selKey != "" {
		for i, cs := range cols {
			if j := slices.IndexFunc(cs, func(c card) bool { return c.key() == m.selKey }); j >= 0 {
				m.col, m.card = i, j
				break
			}
		}
	}
	m.col = max(0, min(m.col, len(cols)-1))
	cs := cols[m.col]
	m.card = max(0, min(m.card, len(cs)-1))
	m.selKey = ""
	if len(cs) > 0 {
		m.selKey = cs[m.card].key()
	}
	shown := fitCols(m.inner())
	m.off = max(0, min(m.off, len(cols)-shown))
	m.off = max(min(m.off, m.col), m.col-shown+1)
	fit := m.fits(len(cs))
	m.top = max(0, min(m.top, len(cs)-fit))
	m.top = max(min(m.top, m.card), m.card-fit+1)
}

// fits is how many of n cards show in a column: all where they fit, else as many as leave a line for "↑ n more"
// above and "+n more" below.
func (m Model) fits(n int) int {
	room := m.cardRows()
	if n*(cardH+1)-1 <= room {
		return n
	}
	return max(1, (room-2+1)/(cardH+1))
}

// inner is the board's text width: the popup less its border and a space each side.
func (m Model) inner() int { return max(1, m.w-4) }

// cardRows are the rows under the column heads: the popup less its border, the filter line and a blank, the heads
// (title, rule), the message line and the keys.
func (m Model) cardRows() int { return max(0, m.h-8) }

// project is a list's project as the boards label it (desktop for a desktop app boss's), as the web page does.
func project(boss string) string { return sidebar.BoardProject(boss) }

// projectName is p on the filter line in its colour ("" is all, muted unless shown), bold when shown.
func projectName(p string, shown bool) string {
	colour := ""
	switch {
	case p != "":
		colour = sidebar.ProjectColourOf(p).ANSI()
	case !shown:
		colour = sidebar.Muted
	}
	if shown {
		colour = bold + colour
	}
	return colour + cmp.Or(p, "all") + reset
}

// View is the board w×h, the item view centred over it while open.
func (m Model) View() string {
	if m.w < 1 || m.h < 1 {
		return ""
	}
	return strings.Join(m.lines(time.Now()), "\n")
}

// lines are View's lines, each exactly w wide, exactly h of them.
func (m Model) lines(now time.Time) []string {
	board := m.board(now)
	if m.item != nil {
		board = m.overlay(board, now)
	}
	return fit(board, m.w, m.h)
}

// fit cuts or pads lines to w×h.
func fit(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := range out {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out[i] = padTo(ansi.Truncate(l, w, ""), w)
	}
	return out
}

// overlay dims the board and draws the item view centred over it.
func (m Model) overlay(board []string, now time.Time) []string {
	x, y, bw, rows := m.box(now)
	if rows == nil {
		return board
	}
	out := make([]string, max(len(board), y+len(rows)))
	for i := range out {
		plain := ""
		if i < len(board) {
			plain = padTo(ansi.Strip(board[i]), m.w)
		}
		if i < y || i >= y+len(rows) {
			out[i] = sidebar.Faint + plain + reset
			continue
		}
		left := padTo(ansi.Truncate(plain, x, ""), x)
		right := ansi.TruncateLeft(plain, x+bw, "")
		out[i] = sidebar.Faint + left + reset + padTo(rows[i-y], bw) + sidebar.Faint + right + reset
	}
	return out
}

// box is the item view's rows and where they go over the board: x, y and the width.
func (m Model) box(now time.Time) (x, y, bw int, rows []string) {
	bw = min(itemview.Width, m.w)
	box := m.item.View(bw, m.h, now)
	if box == "" {
		return 0, 0, bw, nil
	}
	rows = strings.Split(box, "\n")
	return (m.w - bw) / 2, max(0, (m.h-len(rows))/2), bw, rows
}

// board is the board's lines: its border, the filter line, the columns, the message and the keys.
func (m Model) board(now time.Time) []string {
	w, in := m.w, m.inner()
	if w < 4 {
		return nil
	}
	shown := fitCols(in)
	cards := m.columns()
	filter, _ := m.filterLine(len(sidebar.Columns), shown, in)
	body := []string{filter, ""}
	var head [][]string
	var widths []int
	for _, sc := range m.shownCols() {
		lines, _ := m.column(sc.i, cards[sc.i], sc.i == m.col, sc.w, now)
		head, widths = append(head, lines), append(widths, sc.w)
	}
	rows := 2 + m.cardRows()
	for r := range rows {
		var parts []string
		for i, c := range head {
			l := ""
			if r < len(c) {
				l = c[r]
			}
			parts = append(parts, padTo(ansi.Truncate(l, widths[i], ""), widths[i]))
		}
		body = append(body, strings.Join(parts, " "+sidebar.Faint+"│"+reset+" "))
	}
	body = append(body, m.message(in), m.keys(in))
	out := []string{m.topLine(w)}
	for _, l := range body {
		out = append(out, sidebar.Faint+"│"+reset+" "+padTo(ansi.Truncate(l, in, "…"), in)+" "+sidebar.Faint+"│"+reset)
	}
	return append(out, sidebar.Faint+"╰"+strings.Repeat("─", w-2)+"╯"+reset)
}

// shownCol is a column on screen: its index in layout's columns, its first x in the board's text and its width.
type shownCol struct{ i, x, w int }

// shownCols are the columns on screen, sharing the width, the first ones a column more each where it doesn't divide.
func (m Model) shownCols() []shownCol {
	in := m.inner()
	shown := fitCols(in)
	cw, extra := (in-gap*(shown-1))/shown, (in-gap*(shown-1))%shown
	var out []shownCol
	x := 0
	for i := m.off; i < min(len(sidebar.Columns), m.off+shown); i++ {
		colW := cw
		if i-m.off < extra {
			colW++
		}
		out = append(out, shownCol{i, x, colW})
		x += colW + gap
	}
	return out
}

// topLine is the top border: whose board at the left, "N need you · N mine" at the right, counted on every list read
// (the project filter leaves them), open cards only, test items left out, as the web page's tally.
func (m Model) topLine(w int) string {
	var need, mine int
	for _, c := range m.all() {
		if c.done || c.it.Quiet {
			continue
		}
		if c.needsYou() {
			need++
		}
		if c.mine() {
			mine++
		}
	}
	title := " TO DO board · " + m.whose + " "
	counts := " " + strconv.Itoa(need) + " need you · " + strconv.Itoa(mine) + " mine "
	if ansi.StringWidth(title)+ansi.StringWidth(counts)+4 > w {
		counts = ""
	}
	title = ansi.Truncate(title, max(0, w-4-ansi.StringWidth(counts)), "…")
	fill := max(0, w-4-ansi.StringWidth(title)-ansi.StringWidth(counts))
	colour := sidebar.Muted
	if need > 0 {
		colour = sidebar.Red
	}
	return sidebar.Faint + "╭─" + reset + bold + title + reset + sidebar.Faint + strings.Repeat("─", fill) + reset +
		colour + counts + reset + sidebar.Faint + "─╮" + reset
}

// span is where a project shows on the filter line, its x in the board's text; p is its projects index, -1 for
// "the next one" (the cut line).
type span struct{ from, to, p int }

// filterLine is the projects, the one shown bold, and which columns show when not all fit; and where each project is.
func (m Model) filterLine(cols, shown, in int) (string, []span) {
	more := ""
	if shown < cols {
		more = sidebar.Muted + "columns " + strconv.Itoa(m.off+1) + "–" + strconv.Itoa(m.off+shown) + " of " + strconv.Itoa(cols) + " · h/l" + reset
	}
	room := in
	if more != "" {
		room = in - ansi.StringWidth(more) - 2
	}
	projects := m.projects()
	var ps []string
	for _, p := range projects {
		ps = append(ps, projectName(p, p == m.project))
	}
	l := sidebar.Muted + "project " + reset + strings.Join(ps, "  ")
	var spans []span
	x := len("project ")
	for i, p := range ps {
		pw := ansi.StringWidth(p)
		spans = append(spans, span{x, x + pw, i})
		x += pw + 2
	}
	if ansi.StringWidth(l) > room {
		// Too many to list: the one shown, and where it is among them
		n := strconv.Itoa(slices.Index(projects, m.project)+1) + " of " + strconv.Itoa(len(projects))
		l = sidebar.Muted + "project " + reset + projectName(m.project, true) + sidebar.Muted + " (" + n + ", p next)" + reset
		spans = []span{{len("project "), ansi.StringWidth(l), -1}}
	}
	l = ansi.Truncate(l, max(0, room), "…")
	for i := range spans {
		spans[i].to = min(spans[i].to, room)
	}
	if more == "" {
		return l, spans
	}
	return padTo(l, in-ansi.StringWidth(more)) + more, spans
}

// What a column's line is, for a click: a card's index in the column, else these.
const (
	atColumn = -1 // the head, the rule, a gap or None
	atUp     = -2 // "↑ n more"
	atMore   = -3 // "+n more"
)

// column is column i's lines: its head (title and count, red where it holds a card that waits on the user; ▸ while
// selected), its rule, then its cards from top, "↑ n more" and "+n more" where some don't show; and what each line is.
func (m Model) column(i int, cs []card, sel bool, cw int, now time.Time) ([]string, []int) {
	colour, rule := "", sidebar.Faint
	if slices.ContainsFunc(cs, func(c card) bool { return c.needsYou() && !c.it.Quiet }) {
		colour, rule = sidebar.Red, sidebar.Red
	}
	title := sidebar.ColumnTitles[sidebar.Columns[i]]
	if sel {
		title = "▸ " + title
	}
	n := strconv.Itoa(len(cs))
	title = ansi.Truncate(title, max(0, cw-len(n)-1), "…")
	out := []string{bold + colour + title + reset + strings.Repeat(" ", max(0, cw-ansi.StringWidth(title)-len(n))) + sidebar.Muted + n + reset,
		rule + strings.Repeat("─", cw) + reset}
	at := []int{atColumn, atColumn}
	if len(cs) == 0 {
		return append(out, sidebar.Muted+"None"+reset), append(at, atColumn)
	}
	top, fit := 0, m.fits(len(cs))
	if sel {
		top = m.top
	}
	if top > 0 {
		out, at = append(out, sidebar.Muted+"↑ "+strconv.Itoa(top)+" more"+reset), append(at, atUp)
	}
	for i := top; i < min(len(cs), top+fit); i++ {
		if i > top {
			out, at = append(out, ""), append(at, atColumn)
		}
		lines := cardLines(cs[i], sel && i == m.card, cw, now)
		out, at = append(out, lines...), append(at, slices.Repeat([]int{i}, len(lines))...)
	}
	if rest := len(cs) - top - fit; rest > 0 {
		out, at = append(out, sidebar.Muted+"+"+strconv.Itoa(rest)+" more"+reset), append(at, atMore)
	}
	return out, at
}

// cardLines are a card cw wide: project #id (a red ! when it waits on the user) and at the right its state (a start's,
// a PR's, a worker's), in the marks' colours; the title on 2 lines, bold for an ask; @assignee with its chips, options
// and link; what is done on it now. The selected card is on the selection band.
func cardLines(c card, sel bool, cw int, now time.Time) []string {
	it := c.it
	mk := marksOf(c, now)
	mark := ""
	if mk.red {
		mark = sidebar.Red + bold + "!" + reset + " "
	}
	// A long project is cut, the number kept
	id := " #" + strconv.Itoa(it.ID)
	p := project(c.boss)
	name := ansi.Truncate(p, max(1, cw-ansi.StringWidth(mark+id)), "…")
	num := ansi.Truncate(mark+sidebar.ProjectColourOf(p).ANSI()+name+id+reset, cw, "")
	word := ansi.Truncate(mk.state, max(0, cw-ansi.StringWidth(num)-1), "…")
	lines := []string{num + strings.Repeat(" ", max(0, cw-ansi.StringWidth(num)-ansi.StringWidth(word))) + mk.stateColour + word + reset}
	style := ""
	if len(it.Options) > 0 {
		style = bold
	}
	title := strings.Split(ansi.Wrap(strings.Join(strings.Fields(it.Text), " "), cw, ""), "\n")
	if len(title) > 2 {
		title = []string{title[0], ansi.Truncate(title[1]+" "+title[2], cw-1, "") + "…"}
	}
	for len(title) < 2 {
		title = append(title, "")
	}
	for _, l := range title {
		lines = append(lines, style+l+reset)
	}
	who := sidebar.Muted + "@" + assignee(c) + reset
	var chips []string
	for _, ch := range mk.chips {
		chips = append(chips, ch.colour+ch.text+reset)
	}
	foot := append([]string{who}, chips...)
	if it.WaitingFor != "" {
		// The person first: a narrow card cuts the tail. An open item on a boss: who follows up, no one when it is closed
		switch {
		case c.done || it.Assignee.Kind != sidebar.AssigneeBoss:
			foot = append(chips, who)
		case c.closed:
			foot = chips
		default:
			foot = append(chips, who+" follows up")
		}
	}
	if it.Quiet {
		foot = append([]string{sidebar.Muted + "TEST" + reset}, foot...)
	}
	if len(it.Options) > 0 && !c.done {
		foot = append(foot, sidebar.Muted+strconv.Itoa(len(it.Options))+" options"+reset)
	}
	if it.Link != "" {
		foot = append(foot, sidebar.Muted+link(it.Link)+reset)
	}
	lines = append(lines, ansi.Truncate(strings.Join(foot, sidebar.Muted+" · "+reset), cw, "…"))
	doing := ""
	if mk.line != "" {
		doing = sidebar.Gold + ansi.Truncate("▸ "+strings.Join(strings.Fields(mk.line), " "), cw, "…") + reset
	}
	lines = append(lines, doing)
	if sel {
		for i, l := range lines {
			lines[i] = sidebar.SelectedBG + strings.ReplaceAll(padTo(l, cw), reset, reset+sidebar.SelectedBG) + reset
		}
	}
	return lines
}

// assignee is whose the card is: "you" for the user's, else the session's name ("boss" for a desktop app boss).
func assignee(c card) string {
	a := c.it.Assignee
	switch {
	case a.Kind == sidebar.AssigneeYou, a.Kind == "" && c.mine():
		return "you"
	case a.Name != "":
		return a.Name
	}
	return cmp.Or(c.it.On, "boss")
}

// link is a link as a card names it: "PR #n" for a pull request, else "link".
func link(url string) string {
	if _, n, ok := strings.Cut(url, "/pull/"); ok {
		if _, err := strconv.Atoi(n); err == nil {
			return "PR #" + n
		}
	}
	return "link"
}

// message is the last action's outcome, else the lists not read and what the read warned of.
func (m Model) message(in int) string {
	var parts []string
	if m.loadErr != nil {
		parts = append(parts, "❌ lists not read: "+m.loadErr.Error())
	}
	for _, l := range m.lists {
		if l.Error != "" {
			parts = append(parts, "⚠ "+l.Boss+": "+l.Error)
		}
	}
	if m.warn != "" {
		parts = append(parts, "⚠ "+strings.TrimPrefix(m.warn, "⚠ "))
	}
	colour := sidebar.Gold
	if m.msg != "" {
		parts, colour = append([]string{m.msg}, parts...), ""
	}
	if len(parts) == 0 && m.lists != nil && len(m.cards()) == 0 {
		parts, colour = []string{"No open items"}, sidebar.Muted
	}
	return colour + ansi.Truncate(strings.Join(strings.Fields(strings.Join(parts, " · ")), " "), in, "…") + reset
}

// keys is the bottom line: the board's keys.
func (m Model) keys(in int) string {
	ks := [][2]string{{"h/l", "column"}, {"j/k", "card"}, {"⏎/space", "open item"}, {"p", "project"}, {"esc", "close"}}
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = bold + k[0] + reset + sidebar.Muted + " " + k[1] + reset
	}
	return ansi.Truncate(strings.Join(parts, sidebar.Muted+" · "+reset), in, "…")
}

// padTo pads l with spaces to w columns.
func padTo(l string, w int) string {
	return l + strings.Repeat(" ", max(0, w-ansi.StringWidth(l)))
}
