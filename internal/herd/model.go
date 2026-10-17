package herd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
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
}

// tile is one worker session's preview.
type tile struct {
	sess      sidebar.Session
	pane      string   // the agent pane
	startedBy string   // OptStartedBy
	body      []string // the pane's last screen lines; nil when not captured this tick
}

// snapshot is what one refresh saw.
type snapshot struct {
	tiles   []tile
	self    string // own session's id
	name    string // own session's name
	visible bool   // a client shows own window
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
	reply         string // session a reply is being typed to; "" when not typing
	replyPane     string // its agent pane
	draft         []rune // the reply so far
}

// lookups is what refresh asks sidebar.Lookups; tests replace it.
type lookups interface {
	Branch(path string) string
	PR(path, branch string) sidebar.PR
	OpenPR(url string) error
}

type tickMsg struct{}

// watchMsg runs the inbox watch (stuck workers, deferred delivery) every watchInterval.
type watchMsg struct{ err error }

const watchInterval = 15 * time.Second

type refreshMsg struct {
	snap snapshot
	err  error
}

// Run shows the previews in pane until its pane closes or Ctrl-C.
func Run(c *tmux.Client, pane string) error {
	m := Model{c: c, pane: pane, look: sidebar.NewLookups()}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return fmt.Errorf("boss: %w", err)
	}
	return nil
}

// Init starts the first refresh.
func (m Model) Init() tea.Cmd { return tea.Batch(m.refresh(), m.watchLater()) }

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
			m.selected = m.validSelection()
		}
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case actionMsg:
		m.msg = msg.msg
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

// refresh lists panes and clients (two tmux calls), and while a client shows this window captures the
// tiles on the selected page (one call each, at most maxTiles).
func (m Model) refresh() tea.Cmd {
	width, height, selected := m.width, m.height, m.selected
	return func() tea.Msg {
		rows, err := tmux.Query[row](m.c, "list-panes", "-a")
		if err != nil {
			return refreshMsg{err: err}
		}
		clients, err := tmux.Query[sidebar.Client](m.c, "list-clients")
		if err != nil {
			return refreshMsg{err: err}
		}
		snap, err := buildSnapshot(rows, clients, m.pane, m.look)
		if err != nil {
			return refreshMsg{err: err}
		}
		if !snap.visible {
			return refreshMsg{snap: snap}
		}
		g := layout(len(snap.tiles), width, height-1)
		from, to := g.page(index(snap.tiles, selected))
		to = min(to, len(snap.tiles))
		for i := from; i < to; i++ {
			out, err := m.c.Run("capture-pane", "-p", "-e", "-t", snap.tiles[i].pane)
			if err != nil {
				// The pane may have closed since list-panes; its tile goes with the next refresh
				snap.tiles[i].body = []string{err.Error()}
				continue
			}
			lines := screenLines(out)
			if snap.tiles[i].sess.Agent == "Claude" {
				lines = cropInput(lines)
			}
			snap.tiles[i].body = tail(lines, g.tileH-1)
		}
		return refreshMsg{snap: snap}
	}
}

// buildSnapshot turns the rows into tiles in sidebar order, leaving out herd sessions and sessions
// without an agent. PRs are only looked up while visible.
func buildSnapshot(rows []row, clients []sidebar.Client, pane string, look lookups) (snapshot, error) {
	var s snapshot
	own := false
	var names []string
	panes := map[string][]row{}
	for _, r := range rows {
		if r.ID == pane {
			s.self, s.name, own = r.SessionID, r.Session, true
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
		if rs[0].Herd != "" {
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
			Branch: look.Branch(a.Pane.Path)}
		if a.CostOK {
			sess.Cost = sidebar.FormatCost(a.Cost)
		}
		if s.visible {
			sess.PR = look.PR(a.Pane.Path, sess.Branch)
		}
		sessions = append(sessions, sess)
		meta[name] = tile{pane: a.Pane.ID, startedBy: rs[0].StartedBy}
	}
	for _, sess := range sidebar.Order(sessions) {
		t := meta[sess.Name]
		t.sess = sess
		s.tiles = append(s.tiles, t)
	}
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
	lines := strings.Split(osc.ReplaceAllString(capture, ""), "\n")
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
