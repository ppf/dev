package kanban

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/itemview"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// fixture is two lists: alpha with an ask, n own items and one on a worker; beta with one waiting on a person and one
// with a status no board knows.
func fixture(n int) []List {
	alpha := List{Boss: "boss-alpha", Items: []todo.Shown{
		{ID: 1, Text: "Merge #221 now or wait for the phone test?", Status: sidebar.StatusDecide, Options: []string{"Merge | now (Recommended)", "Wait"}},
	}}
	for i := range n {
		alpha.Items = append(alpha.Items, todo.Shown{ID: 10 + i, Text: fmt.Sprintf("Own item %d with a longer title that wraps 日本語", i), Status: sidebar.StatusYours})
	}
	alpha.Items = append(alpha.Items, todo.Shown{ID: 2, Text: "Fix the login loop", On: "alpha-fix", Session: &todo.Session{ID: "$9", Name: "alpha-fix"},
		Status: sidebar.StatusWorking, StatusOn: "alpha-fix", Link: "https://example.com/acme/demo/pull/88"})
	beta := List{Boss: "boss-beta", Items: []todo.Shown{
		{ID: 1, Text: "Review of the coupon change", On: "Carl", Status: sidebar.StatusWaiting, StatusOn: "Carl"},
		{ID: 2, Text: "Odd one", On: "x", Status: "mystery"},
		{ID: 3, Text: "Ticked", Done: true},
	}}
	return []List{alpha, beta}
}

type fakeActs struct {
	answered, ticked []string
	switched         string
}

func (f *fakeActs) acts() itemview.Actions {
	return itemview.Actions{
		Answer: func(boss string, id int, _, answer string) (string, error) {
			f.answered = append(f.answered, fmt.Sprintf("%s#%d=%s", boss, id, answer))
			return "answered " + boss, nil
		},
		Tick: func(boss string, id int, _ string) (string, error) {
			f.ticked = append(f.ticked, fmt.Sprintf("%s#%d", boss, id))
			return "ticked", nil
		},
		Start: func(string, int, string) (string, error) { return "started", nil },
		Open:  func(string) error { return nil },
		Switch: func(s string) error {
			f.switched = s
			return nil
		},
	}
}

func board(t *testing.T, lists []List, w, h int) (Model, *fakeActs) {
	t.Helper()
	f := &fakeActs{}
	m := New("all bosses", func() Loaded { return Loaded{Lists: lists, ManagerRuns: true} }, f.acts())
	m = step(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	return load(t, m, Loaded{Lists: lists, ManagerRuns: true}), f
}

// load is a read's outcome landing, as the latest read started.
func load(t *testing.T, m Model, l Loaded) Model {
	t.Helper()
	m.reads++
	return step(t, m, loadedMsg{l, m.reads})
}

func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func keys(t *testing.T, m Model, ks ...string) Model {
	t.Helper()
	for _, k := range ks {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m = step(t, m, msg)
	}
	return m
}

// run runs an action's command as the program would, feeding its message back.
func run(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func plain(m Model) string { return ansi.Strip(strings.Join(m.lines(time.Now()), "\n")) }

func TestLayout(t *testing.T) {
	for _, c := range []struct {
		popup, cols, shown int
	}{
		{320, 7, 7}, {200, 7, 7}, {190, 7, 7}, {189, 4, 4}, {120, 4, 4}, {109, 4, 4}, {108, 4, 3}, {90, 4, 3}, {82, 4, 3}, {81, 4, 2}, {55, 4, 2}, {54, 4, 1},
	} {
		m := Model{w: c.popup}
		cols, shown := layout(m.inner())
		if len(cols) != c.cols || shown != c.shown {
			t.Errorf("❌ %d columns wide: %d columns, %d shown; want %d, %d", c.popup, len(cols), shown, c.cols, c.shown)
		}
	}
}

// Every size from 80×24 up draws exactly w×h, the item view open or not, wide runes in the titles.
func TestViewFits(t *testing.T) {
	for _, w := range []int{80, 81, 82, 90, 100, 108, 109, 119, 120, 150, 189, 190, 200, 257, 320} {
		for _, h := range []int{24, 28, 40, 55, 75} {
			m, _ := board(t, fixture(12), w, h)
			for _, open := range []bool{false, true} {
				if open {
					m = keys(t, m, "enter")
				}
				lines := m.lines(time.Now())
				if len(lines) != h {
					t.Fatalf("❌ %d×%d open=%v: %d lines", w, h, open, len(lines))
				}
				for i, l := range lines {
					if got := ansi.StringWidth(l); got != w {
						t.Fatalf("❌ %d×%d open=%v: line %d is %d wide: %q", w, h, open, i, got, ansi.Strip(l))
					}
				}
			}
		}
	}
}

func TestColumns(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	out := plain(m)
	for _, want := range []string{"TO DO board · all bosses", "1 to decide · 2 yours · 6 open", "▸ To decide", "Yours", "Starting",
		"In progress", "Waiting on others", "Check", "Boss closed", "? alpha #1", "☐ alpha #10", "you · 2 options", "alpha-fix · PR #88",
		"beta #1", "Carl", "mystery", "project all  alpha  beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ %q missing:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Ticked") {
		t.Errorf("❌ a ticked item shows:\n%s", out)
	}
	// An unknown status is In progress, as the boards have it
	if got := m.columns()[3]; len(got) != 2 || got[1].it.Text != "Odd one" {
		t.Errorf("❌ In progress: %+v", got)
	}
	m, _ = board(t, fixture(2), 120, 40)
	out = plain(m)
	for _, want := range []string{"▸ For you", "Starting · In progress", "Check · Boss closed"} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ 120: %q missing:\n%s", want, out)
		}
	}
	m, _ = board(t, fixture(2), 90, 28)
	if out := plain(m); !strings.Contains(out, "columns 1–3 of 4 · h/l") || strings.Contains(out, "Check · Boss closed") {
		t.Errorf("❌ 90: want 3 of 4 columns:\n%s", out)
	}
}

func TestNavigation(t *testing.T) {
	m, _ := board(t, fixture(12), 90, 28)
	// For you: the ask then 12 own items; 3 fit
	if m.fits(13) != 3 {
		t.Fatalf("❌ fits %d", m.fits(13))
	}
	if out := plain(m); !strings.Contains(out, "+10 more") || strings.Contains(out, "↑") {
		t.Errorf("❌ top of the column:\n%s", out)
	}
	m = keys(t, m, "j", "j", "j", "j")
	if m.card != 4 || m.top != 2 {
		t.Errorf("❌ after 4×j: card %d top %d, want 4 2", m.card, m.top)
	}
	out := plain(m)
	if !strings.Contains(out, "↑ 2 more") || !strings.Contains(out, "+8 more") || !strings.Contains(out, "☐ alpha #13") {
		t.Errorf("❌ scrolled:\n%s", out)
	}
	m = keys(t, m, "k", "k", "k", "k", "k", "k")
	if m.card != 0 || m.top != 0 {
		t.Errorf("❌ back up: card %d top %d", m.card, m.top)
	}
	// l to the last merged column scrolls the columns; h back; neither goes past the ends
	m = keys(t, m, "l", "l", "l", "l")
	if m.col != 3 || m.off != 1 || !strings.Contains(plain(m), "columns 2–4 of 4") {
		t.Errorf("❌ at the right end: col %d off %d\n%s", m.col, m.off, plain(m))
	}
	m = keys(t, m, "h", "h", "h", "h", "h")
	if m.col != 0 || m.off != 0 {
		t.Errorf("❌ at the left end: col %d off %d", m.col, m.off)
	}
	// An empty column opens nothing
	m = keys(t, m, "l", "l", "l", "enter")
	if m.item != nil || !strings.Contains(plain(m), "No card in this column") {
		t.Errorf("❌ enter on an empty column:\n%s", plain(m))
	}
}

func TestFilter(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	m = keys(t, m, "l", "l", "l", "l") // Waiting on others: beta #1
	m = keys(t, m, "p")
	if m.project != "alpha" || strings.Contains(plain(m), "beta #1") {
		t.Errorf("❌ p: project %q\n%s", m.project, plain(m))
	}
	m = keys(t, m, "p")
	if m.project != "beta" || strings.Contains(plain(m), "alpha #1") {
		t.Errorf("❌ p p: project %q", m.project)
	}
	m = keys(t, m, "p")
	if m.project != "" {
		t.Errorf("❌ p p p: project %q, want all", m.project)
	}
	// A list gone from the read drops its filter
	m = keys(t, m, "p", "p")
	m = load(t, m, Loaded{Lists: fixture(2)[:1]})
	if m.project != "" {
		t.Errorf("❌ filter on a list gone: %q", m.project)
	}
}

func TestItemView(t *testing.T) {
	m, f := board(t, fixture(2), 120, 40)
	m = keys(t, m, "enter")
	if m.item == nil || !strings.Contains(plain(m), "alpha #1 · To decide") || !strings.Contains(plain(m), "★ Merge") {
		t.Fatalf("❌ the item view not over the board:\n%s", plain(m))
	}
	m, cmd := run(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, cmd = run(t, m, cmd())
	if m.item != nil || len(f.answered) != 1 || f.answered[0] != "boss-alpha#1=Wait" {
		t.Fatalf("❌ 2 answered %v, view open %v", f.answered, m.item != nil)
	}
	if !strings.Contains(plain(m), "answered boss-alpha") || cmd == nil {
		t.Errorf("❌ the outcome not shown, or no read after it:\n%s", plain(m))
	}
	// The read after it moves the selection to what's left
	rest := fixture(2)
	rest[0].Items = rest[0].Items[1:]
	read, ok := cmd().(loadedMsg)
	if !ok {
		t.Fatal("❌ the command after an action isn't a read")
	}
	read.Lists = rest
	m = step(t, m, read)
	if c := m.columns()[m.col][m.card]; c.it.ID != 10 {
		t.Errorf("❌ selection after the read: %+v", c.it)
	}

	// space opens an own item; d ticks it
	m = keys(t, m, "j", " ")
	m, cmd = run(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m, _ = run(t, m, cmd())
	if len(f.ticked) != 1 || f.ticked[0] != "boss-alpha#11" || m.item != nil {
		t.Errorf("❌ d ticked %v", f.ticked)
	}

	// esc closes the view, then the board
	m = keys(t, m, "enter", "esc")
	if m.item != nil || m.Done {
		t.Fatalf("❌ esc: view open %v, board done %v", m.item != nil, m.Done)
	}
	m = keys(t, m, "esc")
	if !m.Done {
		t.Error("❌ second esc left the board open")
	}
}

// A switch from the item view closes the board: the client is on the session now.
func TestSwitchCloses(t *testing.T) {
	m, f := board(t, fixture(2), 200, 55)
	m = keys(t, m, "l", "l", "l", "enter") // In progress: alpha-fix's item
	m, cmd := run(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m, _ = run(t, m, cmd())
	if f.switched != "alpha-fix" || !m.Done {
		t.Errorf("❌ w: switched %q, board done %v", f.switched, m.Done)
	}
}

func TestMessages(t *testing.T) {
	f := &fakeActs{}
	m := New("boss-alpha and its workers", nil, f.acts())
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = load(t, m, Loaded{Lists: []List{}})
	if out := plain(m); !strings.Contains(out, "No open items") || !strings.Contains(out, "TO DO board · boss-alpha and its workers") {
		t.Errorf("❌ empty:\n%s", out)
	}
	m = load(t, m, Loaded{Lists: []List{{Boss: "boss-x", Error: "list not read: bad"}}, Warn: "⚠ workers not read"})
	if out := plain(m); !strings.Contains(out, "⚠ boss-x: list not read: bad · ⚠ workers not read") {
		t.Errorf("❌ errors:\n%s", out)
	}
	m = load(t, m, Loaded{Err: errors.New("no server")})
	if out := plain(m); !strings.Contains(out, "❌ lists not read: no server") {
		t.Errorf("❌ read failed:\n%s", out)
	}
}

// A read that failed keeps the lists last read and the filter on them; a good one clears the error.
func TestReadFailedKeepsBoard(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	m = keys(t, m, "p", "l")
	m = load(t, m, Loaded{Err: errors.New("no server")})
	out := plain(m)
	if m.project != "alpha" || !strings.Contains(out, "☐ alpha #10") || !strings.Contains(out, "❌ lists not read: no server") || m.col != 1 {
		t.Errorf("❌ after a failed read: project %q col %d\n%s", m.project, m.col, out)
	}
	m = load(t, m, Loaded{Lists: fixture(2)})
	if strings.Contains(plain(m), "❌") {
		t.Errorf("❌ the error stays after a good read:\n%s", plain(m))
	}
}

// A read that started before the latest one and lands after it is dropped.
func TestStaleReadDropped(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	m.reads += 2
	m = step(t, m, loadedMsg{Loaded{Lists: fixture(0)}, m.reads})
	m = step(t, m, loadedMsg{Loaded{Lists: fixture(2)}, m.reads - 1})
	if strings.Contains(plain(m), "alpha #10") {
		t.Errorf("❌ an older read replaced a newer one:\n%s", plain(m))
	}
}

// The selection stays on its card across reads: a card added above it, or its move to another column.
func TestSelectionKept(t *testing.T) {
	m, _ := board(t, fixture(3), 200, 55)
	m = keys(t, m, "l", "j") // Yours: alpha #11
	lists := fixture(3)
	lists[0].Items = append([]todo.Shown{{ID: 30, Text: "New", Status: sidebar.StatusYours}}, lists[0].Items...)
	m = load(t, m, Loaded{Lists: lists})
	if c := m.columns()[m.col][m.card]; c.it.ID != 11 {
		t.Errorf("❌ after a card above: selected #%d, want #11", c.it.ID)
	}
	for i, it := range lists[0].Items {
		if it.ID == 11 {
			lists[0].Items[i].Status, lists[0].Items[i].StatusOn, lists[0].Items[i].On = sidebar.StatusWaiting, "Carl", "Carl"
		}
	}
	m = load(t, m, Loaded{Lists: lists})
	if c := m.columns()[m.col][m.card]; c.it.ID != 11 || m.col != 4 {
		t.Errorf("❌ after its move: column %d #%d, want Waiting on others #11", m.col, c.it.ID)
	}
}

// A switch that failed leaves the board open with why.
func TestSwitchFailed(t *testing.T) {
	f := &fakeActs{}
	acts := f.acts()
	acts.Switch = func(string) error { return errors.New("can't find session") }
	m := New("all bosses", nil, acts)
	m = step(t, m, tea.WindowSizeMsg{Width: 200, Height: 55})
	m = load(t, m, Loaded{Lists: fixture(2)})
	m = keys(t, m, "l", "l", "l", "enter")
	m, cmd := run(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m, _ = run(t, m, cmd())
	if m.Done || m.item == nil || !strings.Contains(plain(m), "can't find session") {
		t.Errorf("❌ failed switch: board done %v, view open %v\n%s", m.Done, m.item != nil, plain(m))
	}
}

// The merged columns hold every group once.
func TestMergedCoverGroups(t *testing.T) {
	seen := map[int]int{}
	for _, c := range merged {
		for _, g := range c.groups {
			seen[g]++
		}
	}
	for g := range sidebar.Groups {
		if seen[g] != 1 {
			t.Errorf("❌ group %q in %d merged columns", sidebar.Groups[g].Title, seen[g])
		}
	}
}

// A long project is cut inside its card, the number kept; projects too many for the filter line show the one picked.
func TestLongNames(t *testing.T) {
	lists := fixture(1)
	for i := range 6 {
		lists = append(lists, List{Boss: fmt.Sprintf("boss-nocworx-core-ssl-ma-demo-%d", i), Items: []todo.Shown{{ID: 12, Text: "Long", Status: sidebar.StatusYours}}})
	}
	m, _ := board(t, lists, 120, 40)
	if out := plain(m); !strings.Contains(out, "project all (1 of 9, p next)") {
		t.Errorf("❌ the filter line:\n%s", out)
	}
	m = keys(t, m, "p", "p", "p")
	for i, l := range m.lines(time.Now()) {
		if got := ansi.StringWidth(l); got != 120 {
			t.Fatalf("❌ line %d is %d wide", i, got)
		}
	}
	out := plain(m)
	if !strings.Contains(out, "project nocworx-core-ssl-ma-demo-0 (4 of 9, p next)") || !strings.Contains(out, "☐ nocworx-core-ssl-ma-… #12") {
		t.Errorf("❌ a long project:\n%s", out)
	}
}
