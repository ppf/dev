package itemview

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

var now = time.Unix(1_800_000_000, 0)

// recorder is Actions that note each call.
type recorder struct{ calls []string }

func (r *recorder) actions() Actions {
	note := func(s string) (string, error) { r.calls = append(r.calls, s); return "ok " + s, nil }
	return Actions{
		Answer: func(boss string, id int, q, a string) (string, error) {
			return note("answer " + boss + " " + q + " " + a)
		},
		Tick:   func(boss string, id int, text string) (string, error) { return note("tick " + boss + " " + text) },
		Start:  func(boss string, id int, text string) (string, error) { return note("start " + boss + " " + text) },
		Open:   func(url string) error { _, err := note("open " + url); return err },
		Switch: func(s string) error { _, err := note("switch " + s); return err },
	}
}

// press runs keys on m, each action's command too.
func press(m Model, ks ...string) Model {
	for _, k := range ks {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
	}
	return m
}

var ask = Item{Boss: "boss-dev", Shown: todo.Shown{ID: 4, Text: "Merge #221 now?", Status: sidebar.StatusDecide, Added: now.Add(-2 * time.Hour).Unix(),
	Link: "https://x/pull/221", Context: []string{"CI green"}, Options: []string{"Merge now | ships today (Recommended)", "Wait | after the test"}}}

// An ask lists its options with ★ and detail, then own words; 1-9 answer at once, j/k ⏎ the picked one, m in own words.
func TestAsk(t *testing.T) {
	var r recorder
	m := New(ask, r.actions(), false)
	v := ansi.Strip(m.View(Width, 40, now))
	for _, want := range []string{"╭─ dev #4 · To decide ", " 2h ─╮", "Merge #221 now?", "CI green", "▸ 1  ★ Merge now   ships today", "  2    Wait        after the test",
		"m    In your own words…", "status  decide", "link    https://x/pull/221", "list    boss-dev · added 2h ago", "1-2 answer · j/k ⏎ pick · m own words · o open link · g go to boss · esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if ansi.StringWidth(l) != Width {
			t.Errorf("line %q is %d wide, want %d", l, ansi.StringWidth(l), Width)
		}
	}
	if m := press(m, "2"); !m.Done || m.Result != "ok answer boss-dev Merge #221 now? Wait | after the test" {
		t.Errorf("2: done %v result %q", m.Done, m.Result)
	}
	if m := press(New(ask, r.actions(), false), "j", "enter"); !m.Done || !strings.HasSuffix(m.Result, "Wait | after the test") {
		t.Errorf("j ⏎: %q", m.Result)
	}
	typed := press(New(ask, r.actions(), false), "m", "s", "o", "o", "n", "backspace", " ", "1")
	if typed.Done || !strings.Contains(ansi.Strip(typed.View(Width, 40, now)), "m    soo 1") {
		t.Fatalf("own words typed over keys:\n%s", ansi.Strip(typed.View(Width, 40, now)))
	}
	if m := press(typed, "enter"); !m.Done || m.Result != "ok answer boss-dev Merge #221 now? soo 1" {
		t.Errorf("own words: %q", m.Result)
	}
	if m := press(typed, "esc"); m.Done || m.typing {
		t.Error("esc left more than the field")
	}
	if m := press(New(ask, r.actions(), false), "3"); m.Done || !strings.Contains(m.msg, "2 options") {
		t.Errorf("3: %q", m.msg)
	}
}

// The user's own item lists Start, Tick, Open link, Go to boss; one on a worker Go to worker, Open link, Go to boss;
// one on a person only the link and its boss.
func TestActions(t *testing.T) {
	plain := Item{Boss: "boss-shop", Shown: todo.Shown{ID: 3, Text: "Reply to the review", Status: sidebar.StatusYours, Link: "https://x/pull/88"}}
	worker := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 9, Text: "Fix the test", On: "dev-flaky", Session: &todo.Session{ID: "$4", Name: "dev-flaky"},
		Status: sidebar.StatusBlocked, StatusOn: "dev-flaky"}}
	person := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 7, Text: "Review", On: "Carl", Status: sidebar.StatusWaiting, StatusOn: "Carl"}}
	check := plain
	check.Status = sidebar.StatusCheck
	closed := plain
	closed.Closed = true
	for _, c := range []struct {
		name string
		it   Item
		keys []string
	}{
		{"plain", plain, []string{"s", "d", "o", "g"}},
		{"worker", worker, []string{"w", "g"}},
		{"person", person, []string{"g"}},
		{"check", check, []string{"d", "o", "g"}},
		{"closed", closed, []string{"d", "o"}},
	} {
		var keys []string
		for _, e := range New(c.it, Actions{}, false).entries {
			keys = append(keys, e.key)
		}
		if !slices.Equal(keys, c.keys) {
			t.Errorf("%s: keys %v, want %v", c.name, keys, c.keys)
		}
	}
	if keys := New(closed, Actions{}, true).entries; keys[0].key != "s" || !strings.Contains(keys[0].detail, "manager") {
		t.Errorf("closed with the manager running: %+v", keys)
	}
	var r recorder
	for _, c := range []struct {
		it   Item
		keys []string
		want string
	}{
		{plain, []string{"enter"}, "start boss-shop Reply to the review"},
		{plain, []string{"d"}, "tick boss-shop Reply to the review"},
		{plain, []string{"k", "enter"}, "switch boss-shop"},
		{plain, []string{"o"}, "open https://x/pull/88"},
		{worker, []string{"w"}, "switch dev-flaky"},
		{worker, []string{"g"}, "switch boss-dev"},
	} {
		r.calls = nil
		if m := press(New(c.it, r.actions(), false), c.keys...); !m.Done || !slices.Equal(r.calls, []string{c.want}) {
			t.Errorf("%v on #%d: done %v calls %v, want %q", c.keys, c.it.ID, m.Done, r.calls, c.want)
		}
	}
	r.calls = nil
	for _, k := range []string{"s", "d", "o"} {
		if m := press(New(worker, r.actions(), false), k); m.Done || m.msg == "" {
			t.Errorf("%s on a worker's item: done %v msg %q", k, m.Done, m.msg)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("worker's item ran %v", r.calls)
	}
	v := ansi.Strip(New(worker, r.actions(), false).View(Width, 40, now))
	if !strings.Contains(v, "status  blocked · dev-flaky") || !strings.Contains(v, "j/k ⏎ run · w go to worker · g go to boss · esc close") {
		t.Errorf("worker's view:\n%s", v)
	}
}

// A failed action keeps the view open with why; keys wait while one is under way.
func TestFailure(t *testing.T) {
	acts := Actions{Tick: func(string, int, string) (string, error) { return "", errors.New("item #3 reads otherwise") }}
	m := New(Item{Boss: "boss-shop", Shown: todo.Shown{ID: 3, Text: "x", Status: sidebar.StatusYours}}, acts, false)
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if again, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}); cmd2 != nil || again.busy == "" {
		t.Error("a second d ran while the first was under way")
	}
	m, _ = m.Update(cmd())
	if m.Done || !strings.Contains(ansi.Strip(m.View(Width, 40, now)), "❌ item #3 reads otherwise") {
		t.Errorf("failure: done %v\n%s", m.Done, ansi.Strip(m.View(Width, 40, now)))
	}
}

// On a short screen the text's lines go first; the border, title and keys stay. A narrow one gets a narrower box.
func TestCut(t *testing.T) {
	it := ask
	it.Context = []string{"one", "two", "three", "four"}
	m := New(it, Actions{}, false)
	full := m.Height(Width, now)
	v := strings.Split(ansi.Strip(m.View(Width, full-3, now)), "\n")
	if len(v) != full-3 || !strings.Contains(v[0], "dev #4") || !strings.Contains(v[len(v)-2], "esc close") || !strings.HasPrefix(v[len(v)-1], "╰") {
		t.Errorf("cut to %d:\n%s", full-3, strings.Join(v, "\n"))
	}
	if strings.Contains(strings.Join(v, "\n"), "four") || !strings.Contains(strings.Join(v, "\n"), "1  ★ Merge now") {
		t.Errorf("cut the wrong lines:\n%s", strings.Join(v, "\n"))
	}
	for _, l := range strings.Split(m.View(60, 40, now), "\n") {
		if ansi.StringWidth(l) != 60 {
			t.Errorf("at 60: %q", ansi.Strip(l))
		}
	}
}

// A closed boss's ask shows its options but takes no answer: 1-9 and ⏎ say how to reopen it.
func TestClosedAsk(t *testing.T) {
	var r recorder
	it := ask
	it.Closed = true
	for _, k := range []string{"1", "enter"} {
		m := press(New(it, r.actions(), false), k)
		if m.Done || len(r.calls) != 0 || !strings.Contains(m.msg, "dev boss dev reopens it") {
			t.Errorf("%s: done %v calls %v msg %q", k, m.Done, r.calls, m.msg)
		}
	}
	if keys := ansi.Strip(New(it, r.actions(), false).keys()); strings.Contains(keys, "answer") {
		t.Errorf("keys %q offer an answer", keys)
	}
}

// An ask handed to a worker is the worker's: no answer, Go to worker; one on the boss's own name is the user's.
func TestOwn(t *testing.T) {
	handed := ask
	handed.On, handed.Session = "dev-merge", &todo.Session{ID: "dev-merge", Name: "dev-merge"}
	if m := New(handed, Actions{}, false); m.ask() || m.entries[0].key != "w" {
		t.Errorf("handed-over ask: entries %+v", m.entries)
	}
	mine := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 2, Text: "x", On: "boss-dev"}}
	if m := New(mine, Actions{}, false); !m.plain() || m.entries[0].key != "s" {
		t.Errorf("item on its own boss: entries %+v", m.entries)
	}
}

// While an action is under way esc waits for its outcome; ctrl+c still closes. A failure's lines show as one.
func TestBusy(t *testing.T) {
	acts := Actions{Tick: func(string, int, string) (string, error) { return "", errors.New("refused\nhint: check it") }}
	m := New(Item{Boss: "boss-shop", Shown: todo.Shown{ID: 3, Text: "x"}}, acts, false)
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if esc, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); esc.Done {
		t.Error("esc closed while ticking")
	}
	if quit, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); !quit.Done {
		t.Error("ctrl+c didn't close while ticking")
	}
	m, _ = m.Update(cmd())
	if m.msg != "❌ refused hint: check it" {
		t.Errorf("msg %q", m.msg)
	}
}

// A paste's line breaks and tabs go into the own-words field as spaces.
func TestPaste(t *testing.T) {
	m := press(New(ask, Actions{}, false), "m")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\tc"), Paste: true})
	if string(m.field) != "a b c" {
		t.Errorf("field %q", string(m.field))
	}
}

// Cut to a few rows, the list keeps the selected line; too narrow, nothing is drawn.
func TestCutSelection(t *testing.T) {
	it := ask
	it.Options = []string{"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8", "o9"}
	m := press(New(it, Actions{}, false), "k", "k")
	v := ansi.Strip(m.View(Width, 9, now))
	if !strings.Contains(v, "▸ 9") || !strings.Contains(v, "esc close") || strings.Count(v, "\n") != 8 {
		t.Errorf("cut to 9 rows:\n%s", v)
	}
	if v := m.View(minWidth-1, 40, now); v != "" {
		t.Errorf("at %d columns: %q", minWidth-1, v)
	}
}

// row is the box's first row showing s.
func row(t *testing.T, m Model, w, h int, s string) int {
	t.Helper()
	for i, l := range strings.Split(ansi.Strip(m.View(w, h, now)), "\n") {
		if strings.Contains(l, s) {
			return i
		}
	}
	t.Fatalf("%q not in the view:\n%s", s, ansi.Strip(m.View(w, h, now)))
	return -1
}

// click clicks row y of the box w×h, running the action's command too.
func click(m Model, y, w, h int) Model {
	m, cmd := m.Click(y, w, h, now)
	if cmd != nil {
		m, _ = m.Update(cmd())
	}
	return m
}

// A click on an option or action does what its key does; elsewhere nothing. Own words: a click on its line opens the
// field, one on another line leaves it (text kept) and runs that line.
func TestClick(t *testing.T) {
	var r recorder
	m := New(ask, r.actions(), false)
	if m := click(m, row(t, m, Width, 40, "Wait"), Width, 40); !m.Done || !strings.HasSuffix(m.Result, "Wait | after the test") {
		t.Errorf("click on Wait: done %v result %q", m.Done, m.Result)
	}
	for _, s := range []string{"Merge #221 now?", "status", "esc close", "╭─", "╰"} {
		if m := click(m, row(t, m, Width, 40, s), Width, 40); m.Done || m.sel != 0 || len(r.calls) != 1 {
			t.Errorf("click on %q: done %v sel %d calls %v", s, m.Done, m.sel, r.calls)
		}
	}
	typed := press(click(m, row(t, m, Width, 40, "In your own words"), Width, 40), "h", "i")
	if !typed.typing || string(typed.field) != "hi" {
		t.Fatalf("click on own words: typing %v field %q", typed.typing, string(typed.field))
	}
	if again := click(typed, row(t, typed, Width, 40, "m    hi"), Width, 40); !again.typing || again.Done {
		t.Error("click on the field's line left it")
	}
	r.calls = nil
	if m := click(typed, row(t, typed, Width, 40, "Merge now"), Width, 40); !m.Done || !slices.Equal(r.calls, []string{"answer boss-dev Merge #221 now? Merge now | ships today (Recommended)"}) {
		t.Errorf("click on Merge now while typing: done %v calls %v", m.Done, r.calls)
	}

	// A closed boss's ask refuses as 1 does
	closed := ask
	closed.Closed = true
	r.calls = nil
	if m := click(New(closed, r.actions(), false), row(t, New(closed, r.actions(), false), Width, 40, "Wait"), Width, 40); m.Done || len(r.calls) != 0 || !strings.Contains(m.msg, "reopens it") {
		t.Errorf("click on a closed boss's option: done %v calls %v msg %q", m.Done, r.calls, m.msg)
	}

	// Actions: Tick, then a click while it's under way does nothing
	plain := Item{Boss: "boss-shop", Shown: todo.Shown{ID: 3, Text: "Reply", Status: sidebar.StatusYours}}
	p := New(plain, r.actions(), false)
	busy, cmd := p.Click(row(t, p, 60, 40, "Tick"), 60, 40, now)
	if cmd == nil || busy.busy != "ticking" {
		t.Fatalf("click on Tick: busy %q", busy.busy)
	}
	if again, cmd2 := busy.Click(row(t, busy, 60, 40, "Start"), 60, 40, now); cmd2 != nil || again.sel != busy.sel {
		t.Error("a click ran while an action was under way")
	}
}

// Cut short, a click still hits the line it shows: the list's front cut, the text gone.
func TestClickCut(t *testing.T) {
	var r recorder
	it := ask
	it.Options = []string{"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8", "o9"}
	m := press(New(it, r.actions(), false), "k", "k") // 9 selected: the list's front is cut
	if v := ansi.Strip(m.View(Width, 9, now)); strings.Contains(v, "o1") {
		t.Fatalf("not cut:\n%s", v)
	}
	if m := click(m, row(t, m, Width, 9, "o8"), Width, 9); !m.Done || !slices.Equal(r.calls, []string{"answer boss-dev Merge #221 now? o8"}) {
		t.Errorf("click on o8 cut: calls %v", r.calls)
	}
}
