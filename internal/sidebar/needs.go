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
	Boss    string     `json:"boss"`
	Error   string     `json:"error"` // the list wasn't read
	Items   []NeedItem `json:"items"`
	Updated int64      `json:"updated"` // the list's last change, unix seconds
	Stale   bool       `json:"stale"`   // unchanged for long: its items may be out of date
	Closed  bool       `json:"closed"`  // its boss session is gone: a manager's TO DO shows it
}

// Manager is the session of the manager over the bosses (inbox.Manager).
const Manager = "manager"

// NeedItem is a list item as dev todo --all --json prints it.
type NeedItem struct {
	ID      int      `json:"id"`
	Text    string   `json:"text"`
	On      string   `json:"on"`
	Done    bool     `json:"done"`
	Options []string `json:"options"`
	Context []string `json:"context"` // an ask's lines under its question
	Added   int64    `json:"added"`   // when it was added or last replaced, unix seconds; 0 for an older dev's
	Check   *Check   `json:"check"`   // why it may be out of date
	Until   int64    `json:"until"`   // when it ticks by itself, unix seconds; 0 for never
	// Status and StatusOn are its status (Statuses) and the worker or person it names; none when ticked, or on
	// someone whose workers weren't read
	Status   string `json:"status"`
	StatusOn string `json:"status_on"`
	// Started is when the user asked to start it (dev todo start), unix seconds, and whom the start went to; StartLate
	// once no one took it up in time
	Started   *NeedStarted `json:"started"`
	StartLate bool         `json:"start_late"`
	Quiet     bool         `json:"quiet"` // a test item (dev todo add --quiet): counted nowhere
	// Column, Assignee, WaitingFor and NeedsYou are its card's place on the boards (todo.Placement)
	Column     string       `json:"column"`
	Assignee   NeedAssignee `json:"assignee"`
	WaitingFor string       `json:"waiting_for"`
	NeedsYou   bool         `json:"needs_you"`
	Doing      *NeedDoing   `json:"doing"`   // what its doer said it does now
	PartOf     int          `json:"part_of"` // its other half's id
}

// NeedStarted is when the user asked to start an item, unix seconds, and whom the start went to (todo.Started).
type NeedStarted struct {
	At int64  `json:"at"`
	To string `json:"to"`
}

// NeedDoing is what an item's doer said it does now (todo.Doing).
type NeedDoing struct {
	Text string `json:"text"`
}

// NeedAssignee is whose a card is (todo.Assignee): Kind AssigneeYou, AssigneeBoss or AssigneeWorker, Name its session.
type NeedAssignee struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Mine reports an open card the user's (assignee you).
func (it NeedItem) Mine() bool { return !it.Done && it.Assignee.Kind == AssigneeYou }

// NeedCounts are the lists' counts for the roll-ups (Subtree), by boss.
func NeedCounts(needs []NeedList) map[string]ListCount {
	out := map[string]ListCount{}
	for _, l := range needs {
		c := ListCount{Unread: l.Error != "", Closed: l.Closed}
		for _, it := range l.Items {
			if !it.Quiet {
				c.Add(it)
			}
		}
		out[l.Boss] = c
	}
	return out
}

// NeedKind is what a TO DO row is, in the order rows sort last within a group (after cardOrder).
type NeedKind int

const (
	NeedDialog  NeedKind = iota // a session's open dialog
	NeedAsk                     // a turn that ended on a question (an asks note)
	NeedListAsk                 // the user's card with options
	NeedPlain                   // the user's card without options
	NeedOn                      // a card not the user's: the boss's, a worker's, a person's
	NeedUnread                  // a list that wasn't read: Text says why
)

// NeedRow is one row of TO DO: a session waiting on a dialog or on its turn's question, an open list item, or a list
// not read.
type NeedRow struct {
	Kind    NeedKind
	Project string   // the session's or the list owner's project
	Who     string   // "boss" for a boss or its list, else the worker's name less its project
	Boss    string   // the boss whose list it is; "" for a session's row
	ID      int      // the list item's number
	Text    string   // the item's text, what the dialog or the question asks, or why the list wasn't read
	Options []string // a list ask's answers; a dialog's choices (the sidebar only shows them; a board answers them)
	Context []string // the list item's lines under its text
	Session string   // where ⏎ goes: the session, or the list's boss
	Updated int64    // a stale list's item's last change (its list's, for an older dev's item), unix seconds; 0 for the others
	Check   *Check   // why the list item may be out of date
	Closed  bool     // the list's boss session is closed: its row can't be answered or gone to, only ticked
	Until   int64    // when the list item ticks by itself, unix seconds; 0 for never
	On      string   // whom the list item is on, as filed
	// Status is the list item's (Statuses), none for a session's; StatusOn the worker or person it names
	Status   string
	StatusOn string
	// StartedAt is when the user asked to start the list item, unix seconds (0: not asked), StartedTo whom it went to;
	// StartLate once no one took it up
	StartedAt int64
	StartedTo string
	StartLate bool
	Quiet     bool  // a test item: Count leaves it out, its text shows after a TEST tag (Tag)
	Added     int64 // when the list item was added, unix seconds
	// Column, Assignee, WaitingFor and NeedsYou are the list item's card's place (NeedItem); Doing its doing text,
	// PartOf its other half's id
	Column     string
	Assignee   NeedAssignee
	WaitingFor string
	NeedsYou   bool
	Doing      string
	PartOf     int
}

// Tag is text after a muted TEST tag when quiet: a test item's, on every board.
func Tag(quiet bool, text string) string {
	if !quiet {
		return text
	}
	return muted + "TEST" + reset + " " + text
}

// Tally is what TO DO's rows count, as the boss pane's and the sidebar's headers say it (the web board's tally, plus
// the sessions): what needs the user (sessions waiting on them, cards marked needs you), the user's cards.
type Tally struct{ NeedYou, Mine int }

// MineWord is the user's cards' count's word: "N mine".
const MineWord = "mine"

// Count tallies rows, test items left out.
func Count(rows []NeedRow) Tally {
	var t Tally
	asking := map[string]bool{}
	for _, r := range rows {
		if r.Kind == NeedDialog || r.Kind == NeedAsk {
			asking[r.Session] = true
		}
	}
	for _, r := range rows {
		if r.Quiet {
			continue
		}
		// A blocked worker's card counts by its session's row, when it has one (not after a failed turn)
		if r.Kind == NeedDialog || r.Kind == NeedAsk || r.NeedsYou && !(r.Assignee.Kind == AssigneeWorker && asking[r.Assignee.Name]) {
			t.NeedYou++
		}
		if r.Mine() {
			t.Mine++
		}
	}
	return t
}

// card reports a list item's card: not a session's, nor a list not read.
func (r NeedRow) card() bool { return r.Boss != "" && r.Kind != NeedUnread }

// Mine reports a list item's card the user's (assignee you).
func (r NeedRow) Mine() bool { return r.card() && r.Assignee.Kind == AssigneeYou }

// Group is the row's block: -1 for a session's (Needs you, first), its column's index in Columns for a list item's
// (In progress for a column it doesn't know, as the web board), len(Columns) for a list not read, after them.
func (r NeedRow) Group() int {
	switch r.Kind {
	case NeedDialog, NeedAsk:
		return -1
	case NeedUnread:
		return len(Columns)
	}
	if i := slices.Index(Columns, r.Column); i >= 0 {
		return i
	}
	return slices.Index(Columns, ColumnProgress)
}

// Yours reports a row the user acts on: a session's, a list not read, or the user's card (Mine).
func (r NeedRow) Yours() bool { return !r.card() || r.Mine() }

// Tickable reports a list item d ticks (dev todo done): the user's card, or their own item asked to start, not an ask.
func (r NeedRow) Tickable() bool {
	return r.card() && len(r.Options) == 0 && (r.Mine() || r.ownStarted())
}

// Startable reports a list item s starts (dev todo start), as the web board offers it: a To do card or a late start,
// not an ask; on the manager's own list only the user's; a closed boss's only while the manager runs, which takes it.
func (r NeedRow) Startable(managerRuns bool) bool {
	return r.card() && len(r.Options) == 0 && (r.Boss != Manager || r.Mine() || r.ownStarted()) && (!r.Closed || managerRuns) &&
		(r.StartLate || r.Column == ColumnTodo)
}

// ownStarted reports the user's own item asked to start: its status says starting only then (todo.Own), whoever the
// card is now.
func (r NeedRow) ownStarted() bool { return r.Status == StatusStarting }

// ClosedHint is what a key on a closed boss's row says instead of acting.
func (r NeedRow) ClosedHint() string {
	return r.Boss + " is closed: dev boss " + r.Project + " reopens it with its list"
}

// MinTextAfterAge is the text width a row keeps beside its age mark; with less the mark is left off.
const MinTextAfterAge = 12

// Age is the row's mark at its right edge: its item's check (Check.Short), else how long ago the item of a stale list
// last changed ("18h 19m"); "" for any other row. Tone is its colour.
func (r NeedRow) Age(now time.Time) string {
	if r.Check != nil {
		return r.Check.Short(now)
	}
	if d := now.Sub(time.Unix(r.Updated, 0)); r.Updated != 0 && d > 0 {
		return AgeSpan(d)
	}
	return ""
}

// Tone is how the row's Age stands out: its check's, else ToneFaint.
func (r NeedRow) Tone() Tone {
	if r.Check == nil {
		return ToneFaint
	}
	return r.Check.Tone()
}

// Tone is a mark's colour, which each view maps to its own.
type Tone int

const (
	ToneFaint Tone = iota
	ToneFlag
	ToneAlert
)

// Colour is the tone's escape: faint, gold or red.
func (t Tone) Colour() string {
	return [...]string{faint, gold, red}[t]
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
		return "", "", fmt.Errorf("dev todo %s timed out after %s: check the list, it may have gone through", args[0], timeout)
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

// StartNeed asks for item id of boss's list to be started, as dev todo start does, only while it still reads text.
func StartNeed(boss string, id int, text string) (string, error) {
	out, warning, err := runTodo(answerTimeout, "start", "--from-board", "--question="+text, "--", boss, strconv.Itoa(id))
	return strings.TrimSpace(out + " " + warning), err
}

// ReopenNeed puts the user's ticked item id back on boss's list, as dev todo reopen does, only while it still reads text.
func ReopenNeed(boss string, id int, text string) (string, error) {
	out, warning, err := runTodo(answerTimeout, "reopen", "--from-board", "--question="+text, "--", boss, strconv.Itoa(id))
	return strings.TrimSpace(out + " " + warning), err
}

// DoingNeed marks the user's own item id of boss's list in progress with doing, or ends it (stop), as dev todo doing
// does, only while it still reads text.
func DoingNeed(boss string, id int, text, doing string, stop bool) (string, error) {
	args := []string{"doing", "--from-board", "--question=" + text}
	if stop {
		args = append(args, "--stop")
	}
	args = append(args, "--", boss, strconv.Itoa(id))
	if !stop && doing != "" {
		args = append(args, doing)
	}
	out, warning, err := runTodo(answerTimeout, args...)
	return strings.TrimSpace(out + " " + warning), err
}

// TickNeed ticks item id of boss's list, as dev todo done does, only while it still reads text.
func TickNeed(boss string, id int, text string) (string, error) {
	out, warning, err := runTodo(answerTimeout, "done", "--text="+text, "--boss", boss, strconv.Itoa(id))
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

// needRows are the lines of the sidebar's TO DO, the rows the user acts on (Yours): with a boss on the server, a row
// for what its boards show (Boards), then what no board shows (a session under no boss, a list whose boss is gone);
// else every row.
func (m Model) needRows() []NeedRow {
	all := m.yourRows("")
	boards := Boards(m.snap.sessions)
	if len(boards) == 0 {
		return all
	}
	shown := map[string]bool{}
	board := NeedRow{Kind: needBoard}
	for _, b := range boards {
		rows := m.yourRows(b)
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

// yourRows are NeedRows under root the user acts on (Yours): the sessions', then the user's cards needing them, then
// the rest in column order, the lists not read last.
func (m Model) yourRows(root string) []NeedRow {
	rows := slices.DeleteFunc(NeedRows(m.snap.sessions, m.needs, root), func(r NeedRow) bool { return !r.Yours() })
	slices.SortStableFunc(rows, func(a, b NeedRow) int {
		return cmp.Or(cmp.Compare(min(a.Group(), 0), min(b.Group(), 0)), cmp.Compare(boolRank(a.NeedsYou), boolRank(b.NeedsYou)))
	})
	return rows
}

// needCounts is the sidebar's TO DO header after its title: what its rows and the boards' count, "N need you" red,
// "N mine" muted; "" for none.
func (m Model) needCounts() string {
	t := Count(m.yourRows(""))
	var parts []string
	add := func(n int, colour, word string) {
		if n > 0 {
			parts = append(parts, colour+fmt.Sprintf("%d %s", n, word)+reset)
		}
	}
	add(t.NeedYou, red, NeedYouWord)
	add(t.Mine, muted, MineWord)
	return strings.Join(parts, muted+" · "+reset)
}

// gotoBoard leaves TO DO for board's TO DO.
func (m Model) gotoBoard(board string) (tea.Model, tea.Cmd) {
	c, client := m.c, m.client()
	from := Origin{Session: m.snap.current, Pane: m.pane}
	m.needMode = false
	return m, m.runAction(client, func() error { return focusBoard(c, client, board, from) })
}

// focusBoard switches client to board's session and focuses its previews pane, asking it to focus TO DO
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

// NeedRows are the rows of TO DO from sessions in Order and the bosses' lists, sorted by group (Group), the
// web board's order within a column (cardOrder) and kind (NeedKind), in sessions' and lists' order within one: each session waiting on a dialog or on
// the question its turn ended on, each open item, each list not read. root scopes them to its board: the sessions
// under it (its own question is on its screen) and its and their lists; "" takes them all.
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
		// A closed boss is under no board: the manager's shows its list
		if root != "" && !owners[l.Boss] && !(l.Closed && root == Manager) {
			continue
		}
		project, ok := project[l.Boss]
		if !ok {
			project = strings.TrimPrefix(l.Boss, "boss-")
		}
		row := NeedRow{Project: project, Who: "boss", Boss: l.Boss, Session: l.Boss, Closed: l.Closed}
		if l.Error != "" {
			row.Kind, row.Text = NeedUnread, l.Error
			rows = append(rows, row)
		}
		for _, it := range l.Items {
			if it.Done {
				continue
			}
			row.Kind, row.ID, row.Text, row.Options, row.Check, row.Updated = NeedPlain, it.ID, it.Text, it.Options, it.Check, 0
			row.Until, row.On, row.Status, row.StatusOn, row.Context = it.Until, it.On, it.Status, it.StatusOn, it.Context
			row.StartedAt, row.StartedTo, row.StartLate, row.Quiet, row.Added = 0, "", it.StartLate, it.Quiet, it.Added
			row.Column, row.Assignee, row.WaitingFor, row.NeedsYou, row.Doing, row.PartOf = it.Column, it.Assignee, it.WaitingFor, it.NeedsYou, "", it.PartOf
			if it.Started != nil {
				row.StartedAt, row.StartedTo = it.Started.At, it.Started.To
			}
			if it.Doing != nil {
				row.Doing = it.Doing.Text
			}
			if l.Stale {
				row.Updated = cmp.Or(it.Added, l.Updated)
			}
			switch {
			case !it.Mine():
				row.Kind = NeedOn
			case len(it.Options) > 0:
				row.Kind = NeedListAsk
			}
			rows = append(rows, row)
		}
	}
	slices.SortStableFunc(rows, func(a, b NeedRow) int {
		return cmp.Or(cmp.Compare(a.Group(), b.Group()), cardOrder(a, b), cmp.Compare(a.Kind, b.Kind))
	})
	return rows
}

// cardOrder orders cards within a column as the web board's lanes do: what needs the user first, then in To do the
// user's, then the soonest due (Until), then the oldest.
func cardOrder(a, b NeedRow) int {
	first := func(x, y bool) int { return cmp.Compare(boolRank(x), boolRank(y)) }
	return cmp.Or(first(a.NeedsYou, b.NeedsYou), first(a.Column == ColumnTodo && a.Mine(), b.Column == ColumnTodo && b.Mine()),
		first(a.Until != 0, b.Until != 0), cmp.Compare(a.Until, b.Until), cmp.Compare(a.Added, b.Added))
}

// boolRank sorts true first.
func boolRank(b bool) int {
	if b {
		return 0
	}
	return 1
}

// Boards are the bosses whose boards show TO DO for the sessions under them: a manager, and each top-level boss
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
// open ask, d ticks the user's card (Tickable), space opens a list item in its popup (dev todo show), b the board (dev todo board),
// esc/q leaves.
func (m Model) handleNeedsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.needRows()
	if m.needIdx >= len(rows) || rows[m.needIdx].key() != m.needKey {
		m.needMode, m.needOpen = false, false
		return m, nil
	}
	row := rows[m.needIdx]
	switch s := k.String(); {
	case row.Closed && (s == "enter" || len(s) == 1 && s >= "1" && s <= "9"):
		showMessage(m.c, m.client(), row.ClosedHint())
		return m, nil
	}
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
		// Before m is read: the quit marks its reload
		quit := m.reload.Quit()
		return m, quit
	case " ":
		if row.Boss == "" || row.Kind == NeedUnread || row.Kind == needBoard {
			showMessage(m.c, m.client(), "space opens a list item")
			return m, nil
		}
		client, open := m.client(), m.openItem
		return m, m.runAction(client, func() error { return open(client, row.Boss, row.ID, row.Text) })
	case "b":
		client, open := m.client(), m.openBoard
		return m, m.runAction(client, func() error { return open(client, nil) })
	case "d":
		if !row.Tickable() {
			showMessage(m.c, m.client(), "d ticks your own item, not an ask")
		}
		if m.answering || !row.Tickable() || row.key() == m.ticked {
			return m, nil
		}
		m.answering, m.ticked = true, row.key()
		client, tick := m.client(), m.needsDone
		return m, func() tea.Msg {
			out, err := tick(row.Boss, row.ID, row.Text)
			return answeredMsg{out: out, err: err, client: client}
		}
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
		return m, m.runAction(client, func() error { return focusContent(c, client, current) })
	}
	from := Origin{Session: current, Pane: m.pane}
	return m, m.runAction(client, func() error { return switchTo(c, client, row.Session, from) })
}

func (m Model) enterNeeds() Model {
	if rows := m.needRows(); len(rows) > 0 && m.snap.height >= needsMinHeight {
		m.needMode, m.needOpen, m.needIdx, m.needKey = true, false, 0, rows[0].key()
	} else {
		showMessage(m.c, m.client(), "Nothing to do")
	}
	return m
}

// SplitOption is a list ask's option o in parts (todo.Split): its label, the detail after " | ", and whether it ends
// " (Recommended)", which may end the label or the detail.
func SplitOption(o string) (label, detail string, rec bool) {
	rest, rec := cutRecommended(o)
	// Padded: an empty label or detail, cleaned to "| x" or "x |", still splits
	label, detail, _ = strings.Cut(" "+rest+" ", " | ")
	label, recLabel := cutRecommended(strings.TrimSpace(label))
	return label, strings.TrimSpace(detail), rec || recLabel
}

// cutRecommended is s without its ending " (Recommended)", case aside; rec reports it had one.
func cutRecommended(s string) (string, bool) {
	const suffix = "(recommended)"
	if n := len(s) - len(suffix); n >= 0 && strings.EqualFold(s[n:], suffix) {
		return strings.TrimSpace(s[:n]), true
	}
	return s, false
}

// optionLabel is option o as shown: a list item's label only (SplitOption), a session's choice without its
// " (Recommended)"; rec reports it had one.
func optionLabel(listItem bool, o string) (label string, rec bool) {
	if listItem {
		label, _, rec = SplitOption(o)
		return label, rec
	}
	return cutRecommended(o)
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
	head := " " + bold + "TO DO" + reset
	if counts := m.needCounts(); counts != "" {
		head += "  " + counts
	}
	if m.needsErr != nil {
		head += muted + " (stale)" + reset
	}
	if len(rows) == 1 && rows[0].Kind == needBoard {
		// All on the boards: one line pointing there
		line := truncate(head+faint+"  → board"+reset, w)
		return []string{cardLine(line, ansi.StringWidth(line), w, m.needMode), ""}, []needHit{{line: 0}}
	}
	if m.snap.focused && !m.needMode && ansi.StringWidth(head)+4 <= w {
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
	case NeedAsk, NeedListAsk:
		mark = gold + bold + "?" + reset
	case NeedUnread:
		mark = gold + "⚠" + reset
	case needBoard:
		mark = faint + "→" + reset
	}
	vis := 1 + ansi.StringWidth(project) + 2 + 1 + 1
	room, age := max(1, w-vis-1), r.Age(time.Now())
	if age != "" && room-len(age)-1 >= MinTextAfterAge {
		room -= len(age) + 1
	} else {
		age = ""
	}
	text := truncate(Tag(r.Quiet, r.Text), room)
	line := " " + muted + project + reset + "  " + mark + " " + text
	width := vis + ansi.StringWidth(text)
	if age != "" {
		pad := room - ansi.StringWidth(text) + 1
		line += strings.Repeat(" ", pad) + r.Tone().Colour() + age + reset
		width += pad + len(age)
	}
	return cardLine(line, width, w, selected)
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
	r.Updated = 0 // its age mark belongs to the one-line row
	lines = []string{m.needLine(r, w, true)}
	if text := strings.Split(ansi.Wrap(Tag(r.Quiet, r.Text), max(1, w-vis-1), ""), "\n"); len(text) > 1 {
		first := r
		// Its tag is in the wrapped text already
		first.Text, first.Quiet = text[0], false
		lines = []string{m.needLine(first, w, true)}
		for _, t := range text[1:] {
			lines = append(lines, cardLine(strings.Repeat(" ", vis)+t, vis+ansi.StringWidth(t), w, true))
		}
	}
	opts = make([]int, len(lines))
	labels := make([]string, len(r.Options))
	for i, o := range r.Options {
		labels[i], _ = optionLabel(r.Boss != "", o)
	}
	for i, o := range r.Options {
		n := 0
		label, rec := optionLabel(r.Boss != "", o)
		if strings.Count("\x00"+strings.Join(labels, "\x00")+"\x00", "\x00"+label+"\x00") > 1 {
			// An older ask's options sharing a label: shown whole, to tell them apart
			label, _ = cutRecommended(o)
		}
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
		return " " + faint + "⏎ board · esc leave" + reset
	case m.needOpen && ask:
		return " " + faint + "1-9 answer · esc back" + reset
	case m.needOpen:
		return " " + faint + "⏎ go · esc back" + reset
	case ask:
		hint = "j/k ⏎ options"
	case row.Tickable():
		hint = "d tick (stays till ticked)"
	default:
		hint = "j/k ⏎ expand"
	}
	// The popups' keys, each where it fits whole
	keys := []string{"b board"}
	if row.Boss != "" && row.Kind != NeedUnread {
		keys = append(keys, "space open")
	}
	for _, k := range keys {
		if ansi.StringWidth(hint+" · "+k+" · esc leave") <= m.snap.width-1 {
			hint += " · " + k
		}
	}
	return " " + faint + hint + " · esc leave" + reset
}
