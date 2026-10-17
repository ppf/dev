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

// Order returns sessions sorted by (Group, Name) in byte order, each worker a coordinator started
// (StartedBy) moved right after it, into its group, one Depth deeper: the sidebar's rows,
// @sidebar_session_order, dev goto's numbers and the kill replacement all use it.
func Order(sessions []Session) []Session {
	sorted := slices.Clone(sessions)
	slices.SortStableFunc(sorted, func(a, b Session) int {
		return cmp.Or(strings.Compare(a.Group, b.Group), strings.Compare(a.Name, b.Name))
	})
	listed := map[string]bool{}
	for _, s := range sorted {
		if s.ID != "" {
			listed[s.ID] = true
		}
	}
	children := map[string][]Session{}
	var roots []Session
	for _, s := range sorted {
		if s.StartedBy != "" && s.StartedBy != s.ID && listed[s.StartedBy] {
			children[s.StartedBy] = append(children[s.StartedBy], s)
		} else {
			roots = append(roots, s)
		}
	}
	out := make([]Session, 0, len(sorted))
	placed := map[string]bool{}
	var place func(s Session, depth int, group string)
	place = func(s Session, depth int, group string) {
		if s.ID != "" {
			if placed[s.ID] {
				return
			}
			placed[s.ID] = true
		}
		s.Depth, s.Group = depth, group
		out = append(out, s)
		for _, c := range children[s.ID] {
			place(c, depth+1, group)
		}
	}
	for _, s := range roots {
		place(s, 0, s.Group)
	}
	// Only a cycle of starters leaves sessions unplaced: list them flat rather than drop them
	for _, s := range sorted {
		if s.ID != "" && !placed[s.ID] {
			place(s, 0, s.Group)
		}
	}
	return out
}

// LoadOrder lists the server's session names in Order, from one list-sessions query.
func LoadOrder(c *tmux.Client) ([]string, error) {
	rows, err := tmux.Query[struct {
		Name      string `tmux:"session_name"`
		Project   string `tmux:"@project"`
		ID        string `tmux:"session_id"`
		StartedBy string `tmux:"@dev_started_by"`
	}](c, "list-sessions")
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(rows))
	for i, r := range rows {
		sessions[i] = Session{Name: r.Name, Group: Group(r.Name, r.Project), ID: r.ID, StartedBy: r.StartedBy}
	}
	names := make([]string, 0, len(rows))
	for _, s := range Order(sessions) {
		names = append(names, s.Name)
	}
	return names, nil
}
