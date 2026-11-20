// Package kanban is the to do board as columns by status group (sidebar.Groups), the item view (itemview) drawn
// over it. It draws and takes keys only; its host (dev todo board, in a full-window tmux popup) loads the lists and
// runs the actions.
package kanban

import (
	"cmp"
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
	// cardH is a card's lines: project #id and status, the title on 2, whom it's on
	cardH = 4
	// refresh is how often the board reads the lists again
	refresh = 5 * time.Second
)

// List is a boss's list as dev todo --all --json gives it, statuses set; Error for one not read.
type List struct {
	Boss   string
	Closed bool
	Error  string
	Items  []todo.Shown
}

// Loaded is a read of the lists: them or why not, its warning, and whether the manager runs (takes closed bosses' starts).
type Loaded struct {
	Lists       []List
	Warn        string
	Err         error
	ManagerRuns bool
}

// column is a board column: its title and the groups (sidebar.Groups indexes) it holds.
type column struct {
	title  string
	groups []int
}

var (
	// wide gives every group its own column
	wide = func() []column {
		out := make([]column, len(sidebar.Groups))
		for i, g := range sidebar.Groups {
			out[i] = column{g.Title, []int{i}}
		}
		return out
	}()
	// merged is four columns, what waits on the user first
	merged = []column{
		{"For you", []int{0, 1}},
		{"Starting · In progress", []int{2, 3}},
		{"Waiting on others", []int{4}},
		{"Check · Boss closed", []int{5, 6}},
	}
)

// layout is the board's columns at inner text width w and how many show at once: every group its own column where
// all fit at cardW, else the four merged ones, as many as fit side by side, h/l scrolling the rest in.
func layout(w int) (cols []column, shown int) {
	if len(wide)*cardW+(len(wide)-1)*gap <= w {
		return wide, len(wide)
	}
	return merged, max(1, min(len(merged), (w+gap)/(cardW+gap)))
}

// card is an open item on the board.
type card struct {
	boss   string
	closed bool
	it     todo.Shown
}

func (c card) key() string { return c.boss + "\x00" + strconv.Itoa(c.it.ID) }

// group is c's sidebar.Groups index; no or an unknown status is In progress, as the boards have it.
func (c card) group() int {
	if g := sidebar.GroupOf(c.it.Status); g >= 0 {
		return g
	}
	return sidebar.GroupOf(sidebar.StatusWorking)
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
		read := m.read()
		return m, tea.Batch(read, tick())
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.Done = true
			return m, tea.Quit
		}
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

// updateItem passes msg to the item view; once it's done the board reads the lists again (an action changed them)
// and says what went, or closes after a switch to a session.
func (m Model) updateItem(msg tea.Msg) (tea.Model, tea.Cmd) {
	v, cmd := m.item.Update(msg)
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

// cards are the open items the filter shows, in list order.
func (m Model) cards() []card {
	var out []card
	for _, l := range m.lists {
		if m.project != "" && project(l.Boss) != m.project {
			continue
		}
		for _, it := range l.Items {
			if !it.Done {
				out = append(out, card{boss: l.Boss, closed: l.Closed, it: it})
			}
		}
	}
	return out
}

// columns are the cards by layout column, by status within one as the boards sort them (sidebar.Statuses).
func (m Model) columns() [][]card {
	cols, _ := layout(m.inner())
	out := make([][]card, len(cols))
	at := make([]int, len(sidebar.Groups))
	for i, c := range cols {
		for _, g := range c.groups {
			at[g] = i
		}
	}
	for _, c := range m.cards() {
		i := at[c.group()]
		out[i] = append(out[i], c)
	}
	for _, cs := range out {
		slices.SortStableFunc(cs, func(a, b card) int { return cmp.Compare(rank(a), rank(b)) })
	}
	return out
}

func rank(c card) int {
	if i := slices.Index(sidebar.Statuses, c.it.Status); i >= 0 {
		return i
	}
	return slices.Index(sidebar.Statuses, sidebar.StatusWorking)
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
	_, shown := layout(m.inner())
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

// project is a list's project as the boards label it: its boss's name less "boss-".
func project(boss string) string { return strings.TrimPrefix(boss, "boss-") }

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
	bw := min(itemview.Width, m.w)
	box := m.item.View(bw, m.h, now)
	if box == "" {
		return board
	}
	rows := strings.Split(box, "\n")
	x, y := (m.w-bw)/2, max(0, (m.h-len(rows))/2)
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

// board is the board's lines: its border, the filter line, the columns, the message and the keys.
func (m Model) board(now time.Time) []string {
	w, in := m.w, m.inner()
	if w < 4 {
		return nil
	}
	cols, shown := layout(in)
	cards := m.columns()
	// The columns share the width, the first ones a column more each where it doesn't divide
	cw, extra := (in-gap*(shown-1))/shown, (in-gap*(shown-1))%shown
	var body []string
	body = append(body, m.filterLine(len(cols), shown, in), "")
	var head [][]string
	var widths []int
	for i := m.off; i < min(len(cols), m.off+shown); i++ {
		colW := cw
		if i-m.off < extra {
			colW++
		}
		head, widths = append(head, m.column(cols[i], cards[i], i == m.col, colW, now)), append(widths, colW)
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
	out := []string{m.topLine(cards, w)}
	for _, l := range body {
		out = append(out, sidebar.Faint+"│"+reset+" "+padTo(ansi.Truncate(l, in, "…"), in)+" "+sidebar.Faint+"│"+reset)
	}
	return append(out, sidebar.Faint+"╰"+strings.Repeat("─", w-2)+"╯"+reset)
}

// topLine is the top border: whose board at the left, the counts at the right.
func (m Model) topLine(cards [][]card, w int) string {
	var decide, yours, open int
	for _, cs := range cards {
		for _, c := range cs {
			open++
			switch c.it.Status {
			case sidebar.StatusDecide:
				decide++
			case sidebar.StatusYours:
				yours++
			}
		}
	}
	title := " TO DO board · " + m.whose + " "
	counts := " " + strconv.Itoa(decide) + " to decide · " + strconv.Itoa(yours) + " yours · " + strconv.Itoa(open) + " open "
	if ansi.StringWidth(title)+ansi.StringWidth(counts)+4 > w {
		counts = ""
	}
	title = ansi.Truncate(title, max(0, w-4-ansi.StringWidth(counts)), "…")
	fill := max(0, w-4-ansi.StringWidth(title)-ansi.StringWidth(counts))
	colour := sidebar.Muted
	if decide > 0 {
		colour = sidebar.Red
	}
	return sidebar.Faint + "╭─" + reset + bold + title + reset + sidebar.Faint + strings.Repeat("─", fill) + reset +
		colour + counts + reset + sidebar.Faint + "─╮" + reset
}

// filterLine is the projects, the one shown bold, and which columns show when not all fit.
func (m Model) filterLine(cols, shown, in int) string {
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
		name := cmp.Or(p, "all")
		if p == m.project {
			ps = append(ps, bold+name+reset)
		} else {
			ps = append(ps, sidebar.Muted+name+reset)
		}
	}
	l := sidebar.Muted + "project " + reset + strings.Join(ps, "  ")
	if ansi.StringWidth(l) > room {
		// Too many to list: the one shown, and where it is among them
		n := strconv.Itoa(slices.Index(projects, m.project)+1) + " of " + strconv.Itoa(len(projects))
		l = sidebar.Muted + "project " + reset + bold + cmp.Or(m.project, "all") + reset + sidebar.Muted + " (" + n + ", p next)" + reset
	}
	l = ansi.Truncate(l, max(0, room), "…")
	if more == "" {
		return l
	}
	return padTo(l, in-ansi.StringWidth(more)) + more
}

// column is a column's lines: its head (title and count, red where it holds what waits on the user; ▸ while
// selected), its rule, then its cards from top, "↑ n more" and "+n more" where some don't show.
func (m Model) column(c column, cs []card, sel bool, cw int, now time.Time) []string {
	hot := slices.Contains(c.groups, sidebar.GroupOf(sidebar.StatusDecide)) || slices.Contains(c.groups, sidebar.GroupOf(sidebar.StatusYours))
	colour, rule := "", sidebar.Faint
	if hot {
		colour, rule = sidebar.Red, sidebar.Red
	}
	title := c.title
	if sel {
		title = "▸ " + title
	}
	n := strconv.Itoa(len(cs))
	title = ansi.Truncate(title, max(0, cw-len(n)-1), "…")
	out := []string{bold + colour + title + reset + strings.Repeat(" ", max(0, cw-ansi.StringWidth(title)-len(n))) + sidebar.Muted + n + reset,
		rule + strings.Repeat("─", cw) + reset}
	if len(cs) == 0 {
		return append(out, sidebar.Muted+"None"+reset)
	}
	top, fit := 0, m.fits(len(cs))
	if sel {
		top = m.top
	}
	if top > 0 {
		out = append(out, sidebar.Muted+"↑ "+strconv.Itoa(top)+" more"+reset)
	}
	for i := top; i < min(len(cs), top+fit); i++ {
		if i > top {
			out = append(out, "")
		}
		out = append(out, cardLines(cs[i], sel && i == m.card, cw, now)...)
	}
	if rest := len(cs) - top - fit; rest > 0 {
		out = append(out, sidebar.Muted+"+"+strconv.Itoa(rest)+" more"+reset)
	}
	return out
}

// cardLines are a card cw wide: project #id (? before an ask, ☐ before the user's own, as the boards mark them) and
// its status word at the right in the boards' colours; the title on 2 lines, bold for an ask; whom it's on, its
// options and link. The selected card is on the selection band.
func cardLines(c card, sel bool, cw int, now time.Time) []string {
	it := c.it
	mark := ""
	switch it.Status {
	case sidebar.StatusDecide:
		mark = sidebar.Gold + bold + "?" + reset + " "
	case sidebar.StatusYours:
		mark = "☐ "
	}
	// A long project is cut, the number kept
	id := " #" + strconv.Itoa(it.ID)
	name := ansi.Truncate(project(c.boss), max(1, cw-ansi.StringWidth(mark+id)), "…")
	num := ansi.Truncate(mark+sidebar.Muted+name+id+reset, cw, "")
	word, colour := status(it, now)
	word = ansi.Truncate(word, max(0, cw-ansi.StringWidth(num)-1), "…")
	lines := []string{num + strings.Repeat(" ", max(0, cw-ansi.StringWidth(num)-ansi.StringWidth(word))) + colour + word + reset}
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
	foot := []string{owner(c)}
	if len(it.Options) > 0 {
		foot = append(foot, strconv.Itoa(len(it.Options))+" options")
	}
	if it.Link != "" {
		foot = append(foot, link(it.Link))
	}
	lines = append(lines, sidebar.Muted+ansi.Truncate(strings.Join(foot, " · "), cw, "…")+reset)
	if sel {
		for i, l := range lines {
			lines[i] = sidebar.SelectedBG + strings.ReplaceAll(padTo(l, cw), reset, reset+sidebar.SelectedBG) + reset
		}
	}
	return lines
}

// status is the card's status word and colour: a start's "sent to boss-x · 3m" (red "start not picked up" once
// late), a check's "#18 merged 3d", else the status.
func status(it todo.Shown, now time.Time) (string, string) {
	colour := sidebar.StatusColours[it.Status]
	switch {
	case it.StartLate:
		return "start not picked up", sidebar.Red
	case it.Status == sidebar.StatusStarting && it.Started != nil:
		return "sent to " + it.Started.To + " · " + sidebar.AgeSpan(now.Sub(time.Unix(it.Started.At, 0))), colour
	case it.Status == sidebar.StatusCheck && it.Check != nil:
		return it.Check.Short(now), colour
	}
	return cmp.Or(it.Status, "open"), colour
}

// owner is whom the card is on: "you" for the user's own.
func owner(c card) string {
	if todo.Own(c.it.On, c.it.Session, c.boss) {
		return "you"
	}
	return cmp.Or(c.it.StatusOn, c.it.On)
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
