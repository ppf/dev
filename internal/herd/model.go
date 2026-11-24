package herd

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/bossclear"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

// pollInterval is how often the previews refresh while on screen.
const pollInterval = time.Second

// row is one list-panes -a row: agent.Pane's fields, the session options and the window's focus.
type row struct {
	Session      string `tmux:"session_name"`
	SessionID    string `tmux:"session_id"`
	Window       int    `tmux:"window_index"`
	WindowActive bool   `tmux:"window_active"`
	Active       bool   `tmux:"pane_active"`
	ID           string `tmux:"pane_id"`
	Path         string `tmux:"pane_current_path"`
	Title        string `tmux:"pane_title"`
	Command      string `tmux:"pane_current_command"`
	State        string `tmux:"@agent_state"`
	StateCmd     string `tmux:"@agent_cmd"`
	Cost         string `tmux:"@agent_cost"`
	Project      string `tmux:"@project"`
	Sidebar      string `tmux:"@sidebar_pane"`
	Herd         string `tmux:"@dev_herd"`
	StartedBy    string `tmux:"@dev_started_by"`
	WaitingFor   string `tmux:"@agent_waiting_for"`
	CostOpt      string `tmux:"@dev_cost"`
	PR           string `tmux:"@dev_pr"`
	PRGen        string `tmux:"@dev_pr_gen"`
	Question     string `tmux:"@agent_question"`
	Note         string `tmux:"@dev_boss_note"`
	Transcript   string `tmux:"@agent_transcript"`
	Context      string `tmux:"@agent_context"`
	Since        string `tmux:"@agent_since"`
	Zoomed       bool   `tmux:"window_zoomed_flag"`
	Model        string `tmux:"@dev_model"`
	Effort       string `tmux:"@dev_effort"`
	WaitUntil    string `tmux:"@dev_wait_until"`
	WaitReason   string `tmux:"@dev_wait_reason"`
	ResumeAt     string `tmux:"@dev_resume_at"`
	NeedsFocus   string `tmux:"@dev_needs_focus"`
	// The key of its spent ledger, and the agent pane's conversation
	Socket       string `tmux:"socket_path"`
	Created      string `tmux:"session_created"`
	Conversation string `tmux:"@agent_session"`
	InboxOff     string `tmux:"@dev_boss_inbox"`      // inbox.OptOff
	Outside      string `tmux:"@dev_started_outside"` // OptStartedOutside
}

// tile is one worker session's preview.
type tile struct {
	sess     sidebar.Session
	pane     string          // the agent pane
	deciding bool            // the agent waits on the user's decision, see agent.Deciding
	question agent.Question  // @agent_question, what its dialog asks
	body     []string        // the pane's last screen lines; nil when not captured this tick
	pick     *pick           // the dialog a deciding agent shows; nil when none or not captured
	note     string          // its latest boss inbox note (inbox.OptNote)
	since    time.Time       // when its state began (@agent_since); zero when unknown
	context  string          // @agent_context, its context window's use in whole percent; "" when unknown
	model    string          // @dev_model, the --model its Claude, Codex or agy started with; "" when none was passed
	effort   string          // @dev_effort, the --effort its Claude, Codex or agy started with
	tail     desktop.Tail    // its transcript's end; zero when not read this refresh (boss hidden), for an agent without one, or none written yet
	tailErr  error           // why its transcript could not be read
	ci       ci              // its open or draft PR's checks as last read; zero before a read, or when not looked up this refresh
	via      string          // for a session one of this boss's bosses started (deeper too), the boss that did: shown only while it needs the user, closed only by that boss; "" for this boss's own
	roll     *sidebar.Rollup // a boss's whole subtree (sidebar.Subtree), which places its card; nil for a worker or a deeper session

	// bossAnswers: a plain worker whose boss runs, so its asks note is the boss's to answer (sidebar.Session.AsksBoss)
	bossAnswers bool
}

// snapshot is what one refresh saw.
type snapshot struct {
	tiles   []tile
	started map[string]bool   // sessions this boss started, an agent in them or not, by name
	bosses  []string          // the bosses under this one, at any depth, by name
	asks    []todo.BossList   // their lists' open items (on no one or on others), by boss; none without items or an error
	asksErr error             // why their lists could not be read
	all     []sidebar.Session // every session on the server in sidebar.Order, for the bosses' roll-ups
	self    string            // own session's id
	name    string            // own session's name
	visible bool              // a client shows own window
	focused bool              // own pane is its window's active pane
	zoomed  bool              // own window is zoomed
	// The sidebar asked to focus TO DO (sidebar.OptNeedsFocus), cleared as read
	needsFocus bool
	client     string // tty of the client viewing own session; "" lets tmux pick
	todo       todo.List
	todoErr    error          // why the to do list could not be read
	threads    []inbox.Thread // own open message threads (openThreads)
	msgsErr    error          // why the messages could not be read
}

// Model is the Bubble Tea previews pane.
type Model struct {
	c             *tmux.Client
	pane          string
	look          lookups
	snap          snapshot
	selected      string // session name
	width, height int
	err           error
	confirm       string  // session waiting for y to be killed
	msg           message // one-off message on the bottom row, until the next key
	box           replyBox
	drafts        map[string]replyBox // by session: a reply typed when focus moved away, back when its box opens again
	focused       bool                // own pane is its window's active pane, as of the last input or refresh
	inputs        int                 // keys and clicks seen, so a refresh listed before one doesn't undo its focus
	answering     string              // the session an answer's keys go to, until its outcome; one at a time
	history       []string            // replies sent, oldest first
	clk           control.Clock
	host          string          // hostname, tmux's default pane title: a title that says nothing
	folds         map[string]bool // by session: the user's fold of its card, over the default (see tile.folded)
	expands       map[string]bool // by session: its card shows more of its screen (v)
	size          paneSize        // how fit last sized the pane
	restoredSize  paneSize        // size as a reload carried it, taken at the first refresh; unlisted when none
	restored      bool            // carried by a reload: until the first good refresh, no card is new to Asking
	restoredNeed  *needRestore    // TO DO mode's cursor as a reload carried it, taken at the first good refresh
	todo          *todo.Store     // the boss's to do list; nil reads none, unless findTodo
	findTodo      bool            // refresh looks up todo until it's found
	todoFolded    bool            // TO DO shows as its header only (t)
	waitOpen      bool            // TO DO lists Waiting on others' rows, not only their fold row
	msgs          *inbox.Messages // the tmux server's messages; nil reads none, unless findMsgs
	findMsgs      bool            // refresh looks up msgs until it's found
	// TO DO
	needMode   bool                                                               // its keys act on it
	needIdx    int                                                                // the selected row, clamped to the rows at each use
	needKey    string                                                             // the selected row's sig when chosen: keys act only while it's the same; "" after an answer
	needDone   string                                                             // the answered row's sig: the cursor waits on it until it goes
	needFor    string                                                             // the session or boss needDone's answer went to
	answerList func(boss string, id int, question, option string) (string, error) // dev todo answer
	tickList   func(boss string, id int, text string) (string, error)             // dev todo done
	startList  func(boss string, id int, text string) (string, error)             // dev todo start
	openItem   func(client, boss string, id int, text string) error               // dev todo show
	openBoard  func(client string, bosses []string) error                         // dev todo board
	reload     selfreload.Pane                                                    // a held Ready waits for idle (see idle)
}

// paneSize is how fit last sized the previews pane.
type paneSize int

const (
	unlisted  paneSize = iota // before the first refresh, which tells fit the workers
	unsized                   // not yet, or that fit failed or a resize undid it
	collapsed                 // collapsedW
	shared                    // its share of the window
)

// lookups is what refresh asks sidebar.Lookups; tests replace it.
type lookups interface {
	Branch(path string) string
	PR(pane, stored, gen, path, branch string) sidebar.PR
	OpenPR(url string) error
	Tail(path string) (desktop.Tail, error)
	CI(url, pane, gen string) ci
}

type tickMsg struct{}

// watchDueMsg runs the inbox watch (stuck workers, deferred delivery) and the clear offer, every watchInterval.
type watchDueMsg struct{}

// watchMsg is the inbox watch's outcome.
type watchMsg struct{ err error }

const watchInterval = 15 * time.Second

type refreshMsg struct {
	snap   snapshot
	todo   *todo.Store     // the list found this refresh
	msgs   *inbox.Messages // the messages found this refresh
	inputs int             // m.inputs when the refresh started
	err    error
}

// newModel is the previews in pane.
func newModel(c *tmux.Client, pane string) Model {
	// Without it only a title naming the host shows
	host, _ := os.Hostname()
	return Model{c: c, pane: pane, look: newLookups(c), clk: control.RealClock, host: host, findTodo: true, findMsgs: true,
		answerList: sidebar.AnswerNeed, tickList: sidebar.TickNeed, startList: sidebar.StartNeed, openItem: sidebar.OpenItem, openBoard: sidebar.OpenBoard,
		reload: selfreload.NewPane(selfreload.Version())}
}

// Init starts the first refresh, which sizes the pane (see fit).
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.watchLater(), m.reload.Init())
}

// fit sizes the pane once the first refresh is in: collapsed while it has nothing to show, else its share of the
// window; only when that changes, so a pane the user resized keeps its width while it shows something. A
// collapsed pane hands the focus back to the coordinator; a zoomed window waits, as resizing would unzoom it.
// It runs in Update, so fits apply in order.
func (m *Model) fit() {
	want := shared
	if len(m.snap.tiles) == 0 && len(m.snap.todo.Items) == 0 && m.snap.todoErr == nil && len(m.snap.asks) == 0 && m.snap.asksErr == nil && len(m.snap.threads) == 0 && m.snap.msgsErr == nil && m.err == nil && !m.msg.alert && m.box.session == "" && m.confirm == "" {
		want = collapsed
	}
	if m.size == unlisted || m.snap.zoomed || m.size == want && (want == shared || !m.focused) {
		return
	}
	var err error
	if want == collapsed {
		err = collapse(m.c, m.pane, m.focused)
		m.focused = false
	} else {
		err = fitWidth(m.c, m.pane)
	}
	if m.size = want; err != nil {
		// Shown at the pane's share, tried again on the next message
		m.size, m.msg = unsized, alert("❌ "+err.Error())
	}
}

// watchLater runs inbox.Watch and bossclear.Offer after watchInterval.
func (m Model) watchLater() tea.Cmd {
	return tea.Tick(watchInterval, func(time.Time) tea.Msg { return watchDueMsg{} })
}

// Update routes refreshes, size and input, then fits the pane.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	n := next.(Model)
	n.fit()
	return n, cmd
}

// update is Update before the fit.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	msg = selfreload.Done(msg)
	// Quitting to reload: tea.Quit lands later, and anything started meanwhile would be cut off by the exec
	if m.reload.Quitting() {
		return m, nil
	}
	if cmd, ok := m.reload.Update(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tickMsg:
		return m, m.refresh()
	case refreshMsg:
		if m.size == unlisted {
			// A reload's carried size: no refit of a width the user dragged, no focus handed back
			m.size = cmp.Or(m.restoredSize, unsized)
		}
		if msg.err != nil {
			m.err = msg.err
		} else {
			// A card entering Asking shows screenRows of its screen again, where it asks
			for _, t := range msg.snap.tiles {
				was := slices.IndexFunc(m.snap.tiles, func(u tile) bool { return u.sess.Name == t.sess.Name })
				if t.section() == needsYouSection && !m.restored && (was < 0 || m.snap.tiles[was].section() != needsYouSection) {
					delete(m.expands, t.sess.Name)
				}
			}
			m.snap, m.err, m.restored = msg.snap, nil, false
			if msg.msgs != nil {
				m.msgs = msg.msgs
			}
			if msg.todo != nil {
				m.todo = msg.todo
			}
			if n := m.restoredNeed; n != nil {
				// Only now has TO DO rows: before, fixNeeds would have left it
				m.needMode, m.needIdx, m.needKey, m.needDone, m.todoFolded = true, n.Idx, m.needSig(n.Key), m.needSig(n.Done), false
				m.restoredNeed = nil
			}
			if msg.inputs == m.inputs {
				m.focused = msg.snap.focused
			}
			// An answer going out or still on screen keeps its picker, so it isn't answered twice
			if !m.focused && m.box.session != "" && m.answering != m.box.session && m.box.sent == "" {
				m.blur()
			}
			m.selected = m.validSelection()
			m.syncPick()
			if msg.snap.needsFocus {
				m = m.enterNeeds()
			}
			m.fixNeeds()
			for _, byName := range []map[string]bool{m.folds, m.expands} {
				for name := range byName {
					if !slices.ContainsFunc(m.snap.tiles, func(t tile) bool { return t.sess.Name == name }) {
						// A new session of that name starts from its section's default
						delete(byName, name)
					}
				}
			}
		}
		// Only after a good refresh is applied: one in flight may carry (and has cleared) the sidebar's ask to focus
		// TO DO, and a failed one leaves the sessions unread, so carry would drop every kept reply
		if msg.err == nil && m.reload.Held() != "" {
			if quit := m.reload.Reload(m.idle()); quit != nil {
				return m, quit
			}
		}
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A window resize spreads its columns over every pane, a collapsed one too; a user's drag is undone too
		if m.size == collapsed && msg.Width > collapsedW {
			m.size = unsized
		}
		if m.size == unlisted && msg.Width > collapsedW && m.restoredSize == collapsed {
			// Before a reload's first refresh: the carried collapse is undone too
			m.restoredSize = unsized
		}
		m.fixNeeds()
		return m, nil
	case tea.KeyMsg:
		m.focused, m.inputs, m.reload.Err = true, m.inputs+1, nil
		return m.handleKey(msg)
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// tmux focuses a clicked pane before passing the click on
			m.focused, m.inputs, m.reload.Err = true, m.inputs+1, nil
		}
		return m.handleMouse(msg)
	case actionMsg:
		m.msg = msg.msg
		if msg.msg.alert && m.needDone != "" {
			// A reply from TO DO not sent: its row takes keys again
			m.needKey, m.needDone = m.needDone, ""
		}
		return m, nil
	case answeredMsg:
		m.msg, m.answering = msg.msg, ""
		switch {
		case m.box.block && msg.session == m.box.session && (!msg.took || !m.multiPick()):
			// TO DO's answer is done, or failed: its keys go back to the block, which shows the row as it is now
			m.blur()
		case !msg.took && msg.session == m.box.session:
			m.box.sent = ""
		}
		if !msg.took && m.needDone != "" && msg.session == m.needFor {
			// Not answered: its row takes keys again
			m.needKey, m.needDone = m.needDone, ""
		}
		return m, nil
	case watchDueMsg:
		c, pane := m.c, m.pane
		return m, m.reload.Busy.Run(func() tea.Msg {
			return watchMsg{err: errors.Join(inbox.Watch(c, control.RealClock, pane), bossclear.Offer(c, control.RealClock, pane))}
		})
	case watchMsg:
		if msg.err != nil {
			m.msg = alert("❌ inbox: " + msg.err.Error())
		}
		return m, m.watchLater()
	}
	return m, nil
}

// validSelection keeps the selection while its session is listed, else picks the first tile.
func (m Model) validSelection() string {
	for _, t := range m.snap.tiles {
		if t.sess.Name == m.selected {
			return m.selected
		}
	}
	if len(m.snap.tiles) > 0 {
		return m.snap.tiles[0].sess.Name
	}
	return ""
}

// refresh lists panes and clients (two tmux calls), and while a client shows this window captures the open
// cards' panes and the deciding and asking ones', for their dialogs (one call each, at most maxCaptures, the
// selected card's first).
func (m Model) refresh() tea.Cmd {
	selected, inputs, host := m.selected, m.inputs, m.host
	folds := maps.Clone(m.folds)
	return func() tea.Msg {
		rows, err := tmux.Query[row](m.c, "list-panes", "-a")
		if err != nil {
			return refreshMsg{err: err}
		}
		clients, err := tmux.Query[sidebar.Client](m.c, "list-clients")
		if err != nil {
			return refreshMsg{err: err}
		}
		snap, err := buildSnapshot(rows, clients, m.pane, host, m.look)
		if err != nil {
			return refreshMsg{err: err}
		}
		if snap.needsFocus {
			if _, err := m.c.Run("set-option", "-pu", "-t", m.pane, sidebar.OptNeedsFocus); err != nil {
				return refreshMsg{err: err}
			}
		}
		// Looked up here, not at start: a restored boss's previews start before its session is marked one
		store := m.todo
		if store == nil && m.findTodo {
			if s, err := todo.ForPane(m.c, m.pane); err != nil {
				if !errors.Is(err, todo.ErrNotBoss) {
					snap.todoErr = err
				}
			} else {
				store = &s
			}
		}
		if store != nil {
			snap.todo, snap.todoErr = store.Read(m.clk.Now())
		}
		if manager := snap.name == inbox.Manager; len(snap.bosses) > 0 || manager {
			snap.asks, snap.asksErr = bossLists(m.c, snap.bosses, manager, m.clk.Now())
		}
		// Before the captures, which skip folded cards; again after them
		snap.rollUp()
		msgs := m.msgs
		if msgs == nil && m.findMsgs {
			if _, socket, err := inbox.Viewer(m.c, m.pane); err != nil {
				snap.msgsErr = err
			} else if s, err := inbox.OpenMessages(socket); err != nil {
				snap.msgsErr = err
			} else {
				msgs = &s
			}
		}
		if msgs != nil {
			var ts []inbox.Thread
			ts, snap.msgsErr = msgs.Threads(snap.name, m.clk.Now())
			snap.threads = openThreads(ts, m.clk.Now())
		}
		if !snap.visible {
			snap.rollUp()
			snap.tiles = slices.DeleteFunc(snap.tiles, offBoard)
			return refreshMsg{snap: snap, todo: store, msgs: msgs, inputs: inputs}
		}
		for _, i := range captures(snap.tiles, selected, folds) {
			out, err := m.c.Run("capture-pane", "-p", "-e", "-t", snap.tiles[i].pane)
			if err != nil {
				// The pane may have closed since list-panes; its card goes with the next refresh
				snap.tiles[i].body = []string{err.Error()}
				continue
			}
			t := &snap.tiles[i]
			if t.deciding || t.asking() {
				d, ok := parseDialog(out)
				var p pick
				if ok {
					p = newPick(d, t.question, ansi.Strip(out))
				}
				switch {
				// A deciding tile's dialog, or the question an asking tile's hook recorded: a decision before its Notification records it
				case ok && (t.deciding || p.cur >= 0):
					t.pick, t.deciding, t.sess.State = &p, true, agent.Waiting
				case t.deciding:
					// Its dialog may have been answered No or Esc, which fires no hook
					st, err := control.ReadChecked(m.c, t.pane)
					if err != nil {
						t.body = []string{err.Error()}
						continue
					}
					t.deciding = agent.Deciding(st.Trusted(), st.WaitingFor)
					t.sess.State = st.Trusted()
				}
			}
			t.body = previewLines(out, *t)
			if p := t.pick; p != nil {
				// Its card shows the dialog itself, from its tabs or title on: what it shows above them (a command, a
				// file's contents, a folder, the answers to review) stays on screen
				cut := p.d.titleAt
				if len(p.d.tabs) > 0 {
					cut = p.d.top
				}
				t.body = trimBlank(t.body[:min(cut, len(t.body))])
			}
		}
		// A dialog found gone moves its card out of Asking, a deeper session's off the board
		snap.rollUp()
		snap.tiles = slices.DeleteFunc(snap.tiles, offBoard)
		return refreshMsg{snap: snap, todo: store, msgs: msgs, inputs: inputs}
	}
}

// offBoard reports a deeper session's card that doesn't need the user, as captured: shown only while it does.
func offBoard(t tile) bool { return t.via != "" && t.section() != needsYouSection }

// maxCaptures caps the captures per refresh.
const maxCaptures = 12

// captures are the indexes of the tiles whose panes a refresh captures: the selected one when open, then the
// deciding (or asking) ones, then the other open ones nearest the selected one (in view, as the board scrolls
// to it), at most maxCaptures.
func captures(tiles []tile, selected string, folds map[string]bool) []int {
	var first, deciding, open []int
	sel := index(tiles, selected)
	for i, t := range tiles {
		switch {
		case t.sess.Name == selected && (t.deciding || t.asking() || !t.folded(folds)):
			first = append(first, i)
		case t.deciding || t.asking():
			deciding = append(deciding, i)
		case !t.folded(folds):
			open = append(open, i)
		}
	}
	slices.SortStableFunc(open, func(a, b int) int { return cmp.Compare(abs(a-sel), abs(b-sel)) })
	all := slices.Concat(first, deciding, open)
	return all[:min(len(all), maxCaptures)]
}

func abs(n int) int { return max(n, -n) }

// asking reports a working agent whose AskUserQuestion hook recorded its question: its dialog shows
// before the Notification that makes it a decision. The record stays until its turn ends, so only the
// question on screen says it's open.
func (t tile) asking() bool {
	return t.sess.State == agent.Working && t.question.Tool == agent.AskUserQuestion
}

// sortBySection puts tiles in section order, keeping their order within one.
func sortBySection(tiles []tile) {
	slices.SortStableFunc(tiles, func(a, b tile) int { return cmp.Compare(a.section(), b.section()) })
}

// buildSnapshot turns the rows into tiles by section, in sidebar order within one, keeping only agent sessions
// this boss started, bosses too, then (after them) those its bosses started, deeper too, while they need the user
// or ask (see offBoard). PRs, their checks and transcripts are only read while visible.
func buildSnapshot(rows []row, clients []sidebar.Client, pane, host string, look lookups) (snapshot, error) {
	var s snapshot
	own := false
	var names []string
	panes := map[string][]row{}
	for _, r := range rows {
		if r.ID == pane {
			s.self, s.name, s.focused, s.zoomed, own = r.SessionID, r.Session, r.Active, r.Zoomed, true
			s.needsFocus = r.NeedsFocus != ""
			_, viewed := sidebar.ActiveClient(clients, r.Session)
			s.visible = viewed && r.WindowActive
		}
		if _, seen := panes[r.Session]; !seen {
			names = append(names, r.Session)
		}
		panes[r.Session] = append(panes[r.Session], r)
	}
	if !own {
		return snapshot{}, fmt.Errorf("boss: pane %s not listed", pane)
	}
	c, _ := sidebar.ActiveClient(clients, s.name)
	s.client = c.TTY
	var sessions, deeper []sidebar.Session
	created, socket := map[string]string{}, ""
	meta := map[string]tile{}
	under := descendants(names, panes, s.self, s.name)
	// Who runs as a boss, for each session's BossRuns
	bosses := make([]sidebar.Session, 0, len(names))
	for _, name := range names {
		r := panes[name][0]
		b := sidebar.Session{Name: name, ID: r.SessionID, Boss: r.Herd != "", InboxOff: r.InboxOff == "off"}
		if a, ok := agent.Detect(agentPanes(panes[name]), r.Sidebar); ok {
			b.Agent = a.Label
		}
		bosses = append(bosses, b)
	}
	for _, name := range names {
		rs := panes[name]
		via, deep := under[name]
		mine := sidebar.Names(rs[0].StartedBy, s.self, s.name, true)
		if rs[0].Herd != "" && (mine || deep) {
			s.bosses = append(s.bosses, name)
		}
		if mine {
			if s.started == nil {
				s.started = map[string]bool{}
			}
			s.started[name] = true
		}
		a, ok := agent.Detect(agentPanes(rs), rs[0].Sidebar)
		// As the sidebar reads it (its buildSnapshot), so the roll-ups agree
		full := sidebar.Session{Name: name, Group: sidebar.Group(name, rs[0].Project), ID: rs[0].SessionID, Boss: rs[0].Herd != "", StartedBy: rs[0].StartedBy,
			BossRuns: rs[0].Outside == "" && sidebar.BossRuns(rs[0].StartedBy, bosses)}
		created[rs[0].SessionID], socket = rs[0].Created, rs[0].Socket
		var r row
		if ok {
			r = rs[slices.IndexFunc(rs, func(r row) bool { return r.ID == a.Pane.ID })]
			full.Agent, full.State, full.CostUSD, full.HasCost = a.Label, a.State, a.Cost, a.CostOK
			full.Conversation = r.Conversation
			full.Deciding, full.Note, full.Resume = agent.Deciding(a.State, r.WaitingFor), r.Note, r.ResumeAt != ""
			full.Question = r.Question
			full.Wait = wait.FromOptions(r.WaitUntil, r.WaitReason)
		}
		s.all = append(s.all, full)
		if !ok || !mine && !deep {
			continue
		}
		t := tile{pane: a.Pane.ID, deciding: agent.Deciding(a.State, r.WaitingFor), note: r.Note, context: r.Context, model: r.Model, effort: r.Effort, via: via,
			bossAnswers: !full.Boss && full.BossRuns && !repeated(r.Note)}
		t.sess = sidebar.Session{State: a.State, Wait: wait.FromOptions(r.WaitUntil, r.WaitReason)}
		t.question, _ = agent.ParseQuestion(r.Question)
		// An asking one's capture tells (see refresh)
		if deep && t.section() != needsYouSection && !t.asking() {
			continue
		}
		sess := sidebar.Session{Name: name, Group: sidebar.Group(name, rs[0].Project), Agent: a.Label, State: a.State,
			Branch: look.Branch(a.Pane.Path), Title: sidebar.PaneTitle(a.Label, a.Pane.Title, name, host), Wait: t.sess.Wait}
		if a.CostOK && !sidebar.CostHidden(rs[0].CostOpt) {
			sess.Cost = sidebar.FormatCost(a.Cost)
		}
		if sec, err := strconv.ParseInt(r.Since, 10, 64); err == nil {
			t.since = time.Unix(sec, 0)
		}
		if s.visible {
			sess.PR = look.PR(a.Pane.ID, r.PR, r.PRGen, a.Pane.Path, sess.Branch)
			if pr := sess.PR; pr.URL != "" && (pr.State == "open" || pr.State == "draft") {
				t.ci = look.CI(pr.URL, a.Pane.ID, r.PRGen)
			}
			if r.Transcript != "" {
				t.tail, t.tailErr = look.Tail(r.Transcript)
			}
		}
		if deep {
			deeper = append(deeper, sess)
		} else {
			sessions = append(sessions, sess)
		}
		meta[name] = t
	}
	for _, sess := range slices.Concat(sidebar.Order(sessions), sidebar.Order(deeper)) {
		t := meta[sess.Name]
		t.sess = sess
		s.tiles = append(s.tiles, t)
	}
	if err := sidebar.LoadSpent(s.all, socket, created); err != nil {
		return snapshot{}, err
	}
	s.all = sidebar.Order(s.all)
	sortBySection(s.tiles)
	return s, nil
}

// rollUp places each of this boss's own boss cards by its whole subtree (sidebar.Subtree), its bosses' lists
// as read, then sorts the cards by section again.
func (s *snapshot) rollUp() {
	// A capture's word on a dialog (answered No or Esc, or one an ask's hook recorded) wins over the hooks'
	for _, t := range s.tiles {
		if i := slices.IndexFunc(s.all, func(a sidebar.Session) bool { return a.Name == t.sess.Name }); i >= 0 {
			s.all[i].Deciding, s.all[i].State = t.deciding, t.sess.State
		}
	}
	s.setStatuses()
	rolls := sidebar.Subtree(s.all, sidebar.NeedCounts(s.needLists()))
	for i, t := range s.tiles {
		if t.via == "" {
			s.tiles[i].roll = rolls[t.sess.Name]
		}
	}
	// This boss's own cards before its bosses' sessions within a section, as buildSnapshot lists them
	slices.SortStableFunc(s.tiles, func(a, b tile) int {
		return cmp.Or(cmp.Compare(a.section(), b.section()), cmp.Compare(min(len(a.via), 1), min(len(b.via), 1)))
	})
}

// setStatuses sets the statuses of the own list's items and its bosses' (todo.Owner.Status), from the sessions as
// this refresh saw them, as dev todo --all --json does.
func (s *snapshot) setStatuses() {
	s.todo.SetStatus(todo.OwnerOf(s.all, s.name, false))
	for _, l := range s.asks {
		l.SetStatus(todo.OwnerOf(s.all, l.Boss, l.Closed))
	}
}

// descendants are the sessions the bosses under this boss (id, name) started, bosses at any depth, each with the
// name of the boss that started it; a worker's own sessions aren't followed.
func descendants(names []string, panes map[string][]row, id, name string) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{name: true}
	bosses := []row{{SessionID: id, Session: name}}
	for depth := 0; len(bosses) > 0; depth++ {
		var next []row
		for _, by := range bosses {
			for _, n := range names {
				r := panes[n][0]
				if seen[n] || !sidebar.Names(r.StartedBy, by.SessionID, by.Session, true) {
					continue
				}
				seen[n] = true
				if depth > 0 {
					out[n] = by.Session
				}
				if r.Herd != "" {
					next = append(next, r)
				}
			}
		}
		bosses = next
	}
	return out
}

// bossLists are the open items on the lists of bosses, by boss: those on no one wait on the user, the rest count
// on the boss's card; with closed, then those of the bosses closed on the server; a list not read comes with its error.
func bossLists(c *tmux.Client, bosses []string, closed bool, now time.Time) ([]todo.BossList, error) {
	all, err := todo.All(c, now, func(name string) bool { return slices.Contains(bosses, name) }, closed, false)
	if err != nil {
		return nil, err
	}
	var out []todo.BossList
	for _, l := range all {
		l.Items = slices.DeleteFunc(slices.Clone(l.Items), func(it todo.Shown) bool { return it.Done })
		if len(l.Items) > 0 || l.Err != nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// index is name's tile index, 0 when not listed.
func index(tiles []tile, name string) int {
	for i, t := range tiles {
		if t.sess.Name == name {
			return i
		}
	}
	return 0
}

// osc matches OSC sequences (hyperlinks, titles), which would break the crop: only SGR is kept.
var osc = regexp.MustCompile("\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)")

// lastLines is the last n lines of a capture after dropping trailing blank lines.
func lastLines(capture string, n int) []string {
	return tail(screenLines(capture), n)
}

// screenLines splits a capture into lines without OSC sequences or trailing blank lines.
func screenLines(capture string) []string {
	return trimBlank(strings.Split(osc.ReplaceAllString(capture, ""), "\n"))
}

// trimBlank drops trailing blank lines.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// tail is the last n lines.
func tail(lines []string, n int) []string {
	if n <= 0 {
		return []string{}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// agentPanes are a session's rows as agent.Detect reads them.
func agentPanes(rs []row) []agent.Pane {
	ps := make([]agent.Pane, len(rs))
	for i, r := range rs {
		ps[i] = agent.Pane{Session: r.Session, Window: r.Window, ID: r.ID, Path: r.Path, Title: r.Title,
			Command: r.Command, State: r.State, StateCmd: r.StateCmd, Cost: r.Cost}
	}
	return ps
}

// repeated reports an asks note the boss inbox repeated unanswered (sidebar.Unanswered): the user's again.
func repeated(note string) bool { _, r := sidebar.Unanswered(note); return r }
