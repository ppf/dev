package kanban

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/itemview"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// fixture is two lists, as the model places them: alpha with an ask (To do, needing the user), n own items (To do), one on a
// worker (In progress); beta with one waiting on a person (Waiting), one with no column (In progress) and a ticked one (Done).
func fixture(n int) []List {
	you := todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}
	ask := you
	ask.NeedsYou = true
	alpha := List{Boss: "boss-alpha", Items: []todo.Shown{
		{ID: 1, Text: "Merge #221 now or wait for the phone test?", Status: sidebar.StatusDecide, Options: []string{"Merge | now (Recommended)", "Wait"}, Placement: ask},
	}}
	for i := range n {
		alpha.Items = append(alpha.Items, todo.Shown{ID: 10 + i, Text: fmt.Sprintf("Own item %d with a longer title that wraps 日本語", i), Status: sidebar.StatusYours, Placement: you})
	}
	alpha.Items = append(alpha.Items, todo.Shown{ID: 2, Text: "Fix the login loop", On: "alpha-fix", Session: &todo.Session{ID: "$9", Name: "alpha-fix"},
		Status: sidebar.StatusWorking, StatusOn: "alpha-fix", Link: "https://example.com/acme/demo/pull/88",
		Placement: todo.Placement{Column: sidebar.ColumnProgress, Assignee: todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "alpha-fix", Running: true}}})
	beta := List{Boss: "boss-beta", Items: []todo.Shown{
		{ID: 1, Text: "Review of the coupon change", On: "Carl", Status: sidebar.StatusWaiting, StatusOn: "Carl",
			Placement: todo.Placement{Column: sidebar.ColumnWaiting, WaitingFor: "Carl", Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-beta", Running: true}}},
		{ID: 2, Text: "Odd one", On: "x", Status: "mystery"},
		{ID: 3, Text: "Ticked", Done: true, Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}},
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
		Start:  func(string, int, string) (string, error) { return "started", nil },
		Reopen: func(string, int, string) (string, error) { return "reopened", nil },
		Doing:  func(string, int, string, string, string, bool) (string, error) { return "in progress", nil },
		Open:   func(string) error { return nil },
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
	for _, c := range []struct{ popup, shown int }{
		{320, 5}, {200, 5}, {136, 5}, {135, 4}, {109, 4}, {108, 3}, {82, 3}, {81, 2}, {55, 2}, {54, 1},
	} {
		m := Model{w: c.popup}
		if got := fitCols(m.inner()); got != c.shown {
			t.Errorf("❌ %d columns wide: %d columns shown, want %d", c.popup, got, c.shown)
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

// Test items count in none of the top line's numbers and say TEST on their cards.
func TestColumnsQuiet(t *testing.T) {
	ls := fixture(2)
	you := todo.Placement{Column: sidebar.ColumnTodo, NeedsYou: true, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}
	ls[0].Items = append(ls[0].Items, todo.Shown{ID: 50, Text: "T?", Status: sidebar.StatusDecide, Options: []string{"a", "b"}, Quiet: true, Placement: you},
		todo.Shown{ID: 51, Text: "T", Status: sidebar.StatusYours, Quiet: true, Placement: you})
	m, _ := board(t, ls, 200, 55)
	if out := plain(m); !strings.Contains(out, "1 need you · 3 mine") || !strings.Contains(out, "TEST") {
		t.Errorf("❌ quiet items counted or untagged:\n%s", out)
	}
}

// A list with only finished items (items empty) shows its kept ones in Done, a ticked one still in items too, each once.
func TestColumnsFinishedOnly(t *testing.T) {
	you := todo.Assignee{Kind: sidebar.AssigneeYou}
	ls := append(fixture(2), List{Boss: "boss-gamma", Items: []todo.Shown{}, Finished: []todo.Finished{{ID: 1, Text: "Kept", Assignee: you, DoneAt: 100}, {ID: 2, Text: "Newer", Assignee: you, DoneAt: 200}}})
	m, _ := board(t, ls, 200, 55)
	done := m.columns()[4]
	var got []string
	for _, c := range done {
		got = append(got, c.it.Text)
	}
	// The ticked one still in its list has no time yet: the newest
	if want := []string{"Ticked", "Newer", "Kept"}; !slices.Equal(got, want) {
		t.Errorf("❌ Done = %v, want %v", got, want)
	}
	if out := plain(m); !strings.Contains(out, "gamma #2") || strings.Contains(out, "No open items") {
		t.Errorf("❌ finished items:\n%s", out)
	}
	only, _ := board(t, ls[2:], 200, 55)
	if n := len(only.cards()); n != 2 || strings.Contains(plain(only), "No card") {
		t.Errorf("❌ a list with only finished items: %d cards", n)
	}
	// Open or finished, never mixed up: ids restart
	if a, b := (card{boss: "boss-x", it: todo.Shown{ID: 3}}), (card{boss: "boss-x", it: todo.Shown{ID: 3}, done: true, doneAt: 5}); a.key() == b.key() {
		t.Error("❌ an open #3 and a finished #3 share a key")
	}
}

// A card's project label and the filter line's projects are in the project's colour, the shown one bold; "all" and the
// marks keep theirs.
func TestProjectColours(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	out := strings.Join(m.lines(time.Now()), "\n")
	alpha, beta := sidebar.ProjectColourOf("alpha").ANSI(), sidebar.ProjectColourOf("beta").ANSI()
	for _, want := range []string{
		sidebar.Muted + "project " + reset + bold + "all" + reset + "  " + alpha + "alpha" + reset + "  " + beta + "beta" + reset,
		// The selected card: its band follows each reset
		sidebar.Red + bold + "!" + reset + sidebar.SelectedBG + " " + alpha + "alpha #1" + reset,
		alpha + "alpha #2" + reset,
		beta + "beta #1" + reset,
		sidebar.Gold + "waiting for Carl" + reset,
		sidebar.Muted + "@boss-beta",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ %q missing:\n%s", want, out)
		}
	}
	m = keys(t, m, "p")
	if out := strings.Join(m.lines(time.Now()), "\n"); !strings.Contains(out, sidebar.Muted+"all"+reset+"  "+bold+alpha+"alpha"+reset) {
		t.Errorf("❌ p: alpha not bold in its colour:\n%q", out)
	}
	// A desktop app boss's card and filter name take desktop's colour, as its dot on the page; a project named local_… its own
	m, _ = board(t, append(fixture(0), List{Boss: "local_ab12", Items: []todo.Shown{{ID: 4, Text: "Desk", Status: sidebar.StatusYours,
		Placement: todo.Placement{Column: sidebar.ColumnWaiting}}}}, List{Boss: "boss-local_api", Items: []todo.Shown{{ID: 5, Text: "Api",
		Status: sidebar.StatusYours, Placement: todo.Placement{Column: sidebar.ColumnWaiting}}}}), 200, 55)
	desk, api := sidebar.ProjectColourOf("desktop").ANSI(), sidebar.ProjectColourOf("local_api").ANSI()
	out = strings.Join(m.lines(time.Now()), "\n")
	for _, want := range []string{desk + "local_ab12 #4" + reset, desk + "local_ab12" + reset, api + "local_api #5" + reset, api + "local_api" + reset} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ %q missing:\n%q", want, out)
		}
	}
	if desk == api {
		t.Fatal("❌ the case needs desktop and local_api apart")
	}
}

func TestColumns(t *testing.T) {
	m, _ := board(t, fixture(2), 200, 55)
	out := plain(m)
	for _, want := range []string{"TO DO board · all bosses", "1 need you · 3 mine", "▸ To do", "In progress", "Waiting", "Review", "Done",
		"! alpha #1", "decide", "@you", "2 options", "alpha #2", "@alpha-fix", "PR #88", "beta #1", "@boss-beta", "waiting for Carl", "Odd one",
		"Ticked", "project all  alpha  beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ %q missing:\n%s", want, out)
		}
	}
	// The model's column and nothing else: an ask is in To do, an unknown column In progress, a ticked item Done
	want := [][]string{{"Merge", "Own item 0", "Own item 1"}, {"Fix the login", "Odd one"}, {"Review of"}, {}, {"Ticked"}}
	for i, cs := range m.columns() {
		if len(cs) != len(want[i]) {
			t.Fatalf("❌ column %d holds %d cards, want %d", i, len(cs), len(want[i]))
		}
		for j, c := range cs {
			if !strings.HasPrefix(c.it.Text, want[i][j]) {
				t.Errorf("❌ column %d card %d is %q, want %q", i, j, c.it.Text, want[i][j])
			}
		}
	}
	m, _ = board(t, fixture(2), 120, 40)
	if out := plain(m); !strings.Contains(out, "columns 1–4 of 5 · h/l") || strings.Contains(out, "Done  ") {
		t.Errorf("❌ 120: want 4 of 5 columns:\n%s", out)
	}
	m, _ = board(t, fixture(2), 90, 28)
	if out := plain(m); !strings.Contains(out, "columns 1–3 of 5 · h/l") || strings.Contains(out, "Done") {
		t.Errorf("❌ 90: want 3 of 5 columns:\n%s", out)
	}
	m, _ = board(t, fixture(2), 140, 30)
	if out := plain(m); strings.Contains(out, "h/l") && strings.Contains(out, "columns") {
		t.Errorf("❌ 140: all five columns fit:\n%s", out)
	}
}

// The columns sort as the web page's: what needs the user first, the user's first in To do, the soonest due, then the
// oldest; Done newest first.
func TestColumnsOrder(t *testing.T) {
	boss := todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-alpha"}}
	you := todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}
	need := you
	need.NeedsYou = true
	ls := []List{{Boss: "boss-alpha", Items: []todo.Shown{
		{ID: 1, Text: "old boss's", Added: 10, Placement: boss},
		{ID: 2, Text: "newer boss's", Added: 20, Placement: boss},
		{ID: 3, Text: "your old", Added: 5, Placement: you},
		{ID: 4, Text: "due soon", Added: 30, Until: 500, Placement: you},
		{ID: 5, Text: "needs you", Added: 40, Placement: need},
		{ID: 6, Text: "due later", Added: 1, Until: 900, Placement: you},
	}}}
	m, _ := board(t, ls, 200, 55)
	var got []string
	for _, c := range m.columns()[0] {
		got = append(got, c.it.Text)
	}
	if want := []string{"needs you", "due soon", "due later", "your old", "old boss's", "newer boss's"}; !slices.Equal(got, want) {
		t.Errorf("❌ To do = %v, want %v", got, want)
	}
}

// A card shows what the web page's does: the start, the PR, the worker; the chips; what is done on it now; a red column head.
func TestCardMarks(t *testing.T) {
	now := time.Unix(1700000000, 0)
	render := func(c card, cw int) string { return ansi.Strip(strings.Join(cardLines(c, false, cw, now), "\n")) }
	base := todo.Shown{ID: 7, Text: "A task"}
	for _, c := range []struct {
		name string
		c    card
		want []string
		not  []string
	}{
		{"started", mk(base, func(s *todo.Shown) {
			s.Started = &todo.Started{To: "boss-x", At: now.Add(-3 * time.Minute).Unix()}
			s.Column = sidebar.ColumnProgress
		}),
			[]string{"starting · boss-x · 3m"}, nil},
		{"late start", mk(base, func(s *todo.Shown) {
			s.Started, s.StartLate, s.NeedsYou = &todo.Started{To: "boss-x", At: 1}, true, true
		}), []string{"start not picked up", "! "}, []string{"needs you"}},
		{"doing", mk(base, func(s *todo.Shown) { s.Doing = &todo.Doing{Text: "reading the log"} }), []string{"▸ reading the log"}, nil},
		{"decide", mk(base, func(s *todo.Shown) { s.Status, s.NeedsYou = sidebar.StatusDecide, true }), []string{"! ", "decide"}, []string{"needs you"}},
		{"needs you", mk(base, func(s *todo.Shown) { s.NeedsYou, s.Status = true, sidebar.StatusCheck }), []string{"needs you"}, nil},
		{"blocked", mk(base, func(s *todo.Shown) { s.Status, s.NeedsYou = sidebar.StatusBlocked, true }), []string{"blocked · needs you"}, []string{"· needs you ·"}},
		{"asks its boss", mk(base, func(s *todo.Shown) {
			s.Status, s.Now, s.Assignee = sidebar.StatusAsks, "A or B?", todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "w"}
		}), []string{"asks its boss", "▸ A or B?"}, []string{"!", "needs you"}},
		{"parked", mk(base, func(s *todo.Shown) {
			s.Status, s.On, s.Parked, s.Assignee = sidebar.StatusParked, todo.Boss, true, todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-x"}
		}), []string{"▸ parked · boss-x has not taken it up"}, nil},
		{"gone", mk(base, func(s *todo.Shown) { s.Status, s.On = sidebar.StatusGone, "dev-x" }), []string{"worker dev-x gone"}, nil},
		{"PR moved", mk(base, func(s *todo.Shown) {
			s.Check = &sidebar.Check{PR: 18, Kind: sidebar.CheckMerged, Since: now.Add(-72 * time.Hour).Unix()}
		}), []string{"#18 merged 3d"}, nil},
		{"unread", mk(base, func(s *todo.Shown) { s.Assignee = todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "w"} }), []string{"?", "@w"}, nil},
		{"worker word", mk(base, func(s *todo.Shown) {
			s.Status, s.Assignee = sidebar.StatusIdle, todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "w"}
		}), []string{"idle", "@w"}, nil},
		{"waiting for", mk(base, func(s *todo.Shown) { s.WaitingFor, s.Status = "Carl", sidebar.StatusWaiting }), []string{"waiting for Carl"}, []string{"no one follows up"}},
		{"part of", mk(base, func(s *todo.Shown) { s.PartOf = 9 }), []string{"⇄ #9"}, nil},
		{"ticked", card2(mk(base, func(s *todo.Shown) { s.Done, s.NeedsYou = true, true })), nil, []string{"!", "decide"}},
	} {
		out := render(c.c, 60)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("❌ %s: %q missing:\n%s", c.name, w, out)
			}
		}
		for _, w := range c.not {
			if strings.Contains(out, w) {
				t.Errorf("❌ %s: %q shows:\n%s", c.name, w, out)
			}
		}
	}
	// A closed boss's card says so, and a person it waits for is no one's to follow up
	closed := mk(base, func(s *todo.Shown) { s.WaitingFor = "Carl" })
	closed.closed = true
	if out := render(closed, 80); !strings.Contains(out, "boss closed") || !strings.Contains(out, "waiting for Carl · no one follows up") {
		t.Errorf("❌ closed boss's card:\n%s", out)
	}
	// An open item waiting on a person names who follows up; a closed boss's names no one, and a ticked one keeps its assignee
	boss := mk(base, func(s *todo.Shown) {
		s.WaitingFor, s.Assignee = "Carl", todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-beta"}
	})
	if out := render(boss, 80); !strings.Contains(out, "waiting for Carl · @boss-beta follows up") {
		t.Errorf("❌ live boss's waiting card:\n%s", out)
	}
	closedBoss := boss
	closedBoss.closed = true
	if out := render(closedBoss, 80); !strings.Contains(out, "waiting for Carl · no one follows up") || strings.Contains(out, "@boss-beta") {
		t.Errorf("❌ closed boss's waiting card:\n%s", out)
	}
	worker := mk(base, func(s *todo.Shown) {
		s.WaitingFor, s.Assignee = "Carl", todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "beta-fix"}
	})
	if out := render(worker, 80); !strings.Contains(out, "@beta-fix") || strings.Contains(out, "follows up @") {
		t.Errorf("❌ worker's waiting card:\n%s", out)
	}
	boss.done = true
	if out := render(boss, 80); strings.Contains(out, "follows up") || !strings.Contains(out, "@boss-beta") {
		t.Errorf("❌ ticked waiting card:\n%s", out)
	}
	// Every card is cardH lines of cw
	for _, l := range cardLines(closed, true, 30, now) {
		if ansi.StringWidth(l) != 30 {
			t.Errorf("❌ card line %q is %d wide", ansi.Strip(l), ansi.StringWidth(l))
		}
	}
	if n := len(cardLines(closed, false, 30, now)); n != cardH {
		t.Errorf("❌ a card is %d lines, want %d", n, cardH)
	}
	// A column with a card that needs the user has a red head
	m, _ := board(t, fixture(1), 200, 55)
	if head := strings.Split(strings.Join(m.lines(now), "\n"), "\n")[3]; !strings.Contains(head, sidebar.Red+"▸ To do") && !strings.Contains(head, bold+sidebar.Red) {
		t.Errorf("❌ To do's head isn't red: %q", head)
	}
}

func mk(s todo.Shown, mod func(*todo.Shown)) card {
	mod(&s)
	return card{boss: "boss-alpha", it: s}
}

func card2(c card) card {
	c.done = true
	return c
}

func TestNavigation(t *testing.T) {
	m, _ := board(t, fixture(12), 90, 28)
	// To do: the ask then 12 own items; at 28 high 3 fit
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
	if !strings.Contains(out, "↑ 2 more") || !strings.Contains(out, "+8 more") || !strings.Contains(out, "alpha #13") {
		t.Errorf("❌ scrolled:\n%s", out)
	}
	m = keys(t, m, "k", "k", "k", "k", "k", "k")
	if m.card != 0 || m.top != 0 {
		t.Errorf("❌ back up: card %d top %d", m.card, m.top)
	}
	// l to the last column scrolls the columns; h back; neither goes past the ends
	m = keys(t, m, "l", "l", "l", "l", "l")
	if m.col != 4 || m.off != 2 || !strings.Contains(plain(m), "columns 3–5 of 5") {
		t.Errorf("❌ at the right end: col %d off %d\n%s", m.col, m.off, plain(m))
	}
	m = keys(t, m, "h", "h", "h", "h", "h", "h")
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
	m = keys(t, m, "l", "l") // Waiting: beta #1
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
	if m.item == nil || !strings.Contains(plain(m), "alpha #1 · To do") || !strings.Contains(plain(m), "★ Merge") {
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

// ⏎ on a Done card opens it with what its list kept: what was done, when and by whom, details, context.
func TestDoneItemView(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	lists := []List{{Boss: "boss-vitay", Finished: []todo.Finished{{ID: 117, Text: "Send the reminder", Result: "Mail sent 12:40", Details: "Draft ready, not sent",
		Context: []string{"template: Mahnung 2"}, DoneBy: "boss-vitay", DoneAt: at.Unix(), Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}}
	m, _ := board(t, lists, 120, 40)
	m = keys(t, m, "l", "l", "l", "l", "enter")
	v := plain(m)
	clock := at.Format("15:04")
	if at.YearDay() != time.Now().YearDay() {
		clock = at.Format("Jan 2 15:04") // past midnight: a tick from yesterday says its date
	}
	for _, want := range []string{"vitay #117 · Done", "what was done", "Mail sent 12:40", "Draft ready, not sent", "template: Mahnung 2", "ticked  " + clock + " · by boss-vitay"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
}

// A switch from the item view closes the board: the client is on the session now.
func TestSwitchCloses(t *testing.T) {
	m, f := board(t, fixture(2), 200, 55)
	m = keys(t, m, "l", "enter") // In progress: alpha-fix's item
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
	if m.project != "alpha" || !strings.Contains(out, "alpha #10") || !strings.Contains(out, "❌ lists not read: no server") || m.col != 1 {
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
	m = keys(t, m, "j", "j") // To do: alpha #11
	lists := fixture(3)
	lists[0].Items = append([]todo.Shown{{ID: 30, Text: "New", Status: sidebar.StatusYours, Placement: todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}, lists[0].Items...)
	m = load(t, m, Loaded{Lists: lists})
	if c := m.columns()[m.col][m.card]; c.it.ID != 11 {
		t.Errorf("❌ after a card above: selected #%d, want #11", c.it.ID)
	}
	for i, it := range lists[0].Items {
		if it.ID == 11 {
			lists[0].Items[i].Status, lists[0].Items[i].StatusOn, lists[0].Items[i].On = sidebar.StatusWaiting, "Carl", "Carl"
			lists[0].Items[i].Placement = todo.Placement{Column: sidebar.ColumnWaiting, WaitingFor: "Carl", Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-alpha"}}
		}
	}
	m = load(t, m, Loaded{Lists: lists})
	if c := m.columns()[m.col][m.card]; c.it.ID != 11 || m.col != 2 {
		t.Errorf("❌ after its move: column %d #%d, want Waiting #11", m.col, c.it.ID)
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
	m = keys(t, m, "l", "enter")
	m, cmd := run(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m, _ = run(t, m, cmd())
	if m.Done || m.item == nil || !strings.Contains(plain(m), "can't find session") {
		t.Errorf("❌ failed switch: board done %v, view open %v\n%s", m.Done, m.item != nil, plain(m))
	}
}

// A long project is cut inside its card, the number kept; projects too many for the filter line show the one picked.
func TestLongNames(t *testing.T) {
	lists := fixture(1)
	for i := range 6 {
		lists = append(lists, List{Boss: fmt.Sprintf("boss-nocworx-core-ssl-ma-demo-%d", i), Items: []todo.Shown{{ID: 12, Text: "Long", Status: sidebar.StatusYours, Placement: todo.Placement{Column: sidebar.ColumnTodo}}}})
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
	if raw := strings.Join(m.lines(time.Now()), "\n"); !strings.Contains(raw, bold+sidebar.ProjectColourOf("nocworx").ANSI()+"nocworx-core-ssl-ma-demo-0"+reset) {
		t.Errorf("❌ the cut line's project not bold in its colour:\n%q", raw)
	}
	out := plain(m)
	if !strings.Contains(out, "project nocworx-core-ssl-ma-demo-0 (4 of 9, p next)") || !strings.Contains(out, "nocworx-core-ssl-ma-de… #12") {
		t.Errorf("❌ a long project:\n%s", out)
	}
}

// at is where s first shows on the board: its cell column and row.
func at(t *testing.T, m Model, s string) (int, int) {
	t.Helper()
	for y, l := range strings.Split(plain(m), "\n") {
		if i := strings.Index(l, s); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("❌ %q not on the board:\n%s", s, plain(m))
	return 0, 0
}

// clock is the clicks' time: a second apart, past the double-click guard.
var clock = time.Now()

// press is a mouse button at x, y, the action's command run as the program would.
func press(t *testing.T, m Model, b tea.MouseButton, x, y int) Model {
	t.Helper()
	clock = clock.Add(time.Second)
	next, cmd := m.mouse(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}, clock)
	m = next.(Model)
	if cmd != nil && m.item != nil {
		if msg := cmd(); msg != nil {
			next, _ = m.Update(msg)
			m = next.(Model)
		}
	}
	return m
}

func clickOn(t *testing.T, m Model, s string) Model {
	t.Helper()
	x, y := at(t, m, s)
	return press(t, m, tea.MouseButtonLeft, x, y)
}

// A click on a card opens it at every layout, the columns scrolled too; one on a head or a gap selects the column.
func TestClickCard(t *testing.T) {
	for _, c := range []struct {
		w     int
		keys  []string
		card  string
		title string
		col   int
	}{
		{200, nil, "Review of the coupon", "beta #1 · Waiting", 2},
		{120, nil, "Review of the coupon", "beta #1 · Waiting", 2},
		{90, nil, "Fix the login", "alpha #2 · In progress", 1},
		{60, nil, "alpha #2", "alpha #2 · In progress", 1},
		{50, nil, "Own item 1 with", "alpha #11 · To do", 0},
		{90, []string{"l", "l", "l", "l"}, "Review of the coupon", "beta #1 · Waiting", 2}, // columns 3–5 shown
		{50, []string{"l", "l"}, "beta #1", "beta #1 · Waiting", 2},
	} {
		m, _ := board(t, fixture(2), c.w, 40)
		m = keys(t, m, c.keys...)
		m = clickOn(t, m, c.card)
		if m.item == nil || m.col != c.col || !strings.Contains(plain(m), c.title) {
			t.Errorf("❌ %d wide %v, click on %q: col %d, want %d; view open %v\n%s", c.w, c.keys, c.card, m.col, c.col, m.item != nil, plain(m))
		}
	}
	m, _ := board(t, fixture(2), 120, 40)
	m = clickOn(t, m, "Waiting")
	if m.item != nil || m.col != 2 || m.card != 0 {
		t.Errorf("❌ click on a head: col %d card %d, view open %v", m.col, m.card, m.item != nil)
	}
	x, y := at(t, m, "Waiting")
	m = press(t, m, tea.MouseButtonLeft, x, y+10) // below its only card
	if m.item != nil || m.col != 2 {
		t.Errorf("❌ click under a column's cards: col %d, view open %v", m.col, m.item != nil)
	}
	x, _ = at(t, m, "▸ Waiting")
	m = press(t, m, tea.MouseButtonLeft, x-2, y+3) // the rule between two columns
	if m.item != nil || m.col != 2 {
		t.Errorf("❌ click on a rule: col %d, view open %v", m.col, m.item != nil)
	}
	m = press(t, m, tea.MouseButtonRight, x, y)
	m = press(t, m, tea.MouseButtonLeft, x, len(strings.Split(plain(m), "\n"))-2) // the keys line
	if m.item != nil || m.col != 2 {
		t.Error("❌ a right click or one on the keys acted")
	}
}

// The wheel moves the selection in the column under it, the column scrolling with it; "+n more" and "↑ n more" page.
func TestClickScroll(t *testing.T) {
	m, _ := board(t, fixture(12), 90, 28)
	x, y := at(t, m, "To do")
	for range 4 {
		m = press(t, m, tea.MouseButtonWheelDown, x, y+3)
	}
	if m.card != 4 || m.top != 2 || !strings.Contains(plain(m), "↑ 2 more") || m.item != nil {
		t.Errorf("❌ 4 wheel downs: card %d top %d\n%s", m.card, m.top, plain(m))
	}
	m = press(t, m, tea.MouseButtonWheelUp, x, y+3)
	if m.card != 3 {
		t.Errorf("❌ wheel up: card %d", m.card)
	}
	wx, wy := at(t, m, "In progress")
	m = press(t, m, tea.MouseButtonWheelDown, wx, wy+3)
	if m.col != 1 || m.card != 0 {
		t.Errorf("❌ wheel over another column: col %d card %d, want 1 0", m.col, m.card)
	}

	m, _ = board(t, fixture(12), 90, 28)
	m = clickOn(t, m, "+10 more")
	if m.card != 3 || m.top != 3 || m.item != nil || !strings.Contains(plain(m), "↑ 3 more") {
		t.Errorf("❌ +10 more: card %d top %d\n%s", m.card, m.top, plain(m))
	}
	m = clickOn(t, m, "↑ 3 more")
	if m.card != 2 || m.top != 0 || m.item != nil {
		t.Errorf("❌ ↑ 3 more: card %d top %d", m.card, m.top)
	}
	// "+n more" of a column not selected selects it, paged
	lists := fixture(0)
	for i := range 6 {
		lists[1].Items = append(lists[1].Items, todo.Shown{ID: 20 + i, Text: "Waiting", On: "Dora", Status: sidebar.StatusWaiting, Placement: todo.Placement{Column: sidebar.ColumnWaiting}})
	}
	m, _ = board(t, lists, 90, 28)
	m = clickOn(t, m, "+4 more")
	if m.col != 2 || m.card != 3 || m.top != 3 {
		t.Errorf("❌ +4 more of Waiting: col %d card %d top %d", m.col, m.card, m.top)
	}
}

// A click on a project shows it, on all shows all; on the cut line it shows the next one, as p does.
func TestClickFilter(t *testing.T) {
	lists := append(fixture(2), List{Boss: "boss-日本", Items: []todo.Shown{{ID: 5, Text: "Wide name", Status: sidebar.StatusYours}}})
	m, _ := board(t, lists, 120, 40)
	m = clickOn(t, m, "日本")
	if m.project != "日本" || strings.Contains(plain(m), "beta #1") {
		t.Errorf("❌ click on 日本: project %q", m.project)
	}
	m = clickOn(t, m, "beta")
	if m.project != "beta" {
		t.Errorf("❌ click on beta: project %q", m.project)
	}
	x, y := at(t, m, "project all")
	m = press(t, m, tea.MouseButtonLeft, x+len("project "), y)
	if m.project != "" {
		t.Errorf("❌ click on all: project %q", m.project)
	}
	m = press(t, m, tea.MouseButtonLeft, 2, y) // "project ", not a project
	if m.project != "" {
		t.Errorf("❌ click on the label: project %q", m.project)
	}

	var many []List
	for i := range 9 {
		many = append(many, List{Boss: fmt.Sprintf("boss-project-with-a-long-name-%d", i), Items: []todo.Shown{{ID: 1, Text: "x", Status: sidebar.StatusYours}}})
	}
	m, _ = board(t, many, 60, 30)
	if !strings.Contains(plain(m), "(1 of 10, p next)") {
		t.Fatalf("❌ not the cut filter line:\n%s", plain(m))
	}
	m = clickOn(t, m, "p next")
	if m.project != "project-with-a-long-name-0" {
		t.Errorf("❌ click on the cut line: project %q", m.project)
	}
}

// With the item view open a click on an option answers, one beside the view closes it as esc does; a second press
// right after the click that opened it does nothing.
func TestClickItemView(t *testing.T) {
	for _, h := range []int{40, 16} {
		m, f := board(t, fixture(2), 120, h)
		m = clickOn(t, m, "! alpha #1")
		if m.item == nil {
			t.Fatalf("❌ %d high: the ask not open:\n%s", h, plain(m))
		}
		x, y := at(t, m, "Wait   ")
		next, _ := m.mouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, clock.Add(100*time.Millisecond))
		if m := next.(Model); m.item == nil || len(f.answered) != 0 {
			t.Errorf("❌ %d high: a double click's second press answered %v", h, f.answered)
		}
		// A quick click beside it still closes it
		if next, _ := m.mouse(tea.MouseMsg{X: 1, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, clock.Add(100*time.Millisecond)); next.(Model).item != nil {
			t.Errorf("❌ %d high: a quick click beside the view left it open", h)
		}
		// A refusal's line makes the view taller: the click still hits
		m = keys(t, m, "3")
		if !strings.Contains(plain(m), "#1 has 2 options") {
			t.Fatalf("❌ %d high: no refusal:\n%s", h, plain(m))
		}
		m = clickOn(t, m, "Wait   ")
		if m.item != nil || len(f.answered) != 1 || f.answered[0] != "boss-alpha#1=Wait" {
			t.Errorf("❌ %d high: click on Wait answered %v, view open %v\n%s", h, f.answered, m.item != nil, plain(m))
		}
	}
	m, f := board(t, fixture(2), 120, 40)
	m = clickOn(t, m, "! alpha #1")
	m = press(t, m, tea.MouseButtonLeft, 1, 1)
	if m.item != nil || m.Done || len(f.answered) != 0 {
		t.Errorf("❌ click beside the view: open %v, board done %v", m.item != nil, m.Done)
	}
	// Beside it while the own-words field is open: the field closes first, as esc does
	m = keys(t, m, "enter", "m")
	m = press(t, m, tea.MouseButtonLeft, 1, 1)
	if m.item == nil {
		t.Error("❌ click beside the view closed it while typing")
	}
	m = press(t, m, tea.MouseButtonLeft, 1, 1)
	if m.item != nil {
		t.Error("❌ second click beside the view kept it open")
	}
}

// In a scrolled column a click opens the card it shows, under "↑ n more"; releases and drags do nothing; the wheel
// stops at the column's ends and does nothing over the item view.
func TestClickScrolledAndIgnored(t *testing.T) {
	m, _ := board(t, fixture(12), 90, 28)
	m = keys(t, m, "j", "j", "j", "j") // top 2: ↑ 2 more, then alpha #11
	m = clickOn(t, m, "alpha #11")
	if m.item == nil || m.card != 2 || !strings.Contains(plain(m), "alpha #11 · To do") {
		t.Fatalf("❌ click under ↑ 2 more: card %d\n%s", m.card, plain(m))
	}
	// A press beside the view over a card closes it; that click's release doesn't open the card
	x, y := at(t, m, "↑ 2 more")
	m = press(t, m, tea.MouseButtonLeft, x, y+1)
	if m.item != nil {
		t.Fatal("❌ press beside the view left it open")
	}
	for _, a := range []tea.MouseAction{tea.MouseActionRelease, tea.MouseActionMotion} {
		next, _ := m.mouse(tea.MouseMsg{X: x, Y: y + 1, Button: tea.MouseButtonLeft, Action: a}, clock.Add(time.Hour))
		if b := next.(Model); b.item != nil || b.card != m.card || b.col != m.col {
			t.Errorf("❌ left %v over a card acted: view open %v card %d", a, b.item != nil, b.card)
		}
	}

	m, _ = board(t, fixture(12), 90, 28)
	x, y = at(t, m, "To do")
	m = press(t, m, tea.MouseButtonWheelUp, x, y+3)
	if m.card != 0 || m.top != 0 {
		t.Errorf("❌ wheel up on the first card: card %d top %d", m.card, m.top)
	}
	for range 20 {
		m = press(t, m, tea.MouseButtonWheelDown, x, y+3)
	}
	if m.card != 12 {
		t.Errorf("❌ wheel down past the end: card %d, want 12", m.card)
	}
	m = keys(t, m, "enter")
	open := m.card
	m = press(t, m, tea.MouseButtonWheelUp, x, y+3)
	if m.item == nil || m.card != open {
		t.Errorf("❌ wheel over the item view: view open %v card %d", m.item != nil, m.card)
	}
}

// Keys pressed fast in the item view each run there, as on the board.
func TestItemViewFastKeys(t *testing.T) {
	m, _ := board(t, fixture(2), 120, 40)
	m = keys(t, m, "enter", "jj")
	if !strings.Contains(plain(m), "▸ m") {
		t.Errorf("❌ jj didn't move to own words:\n%s", plain(m))
	}
}

// Five columns need an inner width of 132: a popup of 136.
func TestFitColsEdge(t *testing.T) {
	for w, want := range map[int]int{131: 4, 132: 5, 105: 4, 104: 3, 78: 3, 77: 2, 51: 2, 50: 1} {
		if got := fitCols(w); got != want {
			t.Errorf("❌ inner %d: %d columns, want %d", w, got, want)
		}
	}
}

// A check's state: a boss closed's is the chip's, an unchanged one's only on a check item, failing ones red.
func TestCheckState(t *testing.T) {
	now := time.Unix(1700000000, 0)
	at := func(kind, status string) (string, string) {
		it := todo.Shown{Status: status, Check: &sidebar.Check{Kind: kind, PR: 4, Since: now.Unix()}}
		return checkState(card{it: it}, now)
	}
	if s, _ := at(sidebar.CheckBossClosed, sidebar.StatusCheck); s != "" {
		t.Errorf("❌ boss closed shows %q", s)
	}
	if s, _ := at(sidebar.CheckUnchanged, sidebar.StatusYours); s != "" {
		t.Errorf("❌ unchanged on a plain item shows %q", s)
	}
	if _, c := at(sidebar.CheckUnchanged, sidebar.StatusCheck); c != sidebar.Gold {
		t.Errorf("❌ unchanged check colour %q", c)
	}
	if _, c := at(sidebar.CheckFailing, sidebar.StatusCheck); c != sidebar.Red {
		t.Errorf("❌ failing colour %q", c)
	}
	if _, c := at(sidebar.CheckApproved, sidebar.StatusCheck); c != sidebar.Green {
		t.Errorf("❌ approved colour %q", c)
	}
}

// A test item that asks for the user doesn't turn its column's head red, as the top line doesn't count it.
func TestQuietHeadNotRed(t *testing.T) {
	ask := todo.Shown{ID: 1, Text: "T?", Quiet: true, Placement: todo.Placement{Column: sidebar.ColumnTodo, NeedsYou: true}}
	m, _ := board(t, []List{{Boss: "boss-x", Items: []todo.Shown{ask}}}, 200, 30)
	if strings.Contains(strings.Join(m.lines(time.Now()), "\n"), sidebar.Red+"▸ To do") {
		t.Error("❌ a quiet ask reddens its column")
	}
}

// A tick while a read is still under way starts no second one.
func TestTickDoesNotStack(t *testing.T) {
	m, _ := board(t, fixture(1), 200, 30)
	m.reads++ // one started, not landed
	if next, _ := m.Update(tickMsg{}); next.(Model).reads != m.reads {
		t.Error("❌ a tick started a read over one under way")
	}
	m.shown = m.reads
	if next, _ := m.Update(tickMsg{}); next.(Model).reads != m.reads+1 {
		t.Error("❌ a tick started no read")
	}
}
