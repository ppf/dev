package picker

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/config"
)

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func typed(s string) []tea.Msg {
	var msgs []tea.Msg
	for _, r := range s {
		if r == ' ' {
			msgs = append(msgs, key(tea.KeySpace))
		} else {
			msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	return msgs
}

// feed drives m through msgs and reports whether the last one quit the program.
func feed[M tea.Model](t *testing.T, m M, msgs ...tea.Msg) (M, bool) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(M)
	}
	if cmd == nil {
		return m, false
	}
	_, quit := cmd().(tea.QuitMsg)
	return m, quit
}

func rowsOf(f finder) []string {
	var rows []string
	for _, i := range f.matches {
		rows = append(rows, f.rows[i])
	}
	return rows
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		text, query string
		start, span int
		ok          bool
	}{
		{"dev tools", "", 0, 0, true},
		{"dev tools", "dt", 0, 5, true},
		{"dev tools", "tls", 4, 5, true},
		{"dev tools", "xyz", 0, 0, false},
		{"dev tools", "sd", 0, 0, false},
		// Backward pass shrinks "a...a.b" to the tightest span ending at the first full match.
		{"a xab", "ab", 3, 2, true},
	} {
		start, span, ok := match([]rune(tc.text), []rune(tc.query))
		if start != tc.start || span != tc.span || ok != tc.ok {
			t.Errorf("match(%q, %q) = %d, %d, %v; want %d, %d, %v", tc.text, tc.query, start, span, ok, tc.start, tc.span, tc.ok)
		}
	}
}

func TestFilterSmartCaseAndRanked(t *testing.T) {
	rows := []string{"rapid tool", "Other", "API gateway", "a-p-i"}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"api", []string{"API gateway", "rapid tool", "a-p-i"}},
		{"API", []string{"API gateway"}},
		{"aPi", nil},
		// Space-separated terms all match, in any order; each term decides its own case.
		{"tool rap", []string{"rapid tool"}},
		{"gate A", []string{"API gateway"}},
		{"  api  zzz", nil},
	} {
		f, _ := feed(t, newFinder("h", rows, false, nil), typed(tc.query)...)
		if got := rowsOf(f); !slices.Equal(got, tc.want) {
			t.Errorf("%q: matches = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestQueryChangeResetsCursor(t *testing.T) {
	f := newFinder("h", []string{"alpha", "beta", "gamma"}, false, nil)
	f, _ = feed(t, f, key(tea.KeyDown), key(tea.KeyDown))
	if f.picked() != 2 {
		t.Fatalf("picked = %d, want 2", f.picked())
	}
	f, _ = feed(t, f, typed("a")...)
	if f.cursor != 0 {
		t.Errorf("cursor = %d after typing, want 0", f.cursor)
	}
	f, _ = feed(t, f, key(tea.KeyDown), key(tea.KeyBackspace))
	if f.cursor != 0 || len(f.query) != 0 {
		t.Errorf("cursor = %d, query = %q after backspace", f.cursor, string(f.query))
	}
	// Backspace on an empty query changes nothing, so the cursor stays.
	f, _ = feed(t, f, key(tea.KeyDown), key(tea.KeyBackspace))
	if f.cursor != 1 {
		t.Errorf("cursor = %d after no-op backspace, want 1", f.cursor)
	}
}

func TestProjectStepNoCycle(t *testing.T) {
	f := newFinder("Select project", []string{"a", "b"}, false, nil)
	f, _ = feed(t, f, key(tea.KeyUp))
	if f.picked() != 0 {
		t.Errorf("up at top picked %d, want 0", f.picked())
	}
	f, _ = feed(t, f, key(tea.KeyDown), key(tea.KeyCtrlN), key(tea.KeyCtrlJ))
	if f.picked() != 1 {
		t.Errorf("down past bottom picked %d, want 1", f.picked())
	}
}

func TestProjectStepEnter(t *testing.T) {
	rows := []string{"dev Go rewrite", "web site"}
	f, quit := feed(t, newFinder("Select project", rows, false, nil), append(typed("web"), key(tea.KeyEnter))...)
	if !quit || f.aborted || f.picked() != 1 {
		t.Errorf("quit=%v aborted=%v picked=%d, want web row", quit, f.aborted, f.picked())
	}

	f, quit = feed(t, newFinder("Select project", rows, false, nil), append(typed("zzz"), key(tea.KeyEnter))...)
	if !quit || f.picked() != -1 {
		t.Errorf("no match: quit=%v picked=%d, want -1", quit, f.picked())
	}

	for _, k := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC} {
		f, quit = feed(t, newFinder("Select project", rows, false, nil), key(k))
		if !quit || !f.aborted {
			t.Errorf("%v: quit=%v aborted=%v", k, quit, f.aborted)
		}
	}
}

func TestProjectView(t *testing.T) {
	f := projectFinder([]config.Project{
		{Name: "dev", Path: "~/dev", Description: "Go rewrite"},
		{Name: "web", Path: "~/web"},
	})
	f, _ = feed(t, f, tea.WindowSizeMsg{Width: 80, Height: 10}, key(tea.KeyDown))
	lines := strings.Split(f.View(), "\n")
	if !strings.HasPrefix(lines[0], "> ") || !strings.Contains(lines[1], "2/2") || !strings.Contains(lines[2], "Select project") {
		t.Errorf("chrome lines = %q", lines[:3])
	}
	if !strings.HasPrefix(lines[3], "  dev Go rewrite") || !strings.HasPrefix(lines[4], "> web") {
		t.Errorf("rows = %q", lines[3:5])
	}
	if !slices.Equal(f.rows, []string{"dev Go rewrite", "web"}) {
		t.Errorf("rows = %q, want no trailing space without a description", f.rows)
	}
	if !strings.Contains(f.View(), "Path: ~/web") || strings.Contains(f.View(), "Path: ~/dev") {
		t.Errorf("preview does not follow the cursor:\n%s", f.View())
	}
	if len(lines) != 10 {
		t.Errorf("view has %d lines, want the window height 10", len(lines))
	}
}

func TestScrollKeepsCursorVisible(t *testing.T) {
	f := newFinder("h", []string{"r0", "r1", "r2", "r3", "r4", "r5"}, true, nil)
	f, _ = feed(t, f, tea.WindowSizeMsg{Width: 40, Height: chromeLines + 2}, key(tea.KeyDown), key(tea.KeyDown), key(tea.KeyDown))
	if !strings.Contains(f.View(), "> r3") || strings.Contains(f.View(), "r1") {
		t.Errorf("view after scrolling down:\n%s", f.View())
	}
	f, _ = feed(t, f, key(tea.KeyUp), key(tea.KeyUp), key(tea.KeyUp), key(tea.KeyUp))
	if !strings.Contains(f.View(), "> r5") {
		t.Errorf("view after wrapping up:\n%s", f.View())
	}
}

func TestWorktreeStep(t *testing.T) {
	names := []string{"feature/one", "proj-12-fix"}
	for _, tc := range []struct {
		name       string
		msgs       []tea.Msg
		chosen     bool
		main       bool
		worktree   string
		wantPrompt bool
	}{
		{name: "main repo", msgs: []tea.Msg{key(tea.KeyEnter)}, chosen: true, main: true},
		{name: "existing worktree", msgs: []tea.Msg{key(tea.KeyDown), key(tea.KeyDown), key(tea.KeyEnter)}, chosen: true, worktree: "feature/one"},
		{name: "wraps up to last", msgs: []tea.Msg{key(tea.KeyUp), key(tea.KeyEnter)}, chosen: true, worktree: "proj-12-fix"},
		{name: "wraps down to first", msgs: []tea.Msg{key(tea.KeyUp), key(tea.KeyDown), key(tea.KeyEnter)}, chosen: true, main: true},
		{name: "no match is a new name", msgs: append(typed("zzz"), key(tea.KeyEnter)), chosen: true, worktree: "zzz"},
		{name: "query matching + new is the name", msgs: append(typed("new"), key(tea.KeyEnter)), chosen: true, worktree: "new"},
		{name: "query matching a worktree picks it", msgs: append(typed("feat"), key(tea.KeyEnter)), chosen: true, worktree: "feature/one"},
		// fzf smart-case and AND terms decide whether a typed name opens a row or creates a worktree.
		{name: "uppercase query skips main repo", msgs: append(typed("Main"), key(tea.KeyEnter)), chosen: true, worktree: "Main"},
		{name: "uppercase query skips lowercase worktree", msgs: append(typed("PROJ-12"), key(tea.KeyEnter)), chosen: true, worktree: "PROJ-12"},
		{name: "terms match in any order", msgs: append(typed("fix wt"), key(tea.KeyEnter)), chosen: true, worktree: "proj-12-fix"},
		{name: "esc", msgs: []tea.Msg{key(tea.KeyEsc)}},
		{name: "ctrl-c", msgs: append(typed("x"), key(tea.KeyCtrlC))},
		{name: "+ new with empty query prompts", msgs: []tea.Msg{key(tea.KeyDown), key(tea.KeyEnter)}, wantPrompt: true},
		{name: "prompted name", msgs: append([]tea.Msg{key(tea.KeyDown), key(tea.KeyEnter)}, append(typed("my wt"), key(tea.KeyEnter))...), chosen: true, worktree: "my wt"},
		{name: "prompt empty", msgs: []tea.Msg{key(tea.KeyDown), key(tea.KeyEnter), key(tea.KeyEnter)}},
		{name: "prompt esc", msgs: append([]tea.Msg{key(tea.KeyDown), key(tea.KeyEnter)}, append(typed("x"), key(tea.KeyEsc))...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, quit := feed(t, newWorktreeModel(names), tc.msgs...)
			if tc.wantPrompt {
				if quit || !m.prompting || !strings.HasPrefix(m.View(), "Worktree name: ") {
					t.Fatalf("quit=%v prompting=%v view=%q, want the name prompt", quit, m.prompting, m.View())
				}
				return
			}
			if !quit {
				t.Fatal("program did not quit")
			}
			if m.chosen != tc.chosen || m.main != tc.main || m.name != tc.worktree {
				t.Errorf("chosen=%v main=%v name=%q; want %v %v %q", m.chosen, m.main, m.name, tc.chosen, tc.main, tc.worktree)
			}
		})
	}
}

func TestWorktreeRows(t *testing.T) {
	m := newWorktreeModel([]string{"feature/one"})
	if got, want := m.list.rows, []string{"main repo", "+ new worktree", "wt: feature/one"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if !strings.Contains(m.View(), "Session mode — type a name to create a new worktree") {
		t.Errorf("header missing:\n%s", m.View())
	}
}
