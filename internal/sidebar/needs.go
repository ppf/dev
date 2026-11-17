package sidebar

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Each read runs dev todo --all, once per sidebar.
const needsInterval = 2 * time.Second

// needsTimeout bounds a read of the lists, so a hung one can't pile up reads; an answer may wait on the boss's inbox.
const (
	needsTimeout  = 10 * time.Second
	answerTimeout = 30 * time.Second
)

// needList is one boss's list as dev todo --all --json prints it (internal/todo imports this package, so the
// shape is read here, not shared).
type needList struct {
	Boss  string     `json:"boss"`
	Items []needItem `json:"items"`
}

type needItem struct {
	ID      int      `json:"id"`
	Text    string   `json:"text"`
	On      string   `json:"on"`
	Done    bool     `json:"done"`
	Options []string `json:"options"`
}

// needRow is one line of NEEDS YOU: a list item waiting on the user, or a session waiting on a dialog.
type needRow struct {
	project string
	boss    string // the boss whose list it is; "" for a dialog
	id      int
	text    string // the item's text, or the session's name for a dialog
	options []string
	session string // where ⏎ goes: the boss, or the session with the dialog
	dialog  bool
}

// key identifies the row across reads: a list's numbers restart once it empties, so the text is part of it.
func (r needRow) key() string {
	if r.dialog {
		return "dialog\x00" + r.session
	}
	return fmt.Sprintf("ask\x00%s\x00%d\x00%s", r.boss, r.id, r.text)
}

// needsMsg is a finished read; chain is set for the polling chain's own, which schedules the next, so an extra read
// (after an answer) doesn't start another chain.
type needsMsg struct {
	lists []needList
	err   error
	chain bool
}

type needsTickMsg struct{}

type answeredMsg struct {
	out    string
	err    error
	client string
}

// runTodo runs this binary's dev todo with args as the user, within timeout: a sidebar pane may carry CLAUDECODE from a
// server a Claude session started, which dev todo answer refuses; the sidebar is the user's own UI. It returns stdout
// and stderr's words.
func runTodo(timeout time.Duration, args ...string) (stdout, warning string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, append([]string{"todo"}, args...)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "CLAUDECODE=") })
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	warning = strings.Join(strings.Fields(stderr.String()), " ")
	switch {
	case ctx.Err() != nil:
		return "", "", fmt.Errorf("dev todo %s timed out after %s: check the list, an answer may be ticked", args[0], timeout)
	case err != nil:
		return "", "", errors.New(cmp.Or(warning, err.Error()))
	}
	return strings.TrimSpace(string(out)), warning, nil
}

func loadNeeds() ([]needList, error) {
	out, _, err := runTodo(needsTimeout, "--all", "--json")
	if err != nil {
		return nil, fmt.Errorf("dev todo --all: %w", err)
	}
	var lists []needList
	if err := json.Unmarshal([]byte(out), &lists); err != nil {
		return nil, fmt.Errorf("dev todo --all: %w", err)
	}
	return lists, nil
}

// answerNeed answers ask id of boss with option, only while the item still reads question.
func answerNeed(boss string, id int, question, option string) (string, error) {
	out, warning, err := runTodo(answerTimeout, "answer", "--question="+question, "--", boss, strconv.Itoa(id), option)
	// A note queued for a boss that isn't resting says so on stderr
	return strings.TrimSpace(out + " " + warning), err
}

func needsTick() tea.Cmd {
	return tea.Tick(needsInterval, func(time.Time) tea.Msg { return needsTickMsg{} })
}

// readNeeds reads the lists off the update loop; none without a loader.
func (m Model) readNeeds(chain bool) tea.Cmd {
	if m.needsLoad == nil {
		return nil
	}
	load := m.needsLoad
	return func() tea.Msg {
		lists, err := load()
		return needsMsg{lists: lists, err: err, chain: chain}
	}
}

// needRows are the lines of NEEDS YOU: each open item no one else is on, then each session waiting on a dialog.
func (m Model) needRows() []needRow {
	group := map[string]string{}
	for _, s := range m.snap.sessions {
		group[s.Name] = s.Group
	}
	var rows []needRow
	for _, l := range m.needs {
		project, ok := group[l.Boss]
		if !ok {
			project = l.Boss
		}
		for _, it := range l.Items {
			if !it.Done && it.On == "" {
				rows = append(rows, needRow{project: project, boss: l.Boss, id: it.ID, text: it.Text, options: it.Options, session: l.Boss})
			}
		}
	}
	for _, s := range m.snap.sessions {
		if s.Deciding {
			rows = append(rows, needRow{project: s.Group, text: s.Name, session: s.Name, dialog: true})
		}
	}
	return rows
}

// fixNeeds keeps the cursor on its row after the rows changed; a row that went moves it to its place, options shut.
func (m *Model) fixNeeds() {
	if !m.needMode {
		return
	}
	rows := m.needRows()
	if len(rows) == 0 || !m.snap.focused || m.snap.height < needsMinHeight {
		m.needMode, m.needOpen = false, false
		return
	}
	for i, r := range rows {
		if r.key() == m.needKey {
			m.needIdx = i
			return
		}
	}
	m.needIdx = min(m.needIdx, len(rows)-1)
	m.needKey, m.needOpen = rows[m.needIdx].key(), false
}

// handleNeedsKey: j/k move, ⏎ opens an ask's options or goes to the session, 1-9 answers an open ask, esc/q leaves.
func (m Model) handleNeedsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.needRows()
	if m.needIdx >= len(rows) || rows[m.needIdx].key() != m.needKey {
		m.needMode, m.needOpen = false, false
		return m, nil
	}
	row := rows[m.needIdx]
	switch s := k.String(); s {
	case "j", "down", "k", "up":
		dir := 1
		if s == "k" || s == "up" {
			dir = -1
		}
		m.needIdx = ((m.needIdx+dir)%len(rows) + len(rows)) % len(rows)
		m.needKey, m.needOpen = rows[m.needIdx].key(), false
		return m, nil
	case "enter":
		return m.openNeed(row)
	case "esc", "q":
		if m.needOpen {
			m.needOpen = false
		} else {
			m.needMode = false
		}
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n, _ := strconv.Atoi(s)
		if !m.needOpen || m.answering || n > len(row.options) {
			return m, nil
		}
		m.answering = true
		client, answer := m.client(), m.needsAnswer
		return m, func() tea.Msg {
			out, err := answer(row.boss, row.id, row.text, row.options[n-1])
			return answeredMsg{out: out, err: err, client: client}
		}
	}
	return m, nil
}

func (m Model) openNeed(row needRow) (tea.Model, tea.Cmd) {
	if !row.dialog && len(row.options) > 0 {
		m.needOpen = !m.needOpen
		return m, nil
	}
	c, client, current := m.c, m.client(), m.snap.current
	m.needMode, m.needOpen = false, false
	if err := m.setSelection(row.session); err != nil {
		showMessage(c, client, "❌ "+err.Error())
	}
	if row.session == current {
		return m, runAction(c, client, func() error { return focusContent(c, client, current) })
	}
	from := Origin{Session: current, Pane: m.pane}
	return m, runAction(c, client, func() error { return switchTo(c, client, row.session, from) })
}

func (m Model) enterNeeds() Model {
	if rows := m.needRows(); len(rows) > 0 && m.snap.height >= needsMinHeight {
		m.needMode, m.needOpen, m.needIdx, m.needKey = true, false, 0, rows[0].key()
	} else {
		showMessage(m.c, m.client(), "Nothing needs you")
	}
	return m
}

// optionLabel is option o without its " (Recommended)" (todo.Label, which this package can't import); rec reports it had one.
func optionLabel(o string) (label string, rec bool) {
	const suffix = "(recommended)"
	if n := len(o) - len(suffix); n >= 0 && strings.EqualFold(o[n:], suffix) {
		return strings.TrimSpace(o[:n]), true
	}
	return o, false
}

const needsMinHeight = 10

func needsBudget(h int) int { return max(5, h/2) }

type needHit struct{ line, idx int }

func (m Model) renderNeeds(w, h int) ([]string, []needHit) {
	rows := m.needRows()
	if len(rows) == 0 || h < needsMinHeight {
		return nil, nil
	}
	head := " " + red + bold + fmt.Sprintf("NEEDS YOU %d", len(rows)) + reset
	if m.needsErr != nil {
		head += muted + " (stale)" + reset
	}
	if m.snap.focused && !m.needMode {
		// The key in, where the bottom row's hint has no room for it
		head += faint + " · n" + reset
	}
	lines := []string{truncate(head, w), ""}
	var hits []needHit
	open := map[int][]string{}
	if m.needMode && m.needOpen && m.needIdx < len(rows) {
		open[m.needIdx] = m.openLines(rows[m.needIdx], w)
	}
	height := func(i int) int {
		if l, ok := open[i]; ok {
			return len(l)
		}
		return 1
	}
	// Room for rows: the budget less the header, the blank under it and the blank after the section; an open ask always fits
	room := needsBudget(h) - 3
	cur := 0
	if m.needMode {
		cur = min(m.needIdx, len(rows)-1)
		room = max(room, height(cur))
	}
	// Scroll from the top only as far as the cursor's row needs
	span := func(from int) int {
		used := 0
		for i := from; i <= cur; i++ {
			used += height(i)
		}
		return used
	}
	start := 0
	for start < cur && span(start) > room {
		start++
	}
	for i, used := start, 0; i < len(rows) && used+height(i) <= room; i++ {
		used += height(i)
		hits = append(hits, needHit{line: len(lines), idx: i})
		if l, ok := open[i]; ok {
			lines = append(lines, l...)
		} else {
			lines = append(lines, m.needLine(rows[i], w, m.needMode && i == m.needIdx))
		}
	}
	return append(lines, ""), hits
}

func (m Model) needLine(r needRow, w int, selected bool) string {
	project := truncate(r.project, max(1, min(12, w/3)))
	mark := "☐"
	if r.dialog {
		mark = red + "●" + reset
	}
	vis := 1 + ansi.StringWidth(project) + 2 + 1 + 1
	text := truncate(r.text, max(1, w-vis-1))
	line := " " + muted + project + reset + "  " + mark + " " + text
	return cardLine(line, vis+ansi.StringWidth(text), w, selected)
}

// openLines is the open ask: wrapped question, then a numbered line per option.
func (m Model) openLines(r needRow, w int) []string {
	vis := 1 + ansi.StringWidth(truncate(r.project, max(1, min(12, w/3)))) + 2 + 1 + 1
	lines := []string{m.needLine(r, w, true)}
	if text := strings.Split(ansi.Wrap(r.text, max(1, w-vis-1), ""), "\n"); len(text) > 1 {
		lines = []string{m.needLine(needRow{project: r.project, text: text[0]}, w, true)}
		for _, t := range text[1:] {
			lines = append(lines, cardLine(strings.Repeat(" ", vis)+t, vis+ansi.StringWidth(t), w, true))
		}
	}
	for i, o := range r.options {
		label, rec := optionLabel(o)
		if rec {
			label = gold + "★ " + reset + label
		}
		line := "    " + green + strconv.Itoa(i+1) + reset + " " + truncate(label, max(1, w-7))
		lines = append(lines, cardLine(line, ansi.StringWidth(line), w, true))
	}
	return lines
}

func (m Model) needsHint() string {
	rows := m.needRows()
	if m.needIdx >= len(rows) {
		return ""
	}
	hint := "j/k ⏎ go · esc leave"
	switch {
	case m.needOpen:
		hint = "1-9 answer · esc back"
	case !rows[m.needIdx].dialog && len(rows[m.needIdx].options) > 0:
		hint = "j/k ⏎ options · esc leave"
	}
	return " " + faint + hint + reset
}
