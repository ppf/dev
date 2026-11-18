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

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

// Each read runs dev todo --all, once per sidebar.
const needsInterval = 2 * time.Second

// needsTimeout bounds a read of the lists, so a hung one can't pile up reads; an answer may wait on the boss's inbox.
const (
	needsTimeout  = 10 * time.Second
	answerTimeout = 30 * time.Second
)

// NeedList is one boss's list as dev todo --all --json prints it (internal/todo imports this package, so the
// shape is read here, not shared); a board builds it from its own read.
type NeedList struct {
	Boss  string     `json:"boss"`
	Error string     `json:"error"` // the list wasn't read
	Items []NeedItem `json:"items"`
}

// NeedItem is a list item as dev todo --all --json prints it.
type NeedItem struct {
	ID      int      `json:"id"`
	Text    string   `json:"text"`
	On      string   `json:"on"`
	Done    bool     `json:"done"`
	Options []string `json:"options"`
}

// NeedCounts are the lists' counts for the roll-ups (Subtree), by boss.
func NeedCounts(needs []NeedList) map[string]ListCount {
	out := map[string]ListCount{}
	for _, l := range needs {
		c := ListCount{Unread: l.Error != ""}
		for _, it := range l.Items {
			c.Add(it.Done, it.On, len(it.Options))
		}
		out[l.Boss] = c
	}
	return out
}

// NeedKind is what a NEEDS YOU row waits on, in the order rows sort.
type NeedKind int

const (
	NeedDialog  NeedKind = iota // a session's open dialog
	NeedAsk                     // a turn that ended on a question (an asks note)
	NeedListAsk                 // a list item with options
	NeedPlain                   // a list item without options
	NeedUnread                  // a list that wasn't read: Text says why
)

// NeedRow is one row of NEEDS YOU: a session waiting on a dialog or on its turn's question, a list item waiting
// on the user, or a list not read.
type NeedRow struct {
	Kind    NeedKind
	Project string   // the session's or the list owner's project
	Who     string   // "boss" for a boss or its list, else the worker's name less its project
	Boss    string   // the boss whose list it is; "" for a session's row
	ID      int      // the list item's number
	Text    string   // the item's text, what the dialog or the question asks, or why the list wasn't read
	Options []string // a list ask's answers; a dialog's choices (the sidebar only shows them; a board answers them)
	Session string   // where ⏎ goes: the session, or the list's boss
}

// key identifies the row across reads: a list's numbers restart once it empties, so the text is part of it.
func (r NeedRow) key() string {
	switch r.Kind {
	case NeedDialog, NeedAsk:
		return fmt.Sprintf("%d\x00%s", r.Kind, r.Session)
	case NeedUnread:
		return "broken\x00" + r.Boss
	}
	return fmt.Sprintf("ask\x00%s\x00%d\x00%s", r.Boss, r.ID, r.Text)
}

// needsMsg is a finished read; chain is set for the polling chain's own, which schedules the next, so an extra read
// (after an answer) doesn't start another chain.
type needsMsg struct {
	lists []NeedList
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

func loadNeeds() ([]NeedList, error) {
	out, _, err := runTodo(needsTimeout, "--all", "--json")
	if err != nil {
		return nil, fmt.Errorf("dev todo --all: %w", err)
	}
	var lists []NeedList
	if err := json.Unmarshal([]byte(out), &lists); err != nil {
		return nil, fmt.Errorf("dev todo --all: %w", err)
	}
	return lists, nil
}

// AnswerNeed answers ask id of boss with option, only while the item still reads question.
func AnswerNeed(boss string, id int, question, option string) (string, error) {
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

// needBoard is the sidebar's row standing for the boards' rows: ⏎ goes to the first board with any.
const needBoard = NeedUnread + 1

// needRows are the lines of NEEDS YOU: with a boss on the server, a row for what its boards show (Boards), then what
// no board shows (a session under no boss, a list whose boss is gone); else every row.
func (m Model) needRows() []NeedRow {
	all := NeedRows(m.snap.sessions, m.needs, "")
	boards := Boards(m.snap.sessions)
	if len(boards) == 0 {
		return all
	}
	shown := map[string]bool{}
	board := NeedRow{Kind: needBoard}
	for _, b := range boards {
		rows := NeedRows(m.snap.sessions, m.needs, b)
		if len(rows) > 0 && board.Session == "" {
			board.Session = b
		}
		board.ID += len(rows)
		for _, r := range rows {
			shown[r.key()] = true
		}
	}
	var out []NeedRow
	if board.ID > 0 {
		board.Text = fmt.Sprintf("%d on the board", board.ID)
		out = append(out, board)
	}
	return append(out, slices.DeleteFunc(all, func(r NeedRow) bool { return shown[r.key()] })...)
}

// needCount is how many rows NEEDS YOU stands for: the boards' each.
func needCount(rows []NeedRow) int {
	n := len(rows)
	if len(rows) > 0 && rows[0].Kind == needBoard {
		n += rows[0].ID - 1
	}
	return n
}

// gotoBoard leaves NEEDS YOU for board's NEEDS YOU.
func (m Model) gotoBoard(board string) (tea.Model, tea.Cmd) {
	c, client := m.c, m.client()
	from := Origin{Session: m.snap.current, Pane: m.pane}
	m.needMode = false
	return m, runAction(c, client, func() error { return focusBoard(c, client, board, from) })
}

// focusBoard switches client to board's session and focuses its previews pane, asking it to focus NEEDS YOU
// (OptNeedsFocus).
func focusBoard(c *tmux.Client, client, board string, from Origin) error {
	out, err := c.Run("list-panes", "-s", "-t", tmux.SessionTarget(board), "-F", "#{pane_id}\t#{"+OptBossPane+"}")
	if err != nil {
		return err
	}
	pane := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if id, role, _ := strings.Cut(l, "\t"); role == RolePreviews {
			pane = id
		}
	}
	if pane == "" {
		return fmt.Errorf("%s has no board", board)
	}
	if board != from.Session {
		if err := switchTo(c, client, board, from); err != nil {
			return err
		}
	}
	_, err = c.Run("set-option", "-p", "-t", pane, OptNeedsFocus, "1", ";", "select-window", "-t", pane, ";", "select-pane", "-t", pane)
	resetKeyTable(c, client)
	return err
}

// NeedRows are the rows of NEEDS YOU from sessions in Order and the bosses' lists, sorted by kind (NeedKind), in
// sessions' and lists' order within one: each session waiting on a dialog or on the question its turn ended on,
// each open item on no one, each list not read. root scopes them to its board: the sessions under it (its own
// question is on its screen) and its and their lists; "" takes them all.
func NeedRows(sessions []Session, lists []NeedList, root string) []NeedRow {
	scope, owners := sessions, map[string]bool{}
	if root != "" {
		i := slices.IndexFunc(sessions, func(s Session) bool { return s.Name == root })
		if i < 0 {
			return nil
		}
		scope = under(sessions, i)
		owners[root] = true
		for _, s := range scope {
			owners[s.Name] = s.Boss
		}
	}
	project := map[string]string{}
	for _, s := range sessions {
		project[s.Name] = s.Project
	}
	var rows []NeedRow
	for _, s := range scope {
		who := strings.TrimPrefix(s.Name, s.Project+"-")
		if s.Boss {
			who = "boss"
		}
		row := NeedRow{Project: s.Project, Who: who, Session: s.Name}
		switch {
		case s.Deciding:
			row.Kind = NeedDialog
			row.Text, row.Options = dialogText(s)
		case s.Rank() == RankNeedsYou:
			_, text, _ := strings.Cut(s.Note, ": ")
			row.Kind, row.Text = NeedAsk, cmp.Or(strings.Join(strings.Fields(text), " "), "asks")
		default:
			continue
		}
		rows = append(rows, row)
	}
	for _, l := range lists {
		if root != "" && !owners[l.Boss] {
			continue
		}
		project, ok := project[l.Boss]
		if !ok {
			project = strings.TrimPrefix(l.Boss, "boss-")
		}
		row := NeedRow{Project: project, Who: "boss", Boss: l.Boss, Session: l.Boss}
		if l.Error != "" {
			row.Kind, row.Text = NeedUnread, l.Error
			rows = append(rows, row)
		}
		for _, it := range l.Items {
			if it.Done || it.On != "" {
				continue
			}
			row.Kind, row.ID, row.Text, row.Options = NeedPlain, it.ID, it.Text, it.Options
			if len(it.Options) > 0 {
				row.Kind = NeedListAsk
			}
			rows = append(rows, row)
		}
	}
	slices.SortStableFunc(rows, func(a, b NeedRow) int { return cmp.Compare(a.Kind, b.Kind) })
	return rows
}

// Boards are the bosses whose boards show NEEDS YOU for the sessions under them: a manager, and each top-level boss
// (Depth 0) in no manager's group.
func Boards(sessions []Session) []string {
	var out []string
	for _, s := range sessions {
		if s.Boss && (s.Root == s.Name || s.Root == "" && s.Depth == 0) {
			out = append(out, s.Name)
		}
	}
	return out
}

// dialogText falls back to the session's name when no question was recorded.
func dialogText(s Session) (text string, options []string) {
	oneLine := func(v string) string { return strings.Join(strings.Fields(v), " ") }
	q, _ := agent.ParseQuestion(s.Question)
	switch {
	case q.Tool == agent.AskUserQuestion && len(q.Questions) > 0:
		for _, c := range q.Questions[0].Options {
			options = append(options, oneLine(c.Label))
		}
		return oneLine(q.Questions[0].Question), options
	case q.Command != "":
		return oneLine(q.Tool + ": " + q.Command), nil
	case q.Tool != "":
		return q.Tool, nil
	}
	return s.Name, nil
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

// handleNeedsKey: j/k move, ⏎ opens a row, then goes to its session (the boards' row to its board), 1-9 answers an
// open ask, esc/q leaves.
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
		if !m.needOpen || m.answering || row.Kind != NeedListAsk || n > len(row.Options) {
			return m, nil
		}
		m.answering = true
		client, answer := m.client(), m.needsAnswer
		return m, func() tea.Msg {
			out, err := answer(row.Boss, row.ID, row.Text, row.Options[n-1])
			return answeredMsg{out: out, err: err, client: client}
		}
	}
	return m, nil
}

func (m Model) openNeed(row NeedRow) (tea.Model, tea.Cmd) {
	switch {
	case row.Kind == needBoard:
		return m.gotoBoard(row.Session)
	case row.Kind == NeedListAsk:
		m.needOpen = !m.needOpen
		return m, nil
	case !m.needOpen:
		m.needOpen = true
		return m, nil
	}
	c, client, current := m.c, m.client(), m.snap.current
	m.needMode, m.needOpen = false, false
	if err := m.setSelection(row.Session); err != nil {
		showMessage(c, client, "❌ "+err.Error())
	}
	if row.Session == current {
		return m, runAction(c, client, func() error { return focusContent(c, client, current) })
	}
	from := Origin{Session: current, Pane: m.pane}
	return m, runAction(c, client, func() error { return switchTo(c, client, row.Session, from) })
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

// needHit is a click target: row idx's line, or its open ask's option opt (1-9) on it.
type needHit struct{ line, idx, opt int }

func (m Model) renderNeeds(w, h int) ([]string, []needHit) {
	rows := m.needRows()
	if len(rows) == 0 || h < needsMinHeight {
		return nil, nil
	}
	if len(rows) == 1 && rows[0].Kind == needBoard {
		// All on the boards: one line pointing there
		line := " " + red + bold + fmt.Sprintf("NEEDS YOU %d", rows[0].ID) + reset
		if m.needsErr != nil {
			line += muted + " (stale)" + reset
		}
		line = truncate(line+faint+"  → board"+reset, w)
		return []string{cardLine(line, ansi.StringWidth(line), w, m.needMode), ""}, []needHit{{line: 0}}
	}
	head := " " + red + bold + fmt.Sprintf("NEEDS YOU %d", needCount(rows)) + reset
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
	var openOpts []int
	if m.needMode && m.needOpen && m.needIdx < len(rows) {
		open[m.needIdx], openOpts = m.openLines(rows[m.needIdx], w)
		n := min(len(open[m.needIdx]), max(1, h-3))
		open[m.needIdx], openOpts = open[m.needIdx][:n], openOpts[:n]
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
			for j, o := range openOpts {
				if o > 0 {
					hits = append(hits, needHit{line: len(lines) + j, idx: i, opt: o})
				}
			}
			lines = append(lines, l...)
		} else {
			lines = append(lines, m.needLine(rows[i], w, m.needMode && i == m.needIdx))
		}
	}
	return append(lines, ""), hits
}

func (m Model) needLine(r NeedRow, w int, selected bool) string {
	project := truncate(r.label(), max(1, min(12, w/3)))
	mark := "☐"
	switch r.Kind {
	case NeedDialog:
		mark = red + "●" + reset
	case NeedAsk:
		mark = gold + bold + "?" + reset
	case NeedUnread:
		mark = gold + "⚠" + reset
	case needBoard:
		mark = faint + "→" + reset
	}
	vis := 1 + ansi.StringWidth(project) + 2 + 1 + 1
	text := truncate(r.Text, max(1, w-vis-1))
	line := " " + muted + project + reset + "  " + mark + " " + text
	return cardLine(line, vis+ansi.StringWidth(text), w, selected)
}

// label is the row's project column: a session's name, else the list owner's project.
func (r NeedRow) label() string {
	if r.Kind == needBoard {
		return "board"
	}
	if r.Kind == NeedDialog || r.Kind == NeedAsk {
		return r.Session
	}
	return r.Project
}

// openLines is the open ask: wrapped question, then each option numbered, wrapped under its number; opts is each
// line's option number when 1-9 answer it, else 0.
func (m Model) openLines(r NeedRow, w int) (lines []string, opts []int) {
	vis := 1 + ansi.StringWidth(truncate(r.label(), max(1, min(12, w/3)))) + 2 + 1 + 1
	lines = []string{m.needLine(r, w, true)}
	if text := strings.Split(ansi.Wrap(r.Text, max(1, w-vis-1), ""), "\n"); len(text) > 1 {
		first := r
		first.Text = text[0]
		lines = []string{m.needLine(first, w, true)}
		for _, t := range text[1:] {
			lines = append(lines, cardLine(strings.Repeat(" ", vis)+t, vis+ansi.StringWidth(t), w, true))
		}
	}
	opts = make([]int, len(lines))
	for i, o := range r.Options {
		n := 0
		label, rec := optionLabel(o)
		if rec {
			label = gold + "★ " + reset + label
		}
		num := green + strconv.Itoa(i+1) + reset
		if r.Kind != NeedListAsk {
			num = muted + "·" + reset
		} else if i < 9 {
			n = i + 1
		}
		for j, p := range strings.Split(ansi.Wrap(label, max(1, w-7), ""), "\n") {
			line := "      " + p
			if j == 0 {
				line = "    " + num + " " + p
			}
			lines = append(lines, cardLine(line, ansi.StringWidth(line), w, true))
			opts = append(opts, n)
		}
	}
	return lines, opts
}

func (m Model) needsHint() string {
	rows := m.needRows()
	if m.needIdx >= len(rows) {
		return ""
	}
	row := rows[m.needIdx]
	ask := row.Kind == NeedListAsk
	var hint string
	switch {
	case row.Kind == needBoard:
		hint = "⏎ board · esc leave"
	case m.needOpen && ask:
		hint = "1-9 answer · esc back"
	case m.needOpen:
		hint = "⏎ go · esc back"
	case ask:
		hint = "j/k ⏎ options · esc leave"
	default:
		hint = "j/k ⏎ expand · esc leave"
	}
	return " " + faint + hint + reset
}
