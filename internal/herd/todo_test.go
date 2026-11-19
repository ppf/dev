package herd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// withTodo is board4 at 50 columns with a list of four, three waiting on someone else, one ticked linking its
// PR, changed 2 minutes ago: what the block lists, the user's own items being TO DO's.
func withTodo() Model {
	m := board4()
	m.width = 50
	m.snap.todo = todo.List{Updated: m.clk.Now().Add(-2 * time.Minute).Unix(), Items: []todo.Shown{
		{ID: 1, Text: "Answer Vince in Slack: lw deploy window", On: "Vince"},
		{ID: 3, Text: "Approve Kasia's PR #84", On: "Kasia"},
		{ID: 5, Text: "Merge PR #87", Link: "https://github.com/o/r/pull/87", Done: true},
		{ID: 4, Text: "Staging access for lw", On: "Marek (Ops)"},
	}}
	return m
}

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// checkRows fails unless got, stripped, is want, each row width columns wide.
func checkRows(t *testing.T, got, want []string, width int) {
	t.Helper()
	if p := plain(got); !slices.Equal(p, want) {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(p, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range got {
		if w := ansi.StringWidth(l); w != width {
			t.Errorf("row %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
}

// The block: a header in the section rows' style with the open count and the age, then one row per item with its
// The block stays at the top while the board under it scrolls to the selected card, and clicks land on the
// A pane with no rows above its bottom rows (one row, or a reply box filling it) shows no block, and the
// Each refresh reads the boss's list, found once from its pane: unreadable is an alert row; a not-yet-boss shows none, retried next refresh.
func TestRefreshTodo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	herd := ""
	var looked int
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("herd", "$1", "1", "0", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", ""), nil
		case "display-message":
			if strings.Contains(args[len(args)-1], "@dev_herd") {
				looked++
				return tmux.FormatRow("/tmp/s", "herd", herd, "$1", "1700000000"), nil
			}
			return "200\n", nil // fit's width
		}
		return "", nil
	})
	now := time.Unix(1700000000, 0)
	m := Model{c: c, pane: "%2", look: &fakeLookups{}, width: 50, height: 20, findTodo: true, clk: control.Clock{Now: func() time.Time { return now }}}
	msg := m.refresh()().(refreshMsg)
	if msg.todo != nil || msg.snap.todoErr != nil {
		t.Fatalf("unmarked: store %v, err %v", msg.todo, msg.snap.todoErr)
	}
	herd = "1"
	next, _ := m.Update(m.refresh()())
	m = next.(Model)
	if m.todo == nil || m.snap.todoErr != nil || len(m.snap.todo.Items) != 0 {
		t.Fatalf("marked: store %v, err %v, list %+v", m.todo, m.snap.todoErr, m.snap.todo)
	}
	if err := m.todo.Set([]string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	next, _ = m.Update(m.refresh()())
	if m = next.(Model); looked != 2 || len(m.snap.todo.Items) != 1 || m.snap.todo.Items[0].Text != "a" {
		t.Errorf("looked up %d times, list %+v", looked, m.snap.todo)
	}
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*.json"))
	if err := os.WriteFile(matches[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg = m.refresh()().(refreshMsg); msg.snap.todoErr == nil {
		t.Error("broken file: want an error")
	}
}

// A list with no workers grows a collapsed pane to its share; once it's empty the pane collapses again.
func TestFitTodo(t *testing.T) {
	var ran []string
	c := tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, args[0])
		return "200\n", nil
	})
	list := withTodo().snap.todo
	m := Model{c: c, pane: "%2", size: collapsed}
	for _, st := range []struct {
		name string
		snap snapshot
		want paneSize
	}{
		{"a list", snapshot{todo: list}, shared},
		{"unreadable", snapshot{todoErr: errors.New("bad")}, shared},
		{"emptied", snapshot{todo: todo.List{Items: []todo.Shown{}}}, collapsed},
		{"bosses' items on others only", snapshot{asks: []todo.BossList{{Boss: "b", List: todo.List{Items: []todo.Shown{{ID: 1, Text: "x", On: "Carl"}}}}}}, shared},
	} {
		next, _ := m.Update(refreshMsg{snap: st.snap})
		if m = next.(Model); m.size != st.want {
			t.Errorf("%s: size %v, want %v (ran %q)", st.name, m.size, st.want, ran)
		}
	}
}

// withWorkers is board4's list on its workers: one linked to each section's worker, one named on fix-login
// without a link, one on a session with no agent, one on a session that's gone, one on a person.
func withWorkers() Model {
	m := board4()
	m.snap.started = map[string]bool{"fix-login": true, "docs-api": true, "ci-fix": true, "old-spike": true, "shell-only": true}
	on := func(id int, text, name string) todo.Shown {
		return todo.Shown{ID: id, Text: text, On: name, Session: &todo.Session{ID: name, Name: name}}
	}
	m.snap.todo = todo.List{Updated: m.clk.Now().Unix(), Items: []todo.Shown{
		{ID: 1, Text: "Answer Vince"},
		on(2, "Document the API", "docs-api"),
		on(3, "Rework companion importer", "companion-1000-feed"),
		{ID: 4, Text: "Fix login", On: "Fix-Login"},
		on(5, "Get CI green", "ci-fix"),
		on(6, "Spike notes", "old-spike"),
		on(7, "Shell job", "shell-only"),
		{ID: 8, Text: "Staging access", On: "Marek"},
		{ID: 9, Text: "Approve Kasia's PR"},
	}}
	return m
}

// A worker's item shows its dot, name and section in that section's colour; a linked session no longer listed
// An item linked to a boss this one started (a manager's) shows the boss's state, its whole subtree's; once the
// The block lists those on a worker that needs the user first, then on a session that's gone, then the rest in
// A narrow pane keeps a worker's state whole, cutting its name; one too narrow even for that cuts the state rather
// An item on a person shows the person and its PR's number; once its PR moved, the person and ⚠ why, the name cut
// A worker's name stays readable beside a check: whole while the row has room, else the check gives up its age, then
