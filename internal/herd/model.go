package herd

import (
	"cmp"
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
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
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
}

// tile is one worker session's preview.
type tile struct {
	sess     sidebar.Session
	pane     string         // the agent pane
	deciding bool           // the agent waits on the user's decision, see agent.Deciding
	question agent.Question // @agent_question, what its dialog asks
	body     []string       // the pane's last screen lines; nil when not captured this tick
	pick     *pick          // the dialog a deciding agent shows; nil when none or not captured
	note     string         // its latest boss inbox note (inbox.OptNote)
	since    time.Time      // when its state began (@agent_since); zero when unknown
	context  string         // @agent_context, its context window's use in whole percent; "" when unknown
	tail     desktop.Tail   // its transcript's end; zero when not read this refresh (boss hidden), for an agent without one, or none written yet
	tailErr  error          // why its transcript could not be read
	ci       ci             // its open or draft PR's checks as last read; zero before a read, or when not looked up this refresh
}

// snapshot is what one refresh saw.
type snapshot struct {
	tiles   []tile
	self    string // own session's id
	name    string // own session's name
	visible bool   // a client shows own window
	focused bool   // own pane is its window's active pane
	client  string // tty of the client viewing own session; "" lets tmux pick
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
	confirm       string // session waiting for y to be killed
	msg           string // one-off message on the bottom row, until the next key
	box           replyBox
	drafts        map[string]replyBox // by session: a reply typed when focus moved away, back when its box opens again
	focused       bool                // own pane is its window's active pane, as of the last input or refresh
	inputs        int                 // keys and clicks seen, so a refresh listed before one doesn't undo its focus
	answering     string              // the session an answer's keys go to, until its outcome; one at a time
	history       []string            // replies sent, oldest first
	clk           control.Clock
	host          string          // hostname, tmux's default pane title: a title that says nothing
	folds         map[string]bool // by session: the user's fold of its card, over the default (see tile.folded)
	expand        bool            // the selected card shows more of its screen
}

// lookups is what refresh asks sidebar.Lookups; tests replace it.
type lookups interface {
	Branch(path string) string
	PR(pane, stored, gen, path, branch string) sidebar.PR
	OpenPR(url string) error
	Tail(path string) (desktop.Tail, error)
	CI(url string) ci
}

type tickMsg struct{}

// watchMsg runs the inbox watch (stuck workers, deferred delivery) every watchInterval.
type watchMsg struct{ err error }

const watchInterval = 15 * time.Second

type refreshMsg struct {
	snap   snapshot
	inputs int // m.inputs when the refresh started
	err    error
}

// Run shows the previews in pane until its pane closes or Ctrl-C.
func Run(c *tmux.Client, pane string) error {
	if _, err := tea.NewProgram(newModel(c, pane), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return fmt.Errorf("boss: %w", err)
	}
	return nil
}

// newModel is the previews in pane.
func newModel(c *tmux.Client, pane string) Model {
	// Without it only a title naming the host shows
	host, _ := os.Hostname()
	return Model{c: c, pane: pane, look: newLookups(c), clk: control.RealClock, host: host}
}

// Init starts the first refresh and fits the pane to its share of the window, also for a boss restored, or
// one an older dev left wider or narrower.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.watchLater(), m.action(func() error { return fitWidth(m.c, m.pane) }))
}

// watchLater runs inbox.Watch after watchInterval.
func (m Model) watchLater() tea.Cmd {
	return tea.Tick(watchInterval, func(time.Time) tea.Msg {
		return watchMsg{err: inbox.Watch(m.c, control.RealClock, m.pane)}
	})
}

// Update routes refreshes, size and input.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, m.refresh()
	case refreshMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.snap, m.err = msg.snap, nil
			if msg.inputs == m.inputs {
				m.focused = msg.snap.focused
			}
			// An answer going out or still on screen keeps its picker, so it isn't answered twice
			if !m.focused && m.box.session != "" && m.answering != m.box.session && m.box.sent == "" {
				m.blur()
			}
			m.selected = m.validSelection()
			m.syncPick()
			for name := range m.folds {
				if !slices.ContainsFunc(m.snap.tiles, func(t tile) bool { return t.sess.Name == name }) {
					// A new session of that name starts from its section's default
					delete(m.folds, name)
				}
			}
		}
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		m.focused, m.inputs = true, m.inputs+1
		return m.handleKey(msg)
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// tmux focuses a clicked pane before passing the click on
			m.focused, m.inputs = true, m.inputs+1
		}
		return m.handleMouse(msg)
	case actionMsg:
		m.msg = msg.msg
		return m, nil
	case answeredMsg:
		m.msg, m.answering = msg.msg, ""
		if !msg.took && msg.session == m.box.session {
			m.box.sent = ""
		}
		return m, nil
	case watchMsg:
		if msg.err != nil {
			m.msg = "❌ inbox: " + msg.err.Error()
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
// cards' panes and the deciding ones', for their dialogs (one call each, at most maxCaptures, the selected
// card's first).
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
		if !snap.visible {
			return refreshMsg{snap: snap, inputs: inputs}
		}
		for _, i := range captures(snap.tiles, selected, folds) {
			out, err := m.c.Run("capture-pane", "-p", "-e", "-t", snap.tiles[i].pane)
			if err != nil {
				// The pane may have closed since list-panes; its card goes with the next refresh
				snap.tiles[i].body = []string{err.Error()}
				continue
			}
			t := &snap.tiles[i]
			if t.deciding {
				if d, ok := parseDialog(out); ok {
					p := newPick(d, t.question, ansi.Strip(out))
					t.pick = &p
				} else {
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
		// A dialog found gone moves its card out of Needs you
		sortBySection(snap.tiles)
		return refreshMsg{snap: snap, inputs: inputs}
	}
}

// maxCaptures caps the captures per refresh.
const maxCaptures = 12

// captures are the indexes of the tiles whose panes a refresh captures: the selected one when open, then the
// deciding ones, then the other open ones nearest the selected one (in view, as the board scrolls to it), at
// most maxCaptures.
func captures(tiles []tile, selected string, folds map[string]bool) []int {
	var first, deciding, open []int
	sel := index(tiles, selected)
	for i, t := range tiles {
		switch {
		case t.sess.Name == selected && (t.deciding || !t.folded(folds)):
			first = append(first, i)
		case t.deciding:
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

// sortBySection puts tiles in section order, keeping their order within one.
func sortBySection(tiles []tile) {
	slices.SortStableFunc(tiles, func(a, b tile) int { return cmp.Compare(a.section(), b.section()) })
}

// buildSnapshot turns the rows into tiles by section, in sidebar order within one, keeping only agent sessions
// this boss started. PRs, their checks and transcripts are only read while visible.
func buildSnapshot(rows []row, clients []sidebar.Client, pane, host string, look lookups) (snapshot, error) {
	var s snapshot
	own := false
	var names []string
	panes := map[string][]row{}
	for _, r := range rows {
		if r.ID == pane {
			s.self, s.name, s.focused, own = r.SessionID, r.Session, r.Active, true
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
	var sessions []sidebar.Session
	meta := map[string]tile{}
	for _, name := range names {
		rs := panes[name]
		if rs[0].Herd != "" || !sidebar.Names(rs[0].StartedBy, s.self, s.name, true) {
			continue
		}
		ps := make([]agent.Pane, len(rs))
		for i, r := range rs {
			ps[i] = agent.Pane{Session: r.Session, Window: r.Window, ID: r.ID, Path: r.Path, Title: r.Title,
				Command: r.Command, State: r.State, StateCmd: r.StateCmd, Cost: r.Cost}
		}
		a, ok := agent.Detect(ps, rs[0].Sidebar)
		if !ok {
			continue
		}
		sess := sidebar.Session{Name: name, Group: sidebar.Group(name, rs[0].Project), Agent: a.Label, State: a.State,
			Branch: look.Branch(a.Pane.Path), Title: sidebar.PaneTitle(a.Label, a.Pane.Title, name, host)}
		if a.CostOK && !sidebar.CostHidden(rs[0].CostOpt) {
			sess.Cost = sidebar.FormatCost(a.Cost)
		}
		r := rs[slices.IndexFunc(rs, func(r row) bool { return r.ID == a.Pane.ID })]
		t := tile{pane: a.Pane.ID, deciding: agent.Deciding(a.State, r.WaitingFor), note: r.Note, context: r.Context}
		if sec, err := strconv.ParseInt(r.Since, 10, 64); err == nil {
			t.since = time.Unix(sec, 0)
		}
		t.question, _ = agent.ParseQuestion(r.Question)
		if s.visible {
			sess.PR = look.PR(a.Pane.ID, r.PR, r.PRGen, a.Pane.Path, sess.Branch)
			if pr := sess.PR; pr.URL != "" && (pr.State == "open" || pr.State == "draft") {
				t.ci = look.CI(pr.URL)
			}
			if r.Transcript != "" {
				t.tail, t.tailErr = look.Tail(r.Transcript)
			}
		}
		sessions = append(sessions, sess)
		meta[name] = t
	}
	for _, sess := range sidebar.Order(sessions) {
		t := meta[sess.Name]
		t.sess = sess
		s.tiles = append(s.tiles, t)
	}
	sortBySection(s.tiles)
	return s, nil
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
