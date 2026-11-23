// Package itemview is one to do item in a box: its text, context, options or actions, facts and keys. It draws and
// takes keys and clicks only; its host (dev todo show, in a tmux popup) shows it and closes it once Done.
package itemview

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

// Width is the box's width where the screen has room.
const Width = 84

const (
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
	// cursorCell is a blank cell in reverse video: the cursor at the own-words field's end
	cursorCell = "\x1b[7m \x1b[27m"
)

// Item is one item of boss's list as dev todo --all --json gives it, its status set; Closed for a closed boss's; DoneAt
// when a finished one was ticked, unix seconds (0: not known).
type Item struct {
	Boss   string
	Closed bool
	DoneAt int64
	todo.Shown
}

// Actions are the paths the view's keys run, each off the update loop: dev todo answer, done, start, reopen and doing
// (their output, or why not), the browser, a switch of the client to a session.
type Actions struct {
	Answer func(boss string, id int, question, answer string) (string, error)
	Tick   func(boss string, id int, text string) (string, error)
	Start  func(boss string, id int, text string) (string, error)
	Reopen func(boss string, id int, text string) (string, error)
	Doing  func(boss string, id int, text, seen, doing string, stop bool) (string, error)
	Open   func(url string) error
	Switch func(session string) error
}

// entry is a line of the view's list: an ask's option (key 1-9) or own words (m), or an action (I'm on it, i, has a
// field too).
type entry struct {
	key, word, detail string
	rec               bool
}

// Model is the view of one item. Done once an action went through or the user closed it; Result says what went.
type Model struct {
	it          Item
	acts        Actions
	managerRuns bool // the manager takes a closed boss's starts
	entries     []entry
	sel         int
	typing      string // the key of the line whose field takes the keys (m own words, i I'm on it), "" for none
	field       []rune // its text
	busy        string // the action under way
	msg         string // the last action's failure, or why a key does nothing
	Done        bool
	Result      string
}

// doneMsg is an action's outcome.
type doneMsg struct {
	out string
	err error
}

// New is the view of it; managerRuns offers Start on a closed boss's own item, which the manager then takes.
func New(it Item, acts Actions, managerRuns bool) Model {
	m := Model{it: it, acts: acts, managerRuns: managerRuns}
	m.entries = m.list()
	return m
}

// ask, worker and plain are how the view treats an open item: the user's own ask, on a worker, or the user's own; whose
// it is as the list decides (todo.Own), so an ask handed to a worker is the worker's. A ticked item is none of them.
func (m Model) ask() bool    { return !m.it.Done && m.own() && len(m.it.Options) > 0 }
func (m Model) worker() bool { return !m.it.Done && !m.own() && m.it.Session != nil }
func (m Model) plain() bool  { return !m.it.Done && m.own() && len(m.it.Options) == 0 }
func (m Model) own() bool    { return todo.Own(m.it.On, m.it.Session, m.it.Boss) }

// canStart is what todo.Start takes, as the web page offers it: a late start, else a To do card, no ask, whoever's it
// is; on the manager's own list only the user's; a closed boss's only while the manager runs; never a desktop app
// boss's.
func (m Model) canStart() bool {
	it := m.it
	return !it.Done && len(it.Options) == 0 && !strings.HasPrefix(it.Boss, "local_") && (it.Boss != sidebar.Manager || m.own()) &&
		(!it.Closed || m.managerRuns) && (it.StartLate || it.Column == sidebar.ColumnTodo)
}
func (m Model) canTick() bool { return m.plain() }

// canDoing and canStop are what todo.SetDoing takes, as the web page offers them: I'm on it on the user's own To do card,
// no ask, not on it or started yet; stop on the user's card with a doing line; never a desktop app boss's.
func (m Model) canDoing() bool {
	it := m.it
	return m.yourCard() && len(it.Options) == 0 && it.Column == sidebar.ColumnTodo && it.Doing == nil && it.Started == nil
}
func (m Model) canStop() bool { return m.yourCard() && len(m.it.Options) == 0 && m.it.Doing != nil }

// yourCard is an open item assigned to the user, not a desktop app boss's.
func (m Model) yourCard() bool {
	return !m.it.Done && !strings.HasPrefix(m.it.Boss, "local_") && m.it.Assignee.Kind == sidebar.AssigneeYou
}

// seen is the item's doing line and start as the view read them, for dev todo doing --seen.
func (m Model) seen() string {
	b, _ := json.Marshal(todo.Seen{Doing: m.it.Doing, Started: m.it.Started})
	return string(b)
}

// canReopen is what todo.Reopen takes, as the web page offers it: the user's own ticked item, never a desktop app boss's.
func (m Model) canReopen() bool {
	return m.it.Done && m.own() && !strings.HasPrefix(m.it.Boss, "local_")
}

// list is what the view lists: an ask's options then own words; else its actions.
func (m Model) list() []entry {
	var out []entry
	if m.ask() {
		for i, o := range m.it.Options {
			c := todo.Split(o)
			out = append(out, entry{key: strconv.Itoa(i + 1), word: c.Label, detail: c.Detail, rec: c.Recommended})
		}
		if !m.it.Closed {
			out = append(out, entry{key: "m", detail: "In your own words…"})
		}
		return out
	}
	if m.canStart() {
		out = append(out, entry{key: "s", word: "Start", detail: "ask " + m.startTo() + " to start it"})
	}
	switch {
	case m.canDoing():
		out = append(out, entry{key: "i", word: "I'm on it", detail: "In progress, no worker started: say what you do now…"})
	case m.canStop():
		out = append(out, entry{key: "i", word: "Stop", detail: "off it: To do, or Review if its PR moved"})
	}
	if m.canTick() {
		out = append(out, entry{key: "d", word: "Tick", detail: "done: it leaves every board"})
	}
	if m.canReopen() {
		out = append(out, entry{key: "r", word: "Reopen", detail: "back to To do"})
	}
	if m.worker() {
		out = append(out, entry{key: "w", word: "Go to worker", detail: "switch to " + m.it.Session.Name})
	}
	if m.it.Link != "" {
		out = append(out, entry{key: "o", word: "Open link", detail: "in the browser"})
	}
	if !m.it.Closed {
		out = append(out, entry{key: "g", word: "Go to boss", detail: "switch to " + m.it.Boss})
	}
	return out
}

// assignee is whose the item is: "you", else the session's name ("boss" for a desktop app boss).
func (m Model) assignee() string {
	a := m.it.Assignee
	switch {
	case a.Kind == sidebar.AssigneeYou, a.Kind == "" && m.own():
		return "you"
	case a.Name != "":
		return a.Name
	}
	return cmp.Or(m.it.On, "boss")
}

func (m Model) startTo() string {
	if m.it.Closed {
		return sidebar.Manager
	}
	return m.it.Boss
}

// Update takes a key, or an action's outcome.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doneMsg:
		m.busy = ""
		if msg.err != nil {
			m.msg = "❌ " + strings.Join(strings.Fields(msg.err.Error()), " ")
			// I'm on it's field closes: a failure may have marked it anyway (its boss not told), which esc and the
			// board's next read show
			if m.typing == "i" {
				m.typing = ""
			}
			return m, nil
		}
		m.Done, m.Result = true, msg.out
	case tea.KeyMsg:
		if m.typing != "" {
			return m.typeKey(msg)
		}
		if msg.Type != tea.KeyRunes || msg.Paste || msg.Alt || len(msg.Runes) < 2 {
			return m.key(msg.String())
		}
		// Keys pressed fast come as one: each runs, until one starts an action, closes the view or shows a hint; after m the rest are typed
		for i, r := range msg.Runes {
			var cmd tea.Cmd
			m, cmd = m.key(string(r))
			if m.busy != "" || m.Done || m.msg != "" {
				return m, cmd
			}
			if m.typing != "" {
				return m.typeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: msg.Runes[i+1:]})
			}
		}
	}
	return m, nil
}

// key runs k: j/k/↓/↑ move (wrapping), ⏎ the selected line's key, 1-9 answer, m own words, s start, i I'm on it (its
// field) or stop, d tick, r reopen, w go to the worker, o open the link, g go to the boss, esc/q close. Keys but ctrl+c
// wait while an action is under way, so its outcome shows.
func (m Model) key(k string) (Model, tea.Cmd) {
	switch {
	case k == "ctrl+c":
		m.Done = true
		return m, nil
	case m.busy != "":
		return m, nil
	case k == "esc" || k == "q":
		m.Done = true
		return m, nil
	}
	m.msg = ""
	switch k {
	case "j", "down", "k", "up":
		if n := len(m.entries); n > 0 {
			dir := 1
			if k == "k" || k == "up" {
				dir = -1
			}
			m.sel = ((m.sel+dir)%n + n) % n
		}
		return m, nil
	case "enter":
		if len(m.entries) == 0 {
			return m, nil
		}
		k = m.entries[m.sel].key
	}
	it := m.it
	switch {
	case len(k) == 1 && k >= "1" && k <= "9" && m.ask() && !it.Closed:
		n := int(k[0] - '1')
		if n >= len(it.Options) {
			m.msg = fmt.Sprintf("#%d has %d options", it.ID, len(it.Options))
			return m, nil
		}
		return m.run("answering", func() (string, error) { return m.acts.Answer(it.Boss, it.ID, it.Text, it.Options[n]) })
	case k == "m" && m.ask() && !it.Closed, k == "i" && m.canDoing():
		if k == "i" {
			// A fresh line each time; own words wait for the next m
			m.field = nil
		}
		m.typing = k
		m.sel = slices.IndexFunc(m.entries, func(e entry) bool { return e.key == k })
		return m, nil
	case k == "i" && m.canStop():
		return m.run("stopping", func() (string, error) { return m.acts.Doing(it.Boss, it.ID, it.Text, m.seen(), "", true) })
	case k == "s" && m.canStart():
		return m.run("starting", func() (string, error) { return m.acts.Start(it.Boss, it.ID, it.Text) })
	case k == "d" && m.canTick():
		return m.run("ticking", func() (string, error) { return m.acts.Tick(it.Boss, it.ID, it.Text) })
	case k == "r" && m.canReopen():
		return m.run("reopening", func() (string, error) { return m.acts.Reopen(it.Boss, it.ID, it.Text) })
	case k == "w" && m.worker():
		return m.run("switching", func() (string, error) { return "", m.acts.Switch(it.Session.Name) })
	case k == "o" && it.Link != "":
		return m.run("opening", func() (string, error) { return "", m.acts.Open(it.Link) })
	case k == "g" && !it.Closed:
		return m.run("switching", func() (string, error) { return "", m.acts.Switch(it.Boss) })
	case it.Closed && strings.Contains("123456789mgs", k):
		m.msg = sidebar.NeedRow{Boss: it.Boss, Project: project(it.Boss)}.ClosedHint()
	case k == "o":
		m.msg = "#" + strconv.Itoa(it.ID) + " has no link"
	case len(k) == 1 && strings.Contains("sidrw", k):
		m.msg = k + " doesn't act on this item: its keys are below"
	}
	return m, nil
}

// typeKey edits the open field: ⏎ answers with own words, or marks I'm on it with what is done now (none is fine), esc
// leaves it, backspace deletes; a pasted line break is a space.
func (m Model) typeKey(k tea.KeyMsg) (Model, tea.Cmd) {
	switch {
	case k.Type == tea.KeyCtrlC:
		m.Done = true
		return m, nil
	case m.busy != "":
		return m, nil
	}
	switch k.Type {
	case tea.KeyEsc:
		m.typing, m.msg = "", ""
	case tea.KeyBackspace:
		if len(m.field) > 0 {
			m.field = m.field[:len(m.field)-1]
		}
	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.field))
		it := m.it
		if m.typing == "i" {
			return m.run("marking", func() (string, error) { return m.acts.Doing(it.Boss, it.ID, it.Text, m.seen(), text, false) })
		}
		if text == "" {
			m.msg = "Type an answer, or esc"
			return m, nil
		}
		return m.run("answering", func() (string, error) { return m.acts.Answer(it.Boss, it.ID, it.Text, text) })
	case tea.KeySpace:
		m.field = append(m.field, ' ')
	case tea.KeyRunes:
		for _, r := range k.Runes {
			if unicode.IsSpace(r) {
				r = ' '
			}
			if m.typing == "i" && len(m.field) >= todo.MaxDoing {
				m.msg = fmt.Sprintf("At most %d characters", todo.MaxDoing)
				break
			}
			m.field = append(m.field, r)
		}
	}
	return m, nil
}

// run starts an action, the view saying it's under way.
func (m Model) run(doing string, fn func() (string, error)) (Model, tea.Cmd) {
	m.busy = doing
	return m, func() tea.Msg {
		out, err := fn()
		return doneMsg{out: out, err: err}
	}
}

// project is a list's project as the boards label it: its boss's name less "boss-".
func project(boss string) string { return strings.TrimPrefix(boss, "boss-") }

// minField is the fewest columns a typed field keeps beside its line's label.
const minField = 8

// minWidth is the narrowest box View draws.
const minWidth = 20

// View is the box, w columns wide (at most Width, none under minWidth), at most h rows: when cut, the text's lines
// go first, then the facts, then the list's lines farthest from the selection; the border, title and keys stay.
func (m Model) View(w, h int, now time.Time) string {
	rows, _ := m.render(w, h, now)
	return strings.Join(rows, "\n")
}

// render is View's rows and, for each, the entry it shows (-1 for none).
func (m Model) render(w, h int, now time.Time) ([]string, []int) {
	w = min(w, Width)
	if w < minWidth {
		return nil, nil
	}
	in := w - 4
	it := m.it
	var head, tail []string
	for _, l := range strings.Split(ansi.Wrap(strings.Join(strings.Fields(it.Text), " "), in, ""), "\n") {
		head = append(head, bold+l+reset)
	}
	head = append(head, m.block(in, now)...)
	details := detailLines(it.Details, in)
	shown := len(details) // the details lines kept: the first a cut drops, their last ones first
	var context []string
	for _, c := range it.Context {
		context = append(context, sidebar.Muted+ansi.Truncate(c, in, "…")+reset)
	}
	list, sel, first := m.listLines(in), m.sel, 0
	facts := m.facts(in, now)
	if m.msg != "" || m.busy != "" {
		line := sidebar.Red + m.msg + reset
		if m.busy != "" {
			line = sidebar.Muted + "⏳ " + m.busy + "…" + reset
		}
		tail = append(tail, "", ansi.Truncate(line, in, "…"))
	}
	tail = append(tail, "", ansi.Truncate(m.keys(), in, "…"))
	lines := func() ([]string, []int) {
		head := slices.Concat(head, details[:shown])
		if shown < len(details) {
			head = append(head, ansi.Truncate(sidebar.Muted+fmt.Sprintf("… %d more lines of details: open it on the page", len(details)-shown)+reset, in, "…"))
		}
		head = append(head, context...)
		out := append(append([]string{""}, head...), "")
		out = append(out, list...)
		if len(facts) > 0 {
			out = append(append(out, ""), facts...)
		}
		out = append(out, tail...)
		at := slices.Repeat([]int{-1}, len(out))
		for i := range list {
			at[len(head)+2+i] = first + i
		}
		return out, at
	}
	room := max(1, h-2)
	// The details' cut in one step, not a line a pass: a long draft has thousands
	if all, _ := lines(); len(all) > room {
		shown = max(0, shown-(len(all)-room))
	}
cut:
	for all, _ := lines(); len(all) > room; all, _ = lines() {
		switch {
		case shown > 0:
			shown--
		case len(context) > 0:
			context = context[:len(context)-1]
		case len(head) > 1:
			head = head[:len(head)-1]
		case len(facts) > 0:
			facts = facts[:len(facts)-1]
		case len(list) > 1 && sel < len(list)-1:
			list = list[:len(list)-1]
		case len(list) > 1:
			list, sel, first = list[1:], sel-1, first+1
		case len(head) > 0:
			head = nil
		default:
			break cut
		}
	}
	// Too short even so: the top lines go, the keys stay
	all, at := lines()
	cut := max(0, len(all)-room)
	out := []string{m.top(w, now)}
	for _, l := range all[cut:] {
		out = append(out, sidebar.Faint+"│"+reset+" "+padTo(l, in)+" "+sidebar.Faint+"│"+reset)
	}
	out = append(out, sidebar.Faint+"╰"+strings.Repeat("─", w-2)+"╯"+reset)
	return out, append(append([]int{-1}, at[cut:]...), -1)
}

// block is the part under the title: the session on it, or what was done on a finished one; none for the others.
func (m Model) block(in int, now time.Time) []string {
	it := m.it
	rule := func(left, right string) string {
		// A narrow box cuts the right side first, then the left: the rule stays one row
		right = ansi.Truncate(right, max(0, in-7-min(ansi.StringWidth(left), 12)), "…")
		left = ansi.Truncate(left, max(1, in-7-ansi.StringWidth(right)), "…")
		fill := max(1, in-ansi.StringWidth(left)-ansi.StringWidth(right)-6)
		return sidebar.Faint + "─ " + reset + bold + left + reset + " " + sidebar.Faint + strings.Repeat("─", fill) + reset + " " + right + sidebar.Faint + " ─" + reset
	}
	row := func(k, v string) []string {
		var out []string
		for i, l := range strings.Split(ansi.Wrap(v, in-8, ""), "\n") {
			if i == 3 {
				break
			}
			label := ""
			if i == 0 {
				label = k
			}
			out = append(out, sidebar.Muted+fmt.Sprintf("%-8s", label)+reset+l)
		}
		return out
	}
	end := sidebar.Faint + strings.Repeat("─", in) + reset
	switch {
	case it.Done && it.Result != "":
		out := []string{rule("what was done", sidebar.Green+ticked(it, now)+reset)}
		out = append(out, strings.Split(ansi.Wrap(it.Result, in, ""), "\n")...)
		return append(out, end)
	case it.Done:
		return nil
	case todo.OnBoss(it.On, it.Session) && it.Doing != nil:
		since := "since " + clock(time.Unix(it.Doing.At, 0), now)
		return append([]string{rule(m.assignee(), sidebar.Green+"doing"+reset+sidebar.Muted+" · "+since+reset)}, append(row("now", cmp.Or(it.Doing.Text, "on it")), end)...)
	case it.Parked:
		right := sidebar.Muted + sidebar.StatusParked + reset
		if it.Added != 0 {
			right += sidebar.Muted + " · added " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + " ago" + reset
		}
		return append([]string{rule(m.assignee(), right)}, append(row("now", "no one on it: "+sidebar.ParkedLine(m.assignee())), end)...)
	case !m.worker(), it.Status == "" && it.Now == "" && it.Note == "":
		return nil
	}
	word := cmp.Or(liveWords[it.Status], sidebar.StatusWord(it.Status), "no status")
	right := sidebar.StatusColours[it.Status] + word + reset
	if it.Added != 0 {
		right += sidebar.Muted + " · on it " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + reset
	}
	out := []string{rule(it.Session.Name, right)}
	if it.Now != "" {
		out = append(out, row("now", it.Now)...)
	}
	if it.Note != "" {
		out = append(out, row("last", it.Note)...)
	}
	pr := ""
	if n := it.PR(); n != 0 {
		pr = "#" + strconv.Itoa(n)
	}
	if it.Check != nil && it.Check.Kind != sidebar.CheckBossClosed {
		pr = strings.TrimSpace(pr + " " + it.Check.Tone().Colour() + it.Check.Kind + reset)
	}
	if pr != "" {
		out = append(out, row("pr", pr)...)
	}
	return append(out, end)
}

// liveWords are the statuses a session's block says otherwise
var liveWords = map[string]string{sidebar.StatusBlocked: "needs you", sidebar.StatusGone: "session gone"}

// ticked is when a finished item was ticked and by whom: "12:41 · by boss-vitay"; "" when neither is known.
func ticked(it Item, now time.Time) string {
	var parts []string
	if it.DoneAt != 0 {
		parts = append(parts, clock(time.Unix(it.DoneAt, 0), now))
	}
	switch it.DoneBy {
	case "":
	case todo.DoneByUntil:
		parts = append(parts, "by itself, at its time")
	case todo.DoneByPR:
		parts = append(parts, "by itself, at its PR's move")
	default:
		parts = append(parts, "by "+it.DoneBy)
	}
	return strings.Join(parts, " · ")
}

// clock is at as a time of day, with its date when not today: "12:41", "Jan 13 09:00".
func clock(at, now time.Time) string {
	if at.YearDay() != now.YearDay() || at.Year() != now.Year() {
		return at.Format("Jan 2 15:04")
	}
	return at.Format("15:04")
}

// detailLines are details wrapped to in, a blank line between paragraphs, an indented line keeping its indent.
func detailLines(details string, in int) []string {
	if details == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(details, "\n") {
		text := strings.TrimLeft(l, " ")
		indent := min(len(l)-len(text), in/2) // a deep indent leaves the text room
		for i, w := range strings.Split(ansi.Wrap(text, max(1, in-indent-2), ""), "\n") {
			pad := indent
			if i > 0 && listItem(text) {
				pad += 2 // a list item's next lines under its text
			}
			out = append(out, ansi.Truncate(strings.Repeat(" ", pad)+w, in, "…"))
		}
	}
	return out
}

// listItem reports a details line that starts a list item, as the page reads it: "- ", "* " or "• ".
func listItem(text string) bool {
	return strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") || strings.HasPrefix(text, "• ")
}

// Click runs a left click at x, y of the box drawn w×h: on an option or action it does what that line's key does
// (as j/k to it then ⏎), elsewhere (the border too) nothing; while a field is open, one on another line of the list leaves
// the field (its text kept) and runs that line. None while an action is under way.
func (m Model) Click(x, y, w, h int, now time.Time) (Model, tea.Cmd) {
	_, at := m.render(w, h, now)
	if x < 1 || x >= min(w, Width)-1 || y < 0 || y >= len(at) || at[y] < 0 || m.busy != "" {
		return m, nil
	}
	if m.typing != "" {
		if m.entries[at[y]].key == m.typing {
			return m, nil
		}
		m.typing = ""
	}
	m.sel = at[y]
	return m.key("enter")
}

// Height is how many rows View takes uncut at width w.
func (m Model) Height(w int, now time.Time) int {
	return strings.Count(m.View(w, 1<<16, now), "\n") + 1
}

// top is the box's top border: "project #id · column" at the left, the item's age at the right.
func (m Model) top(w int, now time.Time) string {
	it := m.it
	title := " " + project(it.Boss) + " #" + strconv.Itoa(it.ID) + " · " + columnTitle(it.Column) + " "
	age := ""
	if it.Added != 0 {
		age = " " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + " "
	}
	title = ansi.Truncate(title, max(0, w-6-ansi.StringWidth(age)), "…")
	fill := max(0, w-4-ansi.StringWidth(title)-ansi.StringWidth(age))
	return sidebar.Faint + "╭─" + reset + bold + title + reset + sidebar.Faint + strings.Repeat("─", fill) + reset +
		sidebar.Muted + age + reset + sidebar.Faint + "─╮" + reset
}

// columnTitle is a column's title, as the boards show it; "open" for one the model didn't name.
func columnTitle(column string) string { return cmp.Or(sidebar.ColumnTitles[column], "open") }

// listLines are the entries, one per line, the selected one marked ▸ on the selection band; the own-words line shows
// the field while it's typed in.
func (m Model) listLines(in int) []string {
	keyW, wordW := 1, 0
	for _, e := range m.entries {
		keyW, wordW = max(keyW, len(e.key)), max(wordW, ansi.StringWidth(e.word))
	}
	var out []string
	for i, e := range m.entries {
		mark := "  "
		if i == m.sel {
			mark = "▸ "
		}
		star := ""
		if m.ask() {
			star = "  "
			if e.rec {
				star = sidebar.Gold + "★ " + reset
			}
		}
		word := e.word
		if word != "" {
			word = bold + word + reset + strings.Repeat(" ", wordW-ansi.StringWidth(word)+3)
		}
		detail := sidebar.Muted + e.detail + reset
		if e.key == m.typing {
			detail = string(m.field) + cursorCell
			// Too narrow for the label and some of the field: the field alone
			if in-ansi.StringWidth(mark+e.key+"  "+star+word) < minField {
				word = ""
			}
			if w := in - ansi.StringWidth(mark+e.key+"  "+star+word); ansi.StringWidth(detail) > w {
				// The field's end, where it's typed
				detail = ansi.TruncateLeft(detail, ansi.StringWidth(detail)-w+1, "…")
			}
		}
		line := ansi.Truncate(mark+sidebar.Green+e.key+reset+"  "+star+word+detail, in, "…")
		if i == m.sel {
			line = sidebar.SelectedBG + strings.ReplaceAll(padTo(line, in), reset, reset+sidebar.SelectedBG) + reset
		}
		out = append(out, line)
	}
	return out
}

// statusText is the status row: the status, the session it names and when a start went. A person's wait is said by the
// waiting and follows rows, a closed boss by the list row: only what they leave out stays.
func (m Model) statusText(waiting bool, now time.Time) string {
	it := m.it
	var parts []string
	switch {
	case it.Status == sidebar.StatusBossClosed, waiting && it.Status == sidebar.StatusWaiting:
	default:
		parts = append(parts, cmp.Or(sidebar.StatusWord(it.Status), "—"))
	}
	if it.StatusOn != "" && it.StatusOn != it.Boss && !waiting {
		parts = append(parts, it.StatusOn)
	}
	if it.Started != nil {
		parts = append(parts, "sent to "+it.Started.To+" "+sidebar.AgeSpan(now.Sub(time.Unix(it.Started.At, 0)))+" ago")
	}
	return strings.Join(parts, " · ")
}

// facts are the item's column and assignee, status, link, list and age, and its tick time and check when it has them.
func (m Model) facts(in int, now time.Time) []string {
	it := m.it
	list := it.Boss
	if it.Closed {
		list += " (closed)"
	}
	if it.Added != 0 {
		list += " · added " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + " ago"
	}
	var facts [][2]string
	waiting := !it.Done && it.WaitingFor != "" && it.Assignee.Kind == sidebar.AssigneeBoss
	if waiting {
		followUp := "@" + m.assignee()
		if it.Closed {
			followUp = "no one (" + m.assignee() + " is closed)"
		}
		facts = append(facts, [2]string{"column", columnTitle(it.Column)}, [2]string{"waiting", it.WaitingFor}, [2]string{"follows", followUp})
	} else {
		facts = append(facts, [2]string{"column", columnTitle(it.Column) + " · @" + m.assignee()})
	}
	if status := m.statusText(waiting, now); !it.Done && status != "" {
		facts = append(facts, [2]string{"status", sidebar.StatusColours[it.Status] + status + reset})
	}
	facts = append(facts, [2]string{"link", cmp.Or(it.Link, "—")}, [2]string{"list", list})
	doing := it.Now
	if it.Doing != nil {
		doing = it.Doing.Text
	}
	live := len(m.block(in, now)) > 0
	// Only a worker's block says the check: the boss's own says its doing line alone
	session := live && m.worker()
	if doing != "" && !live {
		facts = append(facts, [2]string{"doing", doing})
	}
	if t := ticked(m.it, now); it.Done && t != "" {
		facts = append(facts, [2]string{"ticked", t})
	}
	if it.Until != 0 {
		facts = append(facts, [2]string{"until", "ticks itself " + wait.Clock(time.Unix(it.Until, 0), now)})
	}
	if it.Check != nil && it.Check.Kind != sidebar.CheckBossClosed && !session {
		facts = append(facts, [2]string{"check", it.Check.Tone().Colour() + it.Check.Short(now) + reset})
	}
	out := make([]string, len(facts))
	for i, f := range facts {
		out[i] = ansi.Truncate(sidebar.Muted+fmt.Sprintf("%-8s", f[0])+reset+f[1], in, "…")
	}
	return out
}

// keys is the bottom line: the keys this item takes.
func (m Model) keys() string {
	var ks [][2]string
	switch {
	case m.typing == "i":
		ks = [][2]string{{"⏎", "on it"}, {"esc", "back"}}
	case m.typing != "":
		ks = [][2]string{{"⏎", "answer"}, {"esc", "back"}}
	case m.ask():
		if !m.it.Closed {
			ks = [][2]string{{"1-" + strconv.Itoa(min(9, len(m.it.Options))), "answer"}, {"j/k ⏎", "pick"}, {"m", "own words"}}
		}
	default:
		ks = [][2]string{{"j/k ⏎", "run"}}
		for _, e := range m.entries {
			ks = append(ks, [2]string{e.key, strings.ToLower(e.word)})
		}
	}
	if m.ask() && m.typing == "" {
		if m.it.Link != "" {
			ks = append(ks, [2]string{"o", "open link"})
		}
		if !m.it.Closed {
			ks = append(ks, [2]string{"g", "go to boss"})
		}
	}
	if m.typing == "" {
		ks = append(ks, [2]string{"esc", "close"})
	}
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = bold + k[0] + reset + sidebar.Muted + " " + k[1] + reset
	}
	return strings.Join(parts, sidebar.Muted+" · "+reset)
}

// padTo pads l with spaces to w columns.
func padTo(l string, w int) string {
	return l + strings.Repeat(" ", max(0, w-ansi.StringWidth(l)))
}
