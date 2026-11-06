package herd

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
)

// board4 is a worker in each section, the selected one working, at 70×40.
func board4() Model {
	now := time.Unix(1700000000, 0)
	return Model{width: 70, height: 40, selected: "docs-api", clk: control.Clock{Now: func() time.Time { return now }},
		snap: snapshot{tiles: []tile{
			{sess: sidebar.Session{Name: "fix-login", Agent: "Claude", State: agent.Waiting, Branch: "feature/fix-login-redis"},
				deciding: true, since: now.Add(-3 * time.Minute),
				question: agent.Question{Tool: "Bash", Command: "rm -rf build"}},
			{sess: sidebar.Session{Name: "docs-api", Agent: "Claude", State: agent.Working, Title: "Document the API",
				Branch: "feature/docs-api-examples", Cost: "$1.20", PR: sidebar.PR{Number: 95, State: "open"}},
				since: now.Add(-12 * time.Minute), context: "63",
				tail: desktop.Tail{Lines: []string{"Added **examples** for every `endpoint`.", "Tests next."}, Tool: "Bash", ToolDescription: "Run the desktop tests"},
				body: []string{"one", "two", "three", "four", "five", "six"}},
			{sess: sidebar.Session{Name: "ci-fix", Agent: "Claude", State: agent.Idle, Branch: "ci-fix"},
				since: now.Add(-8 * time.Minute), tail: desktop.Tail{Lines: []string{"Pushed.", "WAITING (CI: lint, test)"}}},
			{sess: sidebar.Session{Name: "old-spike", Agent: "Claude", State: agent.Idle, PR: sidebar.PR{Number: 90, State: "merged"}},
				since: now.Add(-2 * time.Hour), note: "finished: done"},
		}}}
}

// Each section under its header with its count; the working card selected and open with its title, what its
// call does, branch, cost and context, PR, last words and screen; an idle card folded to one row.
func TestBoardLayout(t *testing.T) {
	want := []string{
		" NEEDS YOU 1 ─────────────────────────────────────────────────────────",
		"┃╭───────────────────────────────────────────────────────────────────╮",
		"┃│ ▾ ● fix-login                                                  3m │",
		"┃│    allow Bash: rm -rf build                                       │",
		"┃│    feature/fix-login-redis                                        │",
		"┃│    Open  Message…                                               × │",
		"┃╰───────────────────────────────────────────────────────────────────╯",
		" WORKING 1 ───────────────────────────────────────────────────────────",
		" ╭───────────────────────────────────────────────────────────────────╮",
		" │ ▾ ● docs-api                                                  12m │",
		" │    Document the API                                               │",
		" │    Run the desktop tests                                          │",
		" │    feature/docs-api-examples                          $1.20 · 63% │",
		" │    ○ PR #95 open                                                  │",
		" │    Added examples for every endpoint.                             │",
		" │    Tests next.                                                    │",
		" │    ┄ screen ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ │",
		" │    two                                                            │",
		" │    three                                                          │",
		" │    four                                                           │",
		" │    five                                                           │",
		" │    six                                                            │",
		" │    Open  Message…  PR #95                                       × │",
		" ╰───────────────────────────────────────────────────────────────────╯",
		" WAITING 1 ───────────────────────────────────────────────────────────",
		"┃╭───────────────────────────────────────────────────────────────────╮",
		"┃│ ▾ ● ci-fix                                                     8m │",
		"┃│    CI: lint, test                                                 │",
		"┃│    ci-fix                                                         │",
		"┃│    Pushed.                                                        │",
		"┃│    Open  Message…                                               × │",
		"┃╰───────────────────────────────────────────────────────────────────╯",
		" IDLE 1 ──────────────────────────────────────────────────────────────",
		"  ▸ ● old-spike  finished  ◆ #90                                   2h ",
	}
	got := board4().board().lines
	for i := range max(len(got), len(want)) {
		g := "<none>"
		if i < len(got) {
			g = ansi.Strip(got[i])
		}
		if i >= len(want) || g != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, g, at(want, i))
		}
		if w := ansi.StringWidth(got[i]); w != 70 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

// Needs you and Waiting cards carry their section's bar, the selected card's box its state's colour, the
// others' faint; a needs you status is red.
func TestBoardColours(t *testing.T) {
	got := board4().board().lines
	if !strings.HasPrefix(got[1], sidebar.Red+"┃"+reset+rule+"╭") {
		t.Errorf("needs you: red bar, faint box: %q", got[1])
	}
	if !strings.Contains(got[3], sidebar.Red+bold+"allow Bash") {
		t.Errorf("needs you status not red: %q", got[3])
	}
	if !strings.HasPrefix(got[8], " "+sidebar.Gold+"╭") {
		t.Errorf("selected working card not in gold: %q", got[8])
	}
	if !strings.HasPrefix(got[25], sidebar.Green+"┃") {
		t.Errorf("waiting: green bar: %q", got[25])
	}
}

// A narrow pane cuts every row to its width, the branch at its start.
func TestBoardNarrow(t *testing.T) {
	m := board4()
	m.width = 30
	for i, l := range m.board().lines {
		if w := ansi.StringWidth(l); w != 30 {
			t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	if got := ansi.Strip(m.board().lines[12]); !strings.Contains(got, "│    …") || !strings.Contains(got, "-examples $1.20 · 63%") {
		t.Errorf("branch cut at its start: %q", got)
	}
}

// A card flags a health note, a transcript it couldn't read (the cause before the path) and rows that didn't
// parse; an idle card with one stays open, else it folds; a folded card with a warning ends in ⚠. A needs a
// decision note outliving its dialog says nothing.
func TestBoardWarnings(t *testing.T) {
	m := Model{width: 90, height: 20, folds: map[string]bool{"b": true}, snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "a", State: agent.Idle}, note: "stuck: no progress for 17m",
			tailErr: &fs.PathError{Op: "open", Path: "/t/a.jsonl", Err: fs.ErrPermission}},
		{sess: sidebar.Session{Name: "b", State: agent.Idle}, note: "high context: 85% used"},
		{sess: sidebar.Session{Name: "c", State: agent.Idle}, tail: desktop.Tail{Broken: 2}},
		{sess: sidebar.Session{Name: "d", State: agent.Idle}, note: "finished"},
		{sess: sidebar.Session{Name: "e", Agent: "Codex", State: agent.Idle}, note: "needs a decision: allow Bash: rm -rf build"},
	}}}
	got := ansi.Strip(strings.Join(m.board().lines, "\n"))
	if strings.Contains(got, "decision") || strings.Contains(got, "rm -rf") {
		t.Errorf("e's stale decision note shows:\n%s", got)
	}
	for _, w := range []string{"⚠ stuck: no progress for 17m", "⚠ transcript: open: permission denied (/t/a.jsonl)", "▾ ● c",
		"⚠ transcript: 2 rows did not parse", "▸ ● d  finished", "▸ ● e"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if !strings.Contains(got, "▸ ● b ⚠") {
		t.Errorf("folded b without its ⚠:\n%s", got)
	}
}

// f and a click on the arrow fold and unfold a card for the session; a Needs you card stays open, f on it does
// nothing, and a fold of a session no longer listed is forgotten.
func TestFold(t *testing.T) {
	m := board4()
	press := func(m Model, k string) Model {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(Model)
	}
	m = press(m, "f")
	if !m.snap.tiles[1].folded(m.folds) {
		t.Fatal("f leaves docs-api open")
	}
	// old-spike's row moved up by docs-api's folded body; its arrow at column 2
	y := -1
	for i, l := range m.board().lines {
		if strings.Contains(ansi.Strip(l), "old-spike") {
			y = i
		}
	}
	next, _ := m.Update(tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = next.(Model); m.snap.tiles[3].folded(m.folds) || m.box.session != "" {
		t.Errorf("click on old-spike's arrow: folded %v, box %q", m.snap.tiles[3].folded(m.folds), m.box.session)
	}
	m.folds["fix-login"] = true
	if m.snap.tiles[0].folded(m.folds) {
		t.Error("a deciding card folded")
	}
	delete(m.folds, "fix-login")
	for m.selected != "fix-login" {
		m = press(m, "k")
	}
	if m = press(m, "f"); m.folds["fix-login"] {
		t.Error("f on a Needs you card stored a fold")
	}
	snap := m.snap
	snap.tiles = snap.tiles[1:]
	next, _ = m.Update(refreshMsg{snap: snap, inputs: m.inputs})
	if m = next.(Model); len(m.folds) != 2 {
		t.Errorf("folds %v, want old-spike's and docs-api's kept", m.folds)
	}
	snap.tiles = snap.tiles[1:]
	next, _ = m.Update(refreshMsg{snap: snap, inputs: m.inputs})
	if m = next.(Model); m.folds["docs-api"] {
		t.Errorf("folds %v, want docs-api's forgotten", m.folds)
	}
}

// The board scrolls to keep the selected card whole, its top when it's taller than the pane; the bottom row
// says where the selection is.
func TestBoardScroll(t *testing.T) {
	m := board4()
	m.height = 12
	m.selected = "ci-fix"
	got := m.render()
	if s := ansi.Strip(got[10]); !strings.Contains(s, "╰") {
		t.Errorf("ci-fix's card ends the board: %q", s)
	}
	if s := ansi.Strip(got[11]); !strings.HasPrefix(s, " 3/4  ") {
		t.Errorf("bottom row %q", s)
	}
	m.selected = "docs-api"
	if s := ansi.Strip(m.render()[0]); !strings.Contains(s, "WORKING") {
		t.Errorf("a card taller than the pane shows from its header: %q", s)
	}
	m.snap.tiles = nil
	if s := ansi.Strip(m.render()[0]); !strings.Contains(s, "No worker sessions") {
		t.Errorf("empty board: %q", s)
	}
}

// v shows half the pane of the selected card's screen.
func TestExpand(t *testing.T) {
	m := board4()
	m.height = 10
	if got := m.screenRows(m.snap.tiles[1], nil); len(got) != screenRows {
		t.Errorf("screen rows %d, want %d", len(got), screenRows)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = next.(Model)
	m.snap.tiles[1].body = append(m.snap.tiles[1].body, "7", "8")
	if got := m.screenRows(m.snap.tiles[1], nil); len(got) != screenRows {
		t.Errorf("expanded in a 10-row pane: %d rows, want %d", len(got), screenRows)
	}
	m.height = 40
	if got := m.screenRows(m.snap.tiles[1], nil); len(got) != 8 {
		t.Errorf("expanded: %d rows, want all 8 (up to 20)", len(got))
	}
	if got := m.screenRows(m.snap.tiles[2], nil); len(got) != 0 {
		t.Errorf("an uncaptured card shows %q", got)
	}
}

// A refresh captures the selected card first, then the deciding ones, then the other open ones; folded
// cards' panes aren't captured.
func TestCaptures(t *testing.T) {
	tiles := board4().snap.tiles
	got := captures(tiles, "ci-fix", nil)
	if want := []int{2, 0, 1}; !slices.Equal(got, want) {
		t.Errorf("captures = %v, want %v", got, want)
	}
	if got := captures(tiles, "ci-fix", map[string]bool{"docs-api": true, "old-spike": false}); !slices.Equal(got, []int{2, 0, 3}) {
		t.Errorf("captures with folds = %v", got)
	}
}

// A click lands on the card drawn there on a scrolled board; one on an open card's ▾ folds it, one elsewhere
// on it opens its reply box.
func TestBoardClicks(t *testing.T) {
	m := board4()
	m.height = 12
	m.selected = "ci-fix"
	row := func(m Model, name string) int {
		for y, l := range m.render() {
			if strings.Contains(ansi.Strip(l), "● "+name) {
				return y
			}
		}
		t.Fatalf("%s not on screen", name)
		return -1
	}
	click := func(m Model, x, y int) Model {
		next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return next.(Model)
	}
	y := row(m, "ci-fix")
	if y == 2 {
		t.Fatalf("board not scrolled: ci-fix at row %d", y)
	}
	if got := click(m, 3, y); !got.snap.tiles[2].folded(got.folds) || got.box.session != "" {
		t.Errorf("click on ci-fix's ▾: folded %v, box %q", got.snap.tiles[2].folded(got.folds), got.box.session)
	}
	if got := click(m, 20, y); got.box.session != "ci-fix" {
		t.Errorf("click on ci-fix's name row opened %q", got.box.session)
	}
	if got := click(m, 20, y+1); got.box.session != "ci-fix" {
		t.Errorf("click in ci-fix's body opened %q", got.box.session)
	}
}

// Tiles come in section order, in their order within one.
func TestSortBySection(t *testing.T) {
	tiles := []tile{
		{sess: sidebar.Session{Name: "idle", State: agent.Idle}},
		{sess: sidebar.Session{Name: "work", State: agent.Working}},
		{sess: sidebar.Session{Name: "asks", State: agent.Idle}, note: "asks: Merge?"},
		{sess: sidebar.Session{Name: "work2", State: agent.Working}},
		{sess: sidebar.Session{Name: "dialog", State: agent.Waiting}, deciding: true},
	}
	sortBySection(tiles)
	var got []string
	for _, t := range tiles {
		got = append(got, t.sess.Name)
	}
	if want := []string{"asks", "dialog", "work", "work2", "idle"}; !slices.Equal(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
}

// A turn that ended on a question shows it, red, under Needs you.
func TestBoardAsks(t *testing.T) {
	m := Model{width: 60, height: 20, snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "a", State: agent.Idle}, note: "asks: Merge #68 now?"},
	}}}
	got := strings.Join(m.board().lines, "\n")
	if !strings.Contains(got, "NEEDS YOU 1") || !strings.Contains(got, sidebar.Red+bold+"Merge #68 now?") || strings.Count(got, "Merge #68 now?") != 1 || !strings.Contains(ansi.Strip(got), "▾ ● a") {
		t.Errorf("asks card:\n%s", ansi.Strip(got))
	}
}

// An asking tile is captured with the deciding ones, even folded.
func TestCapturesAsking(t *testing.T) {
	tiles := board4().snap.tiles
	tiles[3].sess.State, tiles[3].question = agent.Working, agent.Question{Tool: agent.AskUserQuestion}
	if got := captures(tiles, "ci-fix", map[string]bool{"old-spike": true}); !slices.Equal(got, []int{2, 0, 3, 1}) {
		t.Errorf("captures = %v", got)
	}
}

// A Needs you card showing its question as chips says it once: not again as its note, nor its screen above
// the dialog; a permission prompt or the answers' review keeps its screen, which shows what it asks about.
func TestQuestionCard(t *testing.T) {
	ask := agent.Question{Tool: agent.AskUserQuestion, Questions: []agent.Ask{{Question: "Which color should the theme use?", Header: "Color",
		Options: []agent.Choice{{Label: "Red"}, {Label: "Green"}, {Label: "Blue"}}}}}
	p := goldenPick(t, "claude-ask-q1", colors)
	m := Model{width: 70, height: 40, snap: snapshot{tiles: []tile{
		{sess: sidebar.Session{Name: "a", Agent: "Claude", State: agent.Waiting}, deciding: true, question: ask, pick: &p,
			note: "needs a decision: " + ask.Ask(), body: []string{"❯ pick a theme colour"}},
	}}}
	got := ansi.Strip(strings.Join(m.board().lines, "\n"))
	if n := strings.Count(got, "Which color should the theme use?"); n != 1 || strings.Contains(got, "screen") || strings.Contains(got, "pick a theme colour") {
		t.Errorf("question shown %d times, or its screen:\n%s", n, got)
	}
	// A dialog nothing told: its note is all it says
	m.snap.tiles[0].pick, m.snap.tiles[0].question = nil, agent.Question{}
	if got := ansi.Strip(strings.Join(m.board().lines, "\n")); !strings.Contains(got, "waiting for you") || !strings.Contains(got, "Which color") {
		t.Errorf("unknown dialog's note dropped:\n%s", got)
	}
	m.snap.tiles[0].question = ask
	for _, f := range []string{"claude-permission-bash", "claude-ask-review"} {
		p := goldenPick(t, f, colors)
		m.snap.tiles[0].pick = &p
		if got := ansi.Strip(strings.Join(m.board().lines, "\n")); !strings.Contains(got, "screen") {
			t.Errorf("%s card without its screen:\n%s", f, got)
		}
	}
}

// A card's screen starts at its first line with text that isn't what the card shows as said.
func TestScreenTrim(t *testing.T) {
	m := board4()
	tl := m.snap.tiles[1]
	tl.body = []string{"", "  ", "\x1b[0m", "", "✻ Working… (3s)"}
	if got := m.screenRows(tl, nil); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("blank lines kept: %q", got)
	}
	tl.body = []string{"", "\x1b[1m⏺\x1b[0m Tests  next.", "", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl)); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("said line kept: %q", got)
	}
	long := strings.Repeat("word ", 28) + "end."
	tl.tail.Lines = []string{long}
	tl.body = []string{"⏺ " + long[:60], "  " + long[60:], "", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl)); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("wrapped said line kept: %q", got)
	}
	tl.body = []string{"⏺ " + long[:60], "  something else", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl)); len(got) != 3 {
		t.Errorf("a line starting like a said one dropped: %q", got)
	}
	tl.tail.Lines = board4().snap.tiles[1].tail.Lines
	tl.body = []string{"Tests next. Then docs.", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl)); len(got) != 2 {
		t.Errorf("a different line dropped: %q", got)
	}
}
