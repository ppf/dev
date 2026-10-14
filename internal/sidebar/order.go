package sidebar

import (
	"cmp"
	"slices"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// Group is a session's sidebar group: its @project, else its name cut at the last "-<digit>"
// (bash ${name%-[0-9]*}: app-2 → app, proj-12-fix → proj).
func Group(name, project string) string {
	if project != "" {
		return project
	}
	for i := len(name) - 2; i >= 0; i-- {
		if name[i] == '-' && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[:i]
		}
	}
	return name
}

// Order returns sessions sorted by (Group, Name) in byte order: the sidebar's rows,
// @sidebar_session_order, dev goto's numbers and the kill replacement all use it.
func Order(sessions []Session) []Session {
	sorted := slices.Clone(sessions)
	slices.SortStableFunc(sorted, func(a, b Session) int {
		return cmp.Or(strings.Compare(a.Group, b.Group), strings.Compare(a.Name, b.Name))
	})
	return sorted
}

// LoadOrder lists the server's session names in Order, from one list-sessions query.
func LoadOrder(c *tmux.Client) ([]string, error) {
	rows, err := tmux.Query[struct {
		Name    string `tmux:"session_name"`
		Project string `tmux:"@project"`
	}](c, "list-sessions")
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(rows))
	for i, r := range rows {
		sessions[i] = Session{Name: r.Name, Group: Group(r.Name, r.Project)}
	}
	names := make([]string, 0, len(rows))
	for _, s := range Order(sessions) {
		names = append(names, s.Name)
	}
	return names, nil
}
