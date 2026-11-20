// Package itemview is one to do item in a box: its text, context, options or actions, facts and keys. It draws and
// takes keys only; its host (dev todo show, in a tmux popup) shows it and closes it once Done.
package itemview

import (
	"cmp"
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

// Item is one item of boss's list as dev todo --all --json gives it, its status set; Closed for a closed boss's.
type Item struct {
	Boss   string
	Closed bool
	todo.Shown
}

// Actions are the paths the view's keys run, each off the update loop: dev todo answer, done and start (their
// output, or why not), the browser, a switch of the client to a session.
type Actions struct {
	Answer func(boss string, id int, question, answer string) (string, error)
	Tick   func(boss string, id int, text string) (string, error)
	Start  func(boss string, id int, text string) (string, error)
	Open   func(url string) error
	Switch func(session string) error
}

// entry is a line of the view's list: an ask's option (key 1-9) or own words (m), or an action.
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
	typing      bool   // the own-words field takes the keys
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

// ask, worker and plain are how the view treats the item: the user's own ask, on a worker, or the user's own; whose
// it is as the list decides (todo.Own), so an ask handed to a worker is the worker's.
func (m Model) ask() bool    { return m.own() && len(m.it.Options) > 0 }
func (m Model) worker() bool { return !m.own() && m.it.Session != nil }
func (m Model) plain() bool  { return m.own() && len(m.it.Options) == 0 }
func (m Model) own() bool    { return todo.Own(m.it.On, m.it.Session, m.it.Boss) }

// canStart and canTick mirror the board's expanded row (herd's actions): a check item isn't started; a closed boss's
// item starts only while the manager runs.
func (m Model) canStart() bool {
	return m.plain() && m.it.Status != sidebar.StatusCheck && (!m.it.Closed || m.managerRuns)
}
func (m Model) canTick() bool { return m.plain() }

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
	if m.canTick() {
		out = append(out, entry{key: "d", word: "Tick", detail: "done: it leaves every board"})
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
			return m, nil
		}
		m.Done, m.Result = true, msg.out
	case tea.KeyMsg:
		if m.typing {
			return m.typeKey(msg)
		}
		return m.key(msg.String())
	}
	return m, nil
}

// key runs k: j/k/↓/↑ move (wrapping), ⏎ the selected line's key, 1-9 answer, m own words, s start, d tick, w go to the
// worker, o open the link, g go to the boss, esc/q close. Keys but ctrl+c wait while an action is under way, so its
// outcome shows.
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
	case k == "m" && m.ask() && !it.Closed:
		m.typing = true
		m.sel = len(m.entries) - 1
		return m, nil
	case k == "s" && m.canStart():
		return m.run("starting", func() (string, error) { return m.acts.Start(it.Boss, it.ID, it.Text) })
	case k == "d" && m.canTick():
		return m.run("ticking", func() (string, error) { return m.acts.Tick(it.Boss, it.ID, it.Text) })
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
	case len(k) == 1 && strings.Contains("sdw", k):
		m.msg = k + " doesn't act on this item: its keys are below"
	}
	return m, nil
}

// typeKey edits the own-words field: ⏎ answers with it, esc leaves it, backspace deletes; a pasted line break is a
// space.
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
		m.typing, m.msg = false, ""
	case tea.KeyBackspace:
		if len(m.field) > 0 {
			m.field = m.field[:len(m.field)-1]
		}
	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.field))
		if text == "" {
			m.msg = "Type an answer, or esc"
			return m, nil
		}
		it := m.it
		return m.run("answering", func() (string, error) { return m.acts.Answer(it.Boss, it.ID, it.Text, text) })
	case tea.KeySpace:
		m.field = append(m.field, ' ')
	case tea.KeyRunes:
		for _, r := range k.Runes {
			if unicode.IsSpace(r) {
				r = ' '
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
	for _, c := range it.Context {
		head = append(head, sidebar.Muted+ansi.Truncate(c, in, "…")+reset)
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
cut:
	for all, _ := lines(); len(all) > room; all, _ = lines() {
		switch {
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

// Click runs a left click at row y of the box drawn w×h: on an option or action it does what that line's key does
// (as j/k to it then ⏎); one while the own-words field is open leaves the field (its text kept) unless it's on the
// field's own line.
func (m Model) Click(y, w, h int, now time.Time) (Model, tea.Cmd) {
	_, at := m.render(w, h, now)
	if y < 0 || y >= len(at) || at[y] < 0 || m.busy != "" {
		return m, nil
	}
	if m.typing {
		if at[y] == len(m.entries)-1 {
			return m, nil
		}
		m.typing = false
	}
	m.sel = at[y]
	return m.key("enter")
}

// Height is how many rows View takes uncut at width w.
func (m Model) Height(w int, now time.Time) int {
	return strings.Count(m.View(w, 1<<16, now), "\n") + 1
}

// top is the box's top border: "project #id · status" at the left, the item's age at the right.
func (m Model) top(w int, now time.Time) string {
	it := m.it
	title := " " + project(it.Boss) + " #" + strconv.Itoa(it.ID) + " · " + statusTitle(it.Status) + " "
	age := ""
	if it.Added != 0 {
		age = " " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + " "
	}
	title = ansi.Truncate(title, max(0, w-6-ansi.StringWidth(age)), "…")
	fill := max(0, w-4-ansi.StringWidth(title)-ansi.StringWidth(age))
	return sidebar.Faint + "╭─" + reset + bold + title + reset + sidebar.Faint + strings.Repeat("─", fill) + reset +
		sidebar.Muted + age + reset + sidebar.Faint + "─╮" + reset
}

// statusTitle is a status as the title says it: its group's title for the user's own, else the word.
func statusTitle(status string) string {
	switch status {
	case sidebar.StatusDecide:
		return "To decide"
	case sidebar.StatusYours:
		return "Yours"
	}
	return cmp.Or(status, "open")
}

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
		if e.key == "m" && m.typing {
			detail = string(m.field) + cursorCell
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

// facts are the item's status and whom it's on, link, list and age, and its tick time and check when it has them.
func (m Model) facts(in int, now time.Time) []string {
	it := m.it
	status := cmp.Or(it.Status, "—")
	if it.StatusOn != "" && it.StatusOn != it.Boss {
		status += " · " + it.StatusOn
	}
	if it.Started != nil {
		status += " · sent to " + it.Started.To + " " + sidebar.AgeSpan(now.Sub(time.Unix(it.Started.At, 0))) + " ago"
	}
	list := it.Boss
	if it.Closed {
		list += " (closed)"
	}
	if it.Added != 0 {
		list += " · added " + sidebar.AgeSpan(now.Sub(time.Unix(it.Added, 0))) + " ago"
	}
	facts := [][2]string{{"status", sidebar.StatusColours[it.Status] + status + reset}, {"link", cmp.Or(it.Link, "—")}, {"list", list}}
	if it.Until != 0 {
		facts = append(facts, [2]string{"until", "ticks itself " + wait.Clock(time.Unix(it.Until, 0), now)})
	}
	if it.Check != nil {
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
	case m.typing:
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
	if m.ask() && !m.typing {
		if m.it.Link != "" {
			ks = append(ks, [2]string{"o", "open link"})
		}
		if !m.it.Closed {
			ks = append(ks, [2]string{"g", "go to boss"})
		}
	}
	if !m.typing {
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
