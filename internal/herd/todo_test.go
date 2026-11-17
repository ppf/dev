package herd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// withTodo is board4 at 50 columns with a list of four, one waiting on someone else, one linking its PR,
// changed 2 minutes ago.
func withTodo() Model {
	m := board4()
	m.width = 50
	m.snap.todo = todo.List{Updated: m.clk.Now().Add(-2 * time.Minute).Unix(), Items: []todo.Shown{
		{ID: 1, Text: "Answer Vince in Slack: lw deploy window"},
		{ID: 3, Text: "Approve Kasia's PR #84"},
		{ID: 5, Text: "Merge PR #87", Link: "https://github.com/o/r/pull/87"},
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

// The block: a header in the section rows' style with the open count, those on others and the age, then one
// row per item with its number, box, text and at the right who it waits on or its PR.
func TestTodoBlock(t *testing.T) {
	m := withTodo()
	checkRows(t, m.todoRows(39), []string{
		" TO DO 4 · 1 on others ─────────────── updated 2m ",
		" 1 ☐ Answer Vince in Slack: lw deploy window      ",
		" 3 ☐ Approve Kasia's PR #84                       ",
		" 5 ☐ Merge PR #87                           ○ #87 ",
		" 4 ◌ Staging access for lw         on Marek (Ops) ",
	}, 50)
	rows := m.todoRows(39)
	if !strings.Contains(rows[0], bold+"TO DO 4") || !strings.Contains(rows[0], sidebar.Muted+"updated 2m") {
		t.Errorf("header: bold title, muted age: %q", rows[0])
	}
	if !strings.Contains(rows[4], sidebar.Gold+"on Marek") {
		t.Errorf("who it waits on in gold: %q", rows[4])
	}
}

// A ticked item shows ☑, struck through, and leaves the count; a stale list's age goes faint.
func TestTodoDoneStale(t *testing.T) {
	m := withTodo()
	m.snap.todo.Items[1].Done = true
	m.snap.todo.Stale = true
	m.snap.todo.Updated = m.clk.Now().Add(-2 * time.Hour).Unix()
	rows := m.todoRows(39)
	if got := ansi.Strip(rows[0]); !strings.HasPrefix(got, " TO DO 3 · 1 on others ") || !strings.HasSuffix(got, " updated 2h ") {
		t.Errorf("header = %q", got)
	}
	if !strings.Contains(rows[0], rule+"updated 2h") {
		t.Errorf("stale age not faint: %q", rows[0])
	}
	if !strings.Contains(rows[2], sidebar.Green+"☑") || !strings.Contains(rows[2], strike+"Approve") {
		t.Errorf("ticked item: green ☑, struck text: %q", rows[2])
	}
}

// Nine items show five, then "+N more"; a short pane gives the block at most half its rows.
func TestTodoMore(t *testing.T) {
	m := withTodo()
	for i := 6; i <= 10; i++ {
		m.snap.todo.Items = append(m.snap.todo.Items, todo.Shown{ID: i, Text: "item " + strconv.Itoa(i)})
	}
	rows := plain(m.todoRows(39))
	// The user's own items come first, so the one on Marek goes under "+4 more"
	if len(rows) != 7 || rows[4] != " 6 ☐ item 6"+strings.Repeat(" ", 39) || strings.TrimSpace(rows[6]) != "+4 more" {
		t.Errorf("rows:\n%s", strings.Join(rows, "\n"))
	}
	rows = plain(m.todoRows(8))
	if len(rows) != 4 || strings.TrimSpace(rows[3]) != "+7 more" {
		t.Errorf("8 rows: want header, 2 items, +7 more:\n%s", strings.Join(rows, "\n"))
	}
	if rows = plain(m.todoRows(3)); len(rows) != 1 {
		t.Errorf("3 rows: want the header only:\n%s", strings.Join(rows, "\n"))
	}
}

// A narrow pane cuts every row to its width: the age goes first, then the text and who it's on.
func TestTodoNarrow(t *testing.T) {
	m := withTodo()
	m.width = 26
	checkRows(t, m.todoRows(39), []string{
		" TO DO 4 · 1 on others ───",
		" 1 ☐ Answer Vince in Sla… ",
		" 3 ☐ Approve Kasia's PR … ",
		" 5 ☐ Merge PR #87   ○ #87 ",
		" 4 ◌ Staging ac… on Mare… ",
	}, 26)
	m.width = 12
	for i, l := range m.todoRows(39) {
		if w := ansi.StringWidth(l); w != 12 {
			t.Errorf("row %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
}

// t folds the block to its header and opens it again; the hint says which.
func TestTodoFold(t *testing.T) {
	m := withTodo()
	m.height = 30
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = next.(Model)
	checkRows(t, m.todoRows(29), []string{" ▸ TO DO 4 · 1 on others ───────────── updated 2m "}, 50)
	if hint := ansi.Strip(m.bottom()[0]); !strings.Contains(hint, "  t open to do  j/k") {
		t.Errorf("hint = %q", hint)
	}
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m = next.(Model); len(m.todoRows(29)) != 5 {
		t.Error("t again: want the block open")
	}
	if hint := ansi.Strip(m.bottom()[0]); !strings.Contains(hint, "  t fold to do  j/k") {
		t.Errorf("hint = %q", hint)
	}
}

// The block stays at the top while the board under it scrolls to the selected card, and clicks land on the
// cards under it; an empty list renders exactly as without one.
func TestTodoRender(t *testing.T) {
	m := withTodo()
	m.height = 20
	m.selected = "old-spike"
	lines := plain(m.render())
	if len(lines) != 20 || lines[0] != plain(m.todoRows(19))[0] || !strings.HasPrefix(lines[5], " ") {
		t.Fatalf("render:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[18], "old-spike") {
		t.Errorf("the selected card above the hint row, under the block:\n%s", strings.Join(lines, "\n"))
	}
	h, ok := m.hitAt(3, 18)
	if !ok || m.snap.tiles[h.tile].sess.Name != "old-spike" {
		t.Errorf("click on old-spike's row: %+v, %t", h, ok)
	}
	if _, ok := m.hitAt(3, 2); ok {
		t.Error("click on the block hit a card")
	}

	empty := board4()
	empty.width, empty.height, empty.selected = 50, 20, "old-spike"
	withEmpty := empty
	withEmpty.snap.todo = todo.List{Items: []todo.Shown{}, Updated: 1}
	if a, b := plain(empty.render()), plain(withEmpty.render()); !slices.Equal(a, b) {
		t.Errorf("empty list changed the board:\n%s\nwant:\n%s", strings.Join(b, "\n"), strings.Join(a, "\n"))
	}
}

// With no workers the block shows over the no-workers line, and the pane keeps its share rather than collapse.
func TestTodoNoWorkers(t *testing.T) {
	m := withTodo()
	m.snap.tiles = nil
	m.height = 12
	lines := plain(m.render())
	if !strings.HasPrefix(lines[0], " TO DO 4") || !strings.HasPrefix(lines[5], " No worker sessions yet") {
		t.Errorf("render:\n%s", strings.Join(lines, "\n"))
	}
	var ran []string
	m.c, m.pane, m.size = tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, args[0])
		return "200\n", nil
	}), "%2", shared
	next, _ := m.Update(refreshMsg{snap: snapshot{todo: m.snap.todo}})
	if len(ran) != 0 || next.(Model).size != shared {
		t.Errorf("ran %q, size %v: want the pane kept at its share", ran, next.(Model).size)
	}
}

// A pane with no rows above its bottom rows (one row, or a reply box filling it) shows no block, and the
// bottom rows keep their place.
func TestTodoTinyPane(t *testing.T) {
	for _, box := range []bool{false, true} {
		m := withTodo()
		m.selected = "old-spike"
		m.height = 1
		if box {
			m.box.session = "old-spike"
			m.height = len(m.bottom())
		}
		for _, err := range []error{nil, errors.New("bad")} {
			m.snap.todoErr = err
			got := m.render()
			if len(got) != m.height || !slices.Equal(got[len(got)-len(m.bottom()):], m.bottom()) {
				t.Errorf("box %t, err %v: render %q, want the bottom rows %q", box, err, plain(got), plain(m.bottom()))
			}
		}
	}
}

// Each refresh reads the boss's list, found once from its pane: unreadable is an alert row; a not-yet-boss shows none, retried next refresh.
func TestRefreshTodo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	herd := ""
	var looked int
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("herd", "$1", "1", "0", "1", "%2", "/h", "", "dev", "", "", "", "", "", "1", "", "", "", "", "", "", "", "", "", "", "", "", ""), nil
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

// A ticked item on a worker that's gone shows ticked, with no state and no session gone.
func TestTodoDoneWorker(t *testing.T) {
	m := withWorkers()
	it := todo.Shown{ID: 3, Text: "Rework companion importer", On: "companion-1000-feed", Session: &todo.Session{ID: "companion-1000-feed", Name: "companion-1000-feed"}, Done: true}
	if o := m.todoOwner(it); o != nil {
		t.Errorf("owner = %+v, want none for a ticked item", o)
	}
	if row := ansi.Strip(todoItem(it, nil, 70)); strings.Contains(row, "gone") || !strings.Contains(row, "on companion-1000-feed") {
		t.Errorf("row = %q", row)
	}
}

// A worker's item shows its dot, name and section in that section's colour; a linked session no longer listed
// shows ✕ session gone, one without an agent idle; an unlinked name matches a worker too; a person stays gold.
func TestTodoWorkers(t *testing.T) {
	m := withWorkers()
	var rows []string
	for _, it := range m.snap.todo.Items {
		rows = append(rows, todoItem(it, m.todoOwner(it), 70))
	}
	checkRows(t, rows, []string{
		" 1 ☐ Answer Vince                                                     ",
		" 2 ◌ Document the API                            ● docs-api · working ",
		" 3 ◌ Rework companion importer             ✕ companio… · session gone ",
		" 4 ◌ Fix login                                ● fix-login · needs you ",
		" 5 ◌ Get CI green                                  ● ci-fix · waiting ",
		" 6 ◌ Spike notes                                   ● old-spike · idle ",
		" 7 ◌ Shell job                                    ● shell-only · idle ",
		" 8 ◌ Staging access                                          on Marek ",
		" 9 ☐ Approve Kasia's PR                                               ",
	}, 70)
	if !strings.Contains(rows[1], sidebar.Gold+"●") || !strings.Contains(rows[1], sidebar.Gold+"working") {
		t.Errorf("working: gold dot and state: %q", rows[1])
	}
	if !strings.Contains(rows[2], sidebar.Red+"✕ ") || !strings.Contains(rows[2], sidebar.Red+" · session gone") {
		t.Errorf("gone: red: %q", rows[2])
	}
	if !strings.Contains(rows[3], sidebar.Red+"needs you") {
		t.Errorf("needs you: red state: %q", rows[3])
	}
	if !strings.Contains(rows[7], sidebar.Gold+"on Marek") {
		t.Errorf("a person stays gold: %q", rows[7])
	}
}

// The block lists the user's own items first, then those on a worker that needs the user, then on a session
// that's gone, then the rest in the list's order; five of them, then +N more.
func TestTodoWorkerOrder(t *testing.T) {
	m := withWorkers()
	var got []int
	for _, it := range m.todoOrder(m.snap.todo.Items) {
		got = append(got, it.ID)
	}
	if want := []int{1, 9, 4, 3, 2, 5, 6, 7, 8}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	rows := plain(m.todoRows(39))
	if len(rows) != 7 || !strings.HasPrefix(rows[3], " 4 ◌ Fix login") || !strings.HasPrefix(rows[4], " 3 ◌ Rework") || strings.TrimSpace(rows[6]) != "+4 more" {
		t.Errorf("rows:\n%s", strings.Join(rows, "\n"))
	}
}

// A narrow pane keeps a worker's state whole, cutting its name; one too narrow even for that cuts the state rather
// than overflow the row.
func TestTodoWorkerNarrow(t *testing.T) {
	m := withWorkers()
	if row := ansi.Strip(todoItem(m.snap.todo.Items[3], m.todoOwner(m.snap.todo.Items[3]), 40)); !strings.HasSuffix(row, " · needs you ") {
		t.Errorf("40 columns: %q, want the state whole", row)
	}
	for _, w := range []int{12, 20, 30, 40} {
		for _, it := range m.snap.todo.Items {
			if got := ansi.StringWidth(todoItem(it, m.todoOwner(it), w)); got != w {
				t.Errorf("item %d at %d columns is %d wide", it.ID, w, got)
			}
		}
	}
}
