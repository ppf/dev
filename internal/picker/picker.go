// Package picker is the two-step project → worktree chooser (replaces fzf in select.sh).
package picker

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/worktree"
)

// PopupGuard is set (=1) in the picker's own popup, so dev there runs the picker instead of another popup.
const PopupGuard = "SIDEBAR_SELECT_IN_POPUP"

// Result is the user's choice; Chosen=false means Esc (exit 0, nothing opened).
type Result struct {
	Chosen   bool
	Project  config.Project
	Worktree string // sanitized; empty = main checkout
}

// Run shows the project list, then the worktree step when the project is a git repo.
func Run(projects []config.Project) (Result, error) {
	pm, err := run(projectFinder(projects))
	if err != nil {
		return Result{}, err
	}
	pick := pm.picked()
	if pm.aborted || pick < 0 {
		return Result{}, nil
	}
	p := projects[pick]

	dir, err := p.Dir()
	if err != nil {
		return Result{}, err
	}
	root, ok := worktree.Root(dir)
	if !ok {
		return Result{Chosen: true, Project: p}, nil
	}
	names, err := worktree.List(root)
	if err != nil {
		return Result{}, err
	}
	wm, err := run(newWorktreeModel(names))
	if err != nil {
		return Result{}, err
	}
	switch {
	case !wm.chosen:
		return Result{}, nil
	case wm.main:
		return Result{Chosen: true, Project: p}, nil
	}
	name := worktree.Sanitize(wm.name)
	if name == "" {
		return Result{}, nil
	}
	return Result{Chosen: true, Project: p, Worktree: name}, nil
}

// projectFinder lists "name description" rows with the configured path as preview.
func projectFinder(projects []config.Project) finder {
	rows := make([]string, len(projects))
	for i, p := range projects {
		rows[i] = strings.TrimSpace(p.Name + " " + p.Description)
	}
	preview := func(row int) string { return "Path: " + projects[row].Path }
	return newFinder("Select project", rows, false, preview)
}

func run[M tea.Model](m M) (M, error) {
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return m, err
	}
	return final.(M), nil
}

const (
	worktreeHeader = "Session mode — type a name to create a new worktree"
	rowMain        = 0 // "main repo"; row 1 is "+ new worktree"
	firstWorktree  = 2
)

// worktreeModel is the worktree step plus the "Worktree name: " prompt for "+ new worktree".
type worktreeModel struct {
	list      finder
	names     []string
	prompting bool
	input     []rune

	chosen bool
	main   bool
	name   string // unsanitized
}

func newWorktreeModel(names []string) worktreeModel {
	rows := []string{"main repo", "+ new worktree"}
	for _, n := range names {
		rows = append(rows, "wt: "+n)
	}
	return worktreeModel{list: newFinder(worktreeHeader, rows, true, nil), names: names}
}

func (m worktreeModel) Init() tea.Cmd { return nil }

func (m worktreeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.prompting {
		k, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		switch k.Type {
		case tea.KeyEnter:
			m.name = string(m.input)
			m.chosen = m.name != ""
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			return m, tea.Quit
		}
		m.input = edit(m.input, k)
		return m, nil
	}

	m.list = m.list.update(msg)
	if !m.list.done {
		return m, nil
	}
	if m.list.aborted {
		return m, tea.Quit
	}
	// "+ new worktree" and no match both take the typed query as the name, like select.sh.
	switch pick := m.list.picked(); {
	case pick == rowMain:
		m.chosen, m.main = true, true
	case pick >= firstWorktree:
		m.chosen, m.name = true, m.names[pick-firstWorktree]
	case len(m.list.query) > 0:
		m.chosen, m.name = true, string(m.list.query)
	default:
		m.prompting = true
		return m, nil
	}
	return m, tea.Quit
}

func (m worktreeModel) View() string {
	if m.prompting {
		return "Worktree name: " + string(m.input) + caretStyle.Render(" ")
	}
	return m.list.View()
}
