// Package picker is the project chooser (replaces fzf in select.sh).
package picker

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/config"
)

// PopupGuard is set (=1) in the picker's own popup, so dev there runs the picker instead of another popup.
const PopupGuard = "SIDEBAR_SELECT_IN_POPUP"

// Result is the user's choice; Chosen=false means Esc (exit 0, nothing opened).
type Result struct {
	Chosen  bool
	Project config.Project
}

// Run shows the project list.
func Run(projects []config.Project) (Result, error) {
	final, err := tea.NewProgram(projectFinder(projects), tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	f := final.(finder)
	pick := f.picked()
	if f.aborted || pick < 0 {
		return Result{}, nil
	}
	return Result{Chosen: true, Project: projects[pick]}, nil
}

// projectFinder lists "name description" rows with the configured path as preview.
func projectFinder(projects []config.Project) finder {
	rows := make([]string, len(projects))
	for i, p := range projects {
		rows[i] = strings.TrimSpace(p.Name + " " + p.Description)
	}
	preview := func(row int) string { return "Path: " + projects[row].Path }
	return newFinder("Select project", rows, preview)
}
