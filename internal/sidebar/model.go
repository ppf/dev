package sidebar

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

// pollInterval is bash's POLL_INTERVAL: focus, size and sessions follow tmux within one tick.
const pollInterval = 120 * time.Millisecond

// paneRow is one row of the per-tick `list-panes -a` query: agent.Pane's fields, the session and
// global options every row resolves, and the geometry and focus the sidebar reads off its own pane.
type paneRow struct {
	Session  string `tmux:"session_name"`
	Window   int    `tmux:"window_index"`
	ID       string `tmux:"pane_id"`
	Path     string `tmux:"pane_current_path"`
	Title    string `tmux:"pane_title"`
	Command  string `tmux:"pane_current_command"`
	State    string `tmux:"@agent_state"`
	StateCmd string `tmux:"@agent_cmd"`
	Active   bool   `tmux:"pane_active"`
	Width    int    `tmux:"pane_width"`
	Height   int    `tmux:"pane_height"`
	Project  string `tmux:"@project"`
	Sidebar  string `tmux:"@sidebar_pane"`
	Selected string `tmux:"@sidebar_selected_session"`
}

// agentPane returns the fields agent.Detect reads.
func (r paneRow) agentPane() agent.Pane {
	return agent.Pane{
		Session: r.Session, Window: r.Window, ID: r.ID, Path: r.Path,
		Title: r.Title, Command: r.Command, State: r.State, StateCmd: r.StateCmd,
	}
}

// snapshot is what one refresh saw.
type snapshot struct {
	sessions []Session // display order (Order)
	current  string    // the sidebar's own session
	width    int       // own pane
	height   int
	focused  bool // own pane is its window's active pane
	clients  []Client
}

// Model is the Bubble Tea sidebar.
type Model struct {
	c        *tmux.Client
	pane     string // this sidebar's pane id (TMUX_PANE)
	host     string // hostname: tmux's default pane title, so a generic title
	branches *branchCache
	snap     snapshot
	selected string // local copy of @sidebar_selected_session
	gen      int    // bumped by each local selection change, so a refresh already running can't undo it
	order    string // @sidebar_session_order as this sidebar last wrote it
	err      error  // last refresh failure; nil once a refresh succeeds

	winWidth, winHeight int // Bubble Tea's window size: where an error shows before the first snapshot
}

// tickMsg starts a refresh.
type tickMsg struct{}

// refreshMsg is a finished refresh.
type refreshMsg struct {
	snap     snapshot
	selected string // after the selection sync
	read     string // @sidebar_selected_session as the refresh read it
	order    string // @sidebar_session_order after the refresh
	gen      int    // Model.gen when the refresh started
	err      error
}

// Run shows the sidebar in pane until the process is killed (its pane closes) or Ctrl-C.
func Run(c *tmux.Client, pane string) error {
	if _, err := tea.NewProgram(newModel(c, pane), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return fmt.Errorf("sidebar: %w", err)
	}
	return nil
}

// newModel returns the sidebar for pane, before its first refresh.
func newModel(c *tmux.Client, pane string) Model {
	host, err := os.Hostname()
	if err != nil {
		// Only the hostname check in isGenericTitle is lost
		host = ""
	}
	return Model{c: c, pane: pane, host: host, branches: newBranchCache()}
}

// Init starts the first refresh at once, as bash refreshed before its first render.
func (m Model) Init() tea.Cmd {
	return m.refresh()
}

// Update routes refreshes to the model and input to the actions.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, m.refresh()
	case refreshMsg:
		return m.apply(msg), tick()
	case tea.WindowSizeMsg:
		m.winWidth, m.winHeight = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// tick schedules the next refresh.
func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// refresh queries tmux once (list-panes -a, list-clients), builds the snapshot, writes
// @sidebar_session_order when it changed and computes the selection sync (refresh_sessions); apply writes it.
func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		fail := func(err error) tea.Msg { return refreshMsg{gen: m.gen, err: err} }
		rows, err := tmux.Query[paneRow](m.c, "list-panes", "-a")
		if err != nil {
			return fail(err)
		}
		clients, err := tmux.Query[Client](m.c, "list-clients")
		if err != nil {
			return fail(err)
		}
		snap, err := buildSnapshot(rows, clients, m.pane, m.host, m.branches.get)
		if err != nil {
			return fail(err)
		}
		order := orderOption(snap.sessions)
		if order != m.order {
			if _, err := m.c.Run("set-option", "-gq", optOrder, order); err != nil {
				return fail(err)
			}
		}
		// A global option: every row carries the same value, and buildSnapshot found at least one row
		read := rows[0].Selected
		return refreshMsg{snap: snap, selected: syncSelection(read, snap), read: read, order: order, gen: m.gen}
	}
}

// apply takes a refresh's result, keeping a local selection made after the refresh started: a stale
// selection sync is neither applied nor written, so it can't overwrite a newer key press on the server.
func (m Model) apply(msg refreshMsg) Model {
	if msg.err != nil {
		// Keep the last snapshot; the view shows the error until a refresh succeeds
		m.err = msg.err
		return m
	}
	m.snap, m.order, m.err = msg.snap, msg.order, nil
	if msg.gen != m.gen {
		return m
	}
	m.selected = msg.selected
	if msg.selected != msg.read {
		if err := setSelected(m.c, msg.selected); err != nil {
			m.err = err
		}
	}
	return m
}

// buildSnapshot turns the query rows into sessions in display order, own pane geometry and focus.
// branch returns a path's branch (the cache in production).
func buildSnapshot(rows []paneRow, clients []Client, pane, host string, branch func(path string) string) (snapshot, error) {
	s := snapshot{clients: clients}
	own := false
	var names []string
	panes := map[string][]paneRow{}
	for _, r := range rows {
		if r.ID == pane {
			s.current, s.width, s.height, s.focused, own = r.Session, r.Width, r.Height, r.Active, true
		}
		if _, seen := panes[r.Session]; !seen {
			names = append(names, r.Session)
		}
		panes[r.Session] = append(panes[r.Session], r)
	}
	if !own {
		return snapshot{}, fmt.Errorf("sidebar: pane %s not listed", pane)
	}
	sessions := make([]Session, 0, len(names))
	for _, name := range names {
		rs := panes[name]
		ps := make([]agent.Pane, len(rs))
		for i, r := range rs {
			ps[i] = r.agentPane()
		}
		// Session options: every row of the session carries the same @project and @sidebar_pane
		sess := Session{Name: name, Group: Group(name, rs[0].Project)}
		if a, ok := agent.Detect(ps, rs[0].Sidebar); ok {
			title := cleanTitle(a.Pane.Title)
			if isGenericTitle(title, name, host) {
				title = ""
			}
			sess.Agent, sess.State, sess.Title = a.Label, a.State, agentTitle(a.Label, title)
			sess.Branch = branch(a.Pane.Path)
		}
		sessions = append(sessions, sess)
	}
	s.sessions = Order(sessions)
	return s, nil
}

// syncSelection is the selection after a refresh, given @sidebar_selected_session as read
// (refresh_sessions' auto-sync, then sync_selected_index).
func syncSelection(selected string, s snapshot) string {
	// Only the sidebar of the session the client is viewing adopts it, so background sidebars
	// don't overwrite a switch
	if c, ok := activeClient(s.clients, ""); ok && !s.focused && c.Session == s.current {
		return s.current
	}
	if selected == "" {
		selected = s.current
	}
	listed := func(name string) bool {
		return slices.ContainsFunc(s.sessions, func(x Session) bool { return x.Name == name })
	}
	switch {
	case listed(selected):
		return selected
	case listed(s.current):
		return s.current
	case len(s.sessions) > 0:
		return s.sessions[0].Name
	}
	return ""
}

// orderOption is the @sidebar_session_order value for sessions: each name followed by a newline.
func orderOption(sessions []Session) string {
	var b strings.Builder
	for _, s := range sessions {
		b.WriteString(s.Name + "\n")
	}
	return b.String()
}

// setSelection selects name here and in @sidebar_selected_session, ahead of any refresh in flight.
func (m *Model) setSelection(name string) error {
	m.selected = name
	m.gen++
	return setSelected(m.c, name)
}

// client is the tty of the client the sidebar acts on: the most recently active one viewing its
// session; "" lets tmux pick.
func (m Model) client() string {
	c, _ := activeClient(m.snap.clients, m.snap.current)
	return c.TTY
}
