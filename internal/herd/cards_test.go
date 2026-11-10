package herd

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
)

// board4 is a worker in each section, the selected one working, at 70×40.
func board4() Model {
	now := time.Unix(1700000000, 0)
	return Model{width: 70, height: 40, selected: "docs-api", size: shared, clk: control.Clock{Now: func() time.Time { return now }},
		snap: snapshot{tiles: []tile{
			{sess: sidebar.Session{Name: "fix-login", Agent: "Claude", State: agent.Waiting, Branch: "feature/fix-login-redis"},
				deciding: true, since: now.Add(-3 * time.Minute),
				question: agent.Question{Tool: "Bash", Command: "rm -rf build"}},
			{sess: sidebar.Session{Name: "docs-api", Agent: "Claude", State: agent.Working, Title: "Document the API",
				Branch: "feature/docs-api-examples", Cost: "$1.20", PR: sidebar.PR{Number: 95, State: "open"}},
				since: now.Add(-12 * time.Minute), context: "63", model: "opus", effort: "medium",
				tail: desktop.Tail{Lines: []string{"Added **examples** for every `endpoint`.", "Tests next."}, Tool: "Bash", ToolDescription: "Run the desktop tests"},
				body: []string{"one", "two", "three", "four", "five", "six"}},
			{sess: sidebar.Session{Name: "ci-fix", Agent: "Claude", State: agent.Idle, Branch: "ci-fix"},
				since: now.Add(-8 * time.Minute), model: "sonnet", tail: desktop.Tail{Lines: []string{"Pushed.", "WAITING (CI: lint, test)"}}},
			{sess: sidebar.Session{Name: "old-spike", Agent: "Claude", State: agent.Idle, PR: sidebar.PR{Number: 90, State: "merged"}},
				since: now.Add(-2 * time.Hour), model: "opus", effort: "low", note: "finished: done"},
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
		"┃│    feature/fix-login-redis                          default model │",
		"┃│    Open  Message…                                               × │",
		"┃╰───────────────────────────────────────────────────────────────────╯",
		" WORKING 1 ───────────────────────────────────────────────────────────",
		" ╭───────────────────────────────────────────────────────────────────╮",
		" │ ▾ ● docs-api                                                  12m │",
		" │    Document the API                                               │",
		" │    Run the desktop tests                                          │",
		" │    feature/docs-api-examples             opus·medium  $1.20 · 63% │",
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
		"┃│    ci-fix                                                  sonnet │",
		"┃│    Pushed.                                                        │",
		"┃│    Open  Message…                                               × │",
		"┃╰───────────────────────────────────────────────────────────────────╯",
		" IDLE 1 ──────────────────────────────────────────────────────────────",
		"  ▸ ● old-spike  finished  ◆ #90                         opus·low  2h ",
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

// v shows half the pane of the selected card's screen, for that card only, until v on it again; on a folded
// card it unfolds it.
func TestExpand(t *testing.T) {
	m := board4()
	m.height = 10
	if got := m.screenRows(m.snap.tiles[1], nil, 80); len(got) != screenRows {
		t.Errorf("screen rows %d, want %d", len(got), screenRows)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = next.(Model)
	m.snap.tiles[1].body = append(m.snap.tiles[1].body, "7", "8")
	if got := m.screenRows(m.snap.tiles[1], nil, 80); len(got) != screenRows {
		t.Errorf("expanded in a 10-row pane: %d rows, want %d", len(got), screenRows)
	}
	m.height = 40
	if got := m.screenRows(m.snap.tiles[1], nil, 80); len(got) != 8 {
		t.Errorf("expanded: %d rows, want all 8 (up to 20)", len(got))
	}
	if got := m.screenRows(m.snap.tiles[2], nil, 80); len(got) != 0 {
		t.Errorf("an uncaptured card shows %q", got)
	}
	m.snap.tiles[0].body = slices.Repeat([]string{"x"}, 8)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m = next.(Model); m.selected != "fix-login" || len(m.screenRows(m.snap.tiles[0], nil, 80)) != screenRows || len(m.screenRows(m.snap.tiles[1], nil, 80)) != 8 {
		t.Errorf("after k: fix-login %d rows, docs-api %d", len(m.screenRows(m.snap.tiles[0], nil, 80)), len(m.screenRows(m.snap.tiles[1], nil, 80)))
	}
	m.selected = "docs-api"
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m = next.(Model); len(m.screenRows(m.snap.tiles[1], nil, 80)) != screenRows {
		t.Errorf("v again: %d rows, want %d", len(m.screenRows(m.snap.tiles[1], nil, 80)), screenRows)
	}
	m.selected = "old-spike"
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m = next.(Model); m.snap.tiles[3].folded(m.folds) || !m.expands["old-spike"] {
		t.Errorf("v on a folded card: folded %v, expanded %v", m.snap.tiles[3].folded(m.folds), m.expands["old-spike"])
	}
	// Expanded, then folded: v unfolds it expanded
	m.fold(m.snap.tiles[3])
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m = next.(Model); m.snap.tiles[3].folded(m.folds) || !m.expands["old-spike"] {
		t.Errorf("v on an expanded folded card: folded %v, expanded %v", m.snap.tiles[3].folded(m.folds), m.expands["old-spike"])
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
// on it selects it, one on its Message… opens its reply box.
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
	for _, dy := range []int{0, 1} {
		if got := click(m, 20, y+dy); got.selected != "ci-fix" || got.box.session != "" {
			t.Errorf("click on ci-fix's row %d: selected %q, box %q", dy, got.selected, got.box.session)
		}
	}
	m.height = 40
	if got := click(m, 20, row(m, "old-spike")); got.selected != "old-spike" || got.box.session != "" {
		t.Errorf("click on old-spike: selected %q, box %q", got.selected, got.box.session)
	}
	for y, l := range m.render() {
		if x := strings.Index(ansi.Strip(l), "Message…"); x >= 0 && y > row(m, "ci-fix") {
			if got := click(m, ansi.StringWidth(ansi.Strip(l)[:x])+1, y); got.box.session != "ci-fix" {
				t.Errorf("click on ci-fix's Message… opened %q", got.box.session)
			}
			return
		}
	}
	t.Error("no Message… under ci-fix")
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
	// A menu without a title: its note says what it asks
	menu := pick{d: dialog{opts: []option{{num: 1, label: "Postgres"}, {num: 2, label: "SQLite"}}, cursor: -1}, cur: -1, other: -1}
	m.snap.tiles[0].pick = &menu
	if got := ansi.Strip(strings.Join(m.board().lines, "\n")); !strings.Contains(got, "Which color") {
		t.Errorf("untitled menu's note dropped:\n%s", got)
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
	if got := m.screenRows(tl, nil, 80); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("blank lines kept: %q", got)
	}
	tl.body = []string{"", "\x1b[1m⏺\x1b[0m Tests  next.", "", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl), 80); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("said line kept: %q", got)
	}
	long := strings.Repeat("word ", 28) + "end."
	tl.tail.Lines = []string{long}
	tl.body = []string{"⏺ " + long[:60], "  " + long[60:], "", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl), 80); !slices.Equal(got, []string{"✻ Working… (3s)"}) {
		t.Errorf("wrapped said line kept: %q", got)
	}
	tl.body = []string{"⏺ " + long[:60], "  something else", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl), 80); len(got) != 3 {
		t.Errorf("a line starting like a said one dropped: %q", got)
	}
	tl.tail.Lines = board4().snap.tiles[1].tail.Lines
	tl.body = []string{"Tests next. Then docs.", "✻ Working… (3s)"}
	if got := m.screenRows(tl, said(tl), 80); len(got) != 2 {
		t.Errorf("a different line dropped: %q", got)
	}
}

// A reply line cut short on its card keeps its rows on screen, which show the rest.
func TestScreenKeepsCutReply(t *testing.T) {
	m := board4()
	tl := &m.snap.tiles[1]
	long := strings.Repeat("word ", 20) + "end."
	tl.tail.Lines = []string{long}
	tl.body = []string{"⏺ " + long[:60], "  " + long[60:], "✻ Working… (3s)"}
	if got := ansi.Strip(strings.Join(m.board().lines, "\n")); !strings.Contains(got, "word end.") {
		t.Errorf("the rest of a cut reply line dropped:\n%s", got)
	}
	tl.tail.Lines = []string{"Short reply."}
	tl.body = []string{"⏺ Short reply.", "✻ Working… (3s)"}
	if got := ansi.Strip(strings.Join(m.board().lines, "\n")); strings.Count(got, "Short reply.") != 1 {
		t.Errorf("a whole reply line repeated on screen:\n%s", got)
	}
}

// asksTile is dev-demo-count as captured: its turn ended on a question in its reply, a table above it.
func asksTile(t *testing.T) tile {
	tl := tile{sess: sidebar.Session{Name: "count", Agent: "Claude", State: agent.Idle, Branch: "demo-count"},
		note: "asks: Want the same breakdown for `sidebar`?",
		tail: desktop.Tail{Lines: []string{"- The files add up to the package totals from before (2,500 code, 3,065 raw).",
			"- `cards.go`, `input.go` and `model.go` together hold 1,323 code lines, over half the package.",
			"Want the same breakdown for `sidebar`?"}}}
	tl.body = previewLines(screenGolden(t, "claude-asks"), tl)
	return tl
}

// An asks card says its question once, in its status: its last lines and its screen stop above it, the
// screen showing the rows above the lines said.
func TestAsksCard(t *testing.T) {
	// At 70 columns a last line is cut short, so its rows stay on screen
	for _, w := range []int{70, 130} {
		m := Model{width: w, height: 64, snap: snapshot{tiles: []tile{asksTile(t)}}}
		got := ansi.Strip(strings.Join(m.board().lines, "\n"))
		if n := strings.Count(got, "Want the same breakdown"); n != 1 {
			t.Errorf("%d columns: question shown %d times:\n%s", w, n, got)
		}
		screen := got[strings.Index(got, "┄ screen"):]
		if !strings.Contains(screen, "│ card.go   │ 73") || !strings.Contains(screen, "└───────────┴") || strings.Contains(screen, "Worked for") {
			t.Errorf("%d columns: screen not the rows above the question:\n%s", w, got)
		}
	}
}

// An asks card says a question longer than a board line once, in its status from its start: its last lines and
// its screen stop above the question's first wrapped row.
func TestAsksCardLongQuestion(t *testing.T) {
	ask := "Should I also move the CI cache settings into the shared workflow file, so `herd`, `sidebar` and `inbox` " +
		"all reuse one Go module cache keyed on go.sum, or keep each job's own cache as it is now, given the shared " +
		"one would make a cold run slower for the first job but save about a minute on every later job in the same run?"
	reply := "Pushed the cache change to the branch.\n\n" + ask
	row := func(r any) string {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	jsonl := row(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": reply}}}})
	if err := os.WriteFile(path, []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := desktop.ReadTail(path)
	if err != nil {
		t.Fatal(err)
	}
	note, ok := inbox.FromHook("cache", []byte(row(map[string]any{"hook_event_name": "Stop", "last_assistant_message": reply})))
	if !ok || note.Kind != inbox.Asks {
		t.Fatalf("note = %+v, want an asks note", note)
	}
	tl := tile{sess: sidebar.Session{Name: "cache", Agent: "Claude", State: agent.Idle, Branch: "ci-cache"},
		note: note.Summary(), tail: read}
	// The screen wraps the reply at 120 columns
	tl.body = []string{"⏺ Bash(go test ./...)", "  ⎿  ok  github.com/ppf/dev/internal/herd", "", "⏺ Pushed the cache change to the branch.", ""}
	var asked []string
	prefix, rowText := "⏺ ", ""
	for _, w := range strings.Fields(strings.ReplaceAll(ask, "`", "")) {
		if len(rowText)+1+len(w) > 118 {
			asked = append(asked, prefix+rowText)
			prefix, rowText = "  ", ""
		}
		rowText = strings.TrimPrefix(rowText+" "+w, " ")
	}
	asked = append(asked, prefix+rowText)
	if len(asked) < 3 {
		t.Fatalf("question wraps to %d rows, want 3 or more", len(asked))
	}
	tl.body = append(append(tl.body, asked...), "", "✻ Worked for 6s · done 13:36")
	if got := said(tl); slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "CI cache") }) {
		t.Errorf("said = %q, want the question left to the status", got)
	}
	m := Model{width: 130, height: 64, snap: snapshot{tiles: []tile{tl}}}
	got := ansi.Strip(strings.Join(m.board().lines, "\n"))
	if n := strings.Count(got, "Should I also move"); n != 1 || !strings.Contains(got, "│    Should I also move the CI cache") {
		t.Errorf("question's start shown %d times, want once in the status:\n%s", n, got)
	}
	_, screen, ok := strings.Cut(got, "┄ screen")
	if !ok || !strings.Contains(screen, "ok  github.com/ppf/dev/internal/herd") {
		t.Fatalf("screen not the rows above the question:\n%s", got)
	}
	for _, r := range asked {
		if strings.Contains(screen, strings.TrimSpace(strings.TrimPrefix(r, "⏺"))) {
			t.Errorf("question's row %q on screen:\n%s", r, got)
		}
	}
}

// dev-demo-long-ask and dev-demo-short-ask as live (transcript, hook note, screen): each card says its question
// once, from its start, though its options outnumber a reply's last lines and its prompt above quotes it; the
// options its status can't fit show once, at its screen's end.
func TestAsksCardLiveDemos(t *testing.T) {
	long := "Before I go further on the migration I need you to pick how we handle the legacy session records that " +
		"still carry the old two-letter region codes, since rewriting them in place touches about forty thousand rows " +
		"across three shards and cannot be rolled back without a restore from last night's snapshot, which would drop " +
		"today's writes. Which way?"
	options := []string{"- Rewrite in place tonight during the maintenance window", "- Keep both formats and translate on read",
		"- Leave them and only fix new records"}
	row := func(r any) string {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	for _, tt := range []struct {
		name, golden, reply, start string
		lines                      int
	}{
		{"long", "claude-asks-options", long + "\n\n" + strings.Join(options, "\n"), "Before I go further", 4},
		{"short", "claude-asks-quoted", "Ship it now or wait for CI?", "Ship it now or wait for CI?", 1},
	} {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		jsonl := row(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": tt.reply}}}})
		if err := os.WriteFile(path, []byte(jsonl), 0o600); err != nil {
			t.Fatal(err)
		}
		read, err := desktop.ReadTail(path)
		if err != nil || len(read.Lines) != tt.lines {
			t.Fatalf("%s: ReadTail = %q, %v, want the question and its options", tt.name, read.Lines, err)
		}
		note, ok := inbox.FromHook(tt.name, []byte(row(map[string]any{"hook_event_name": "Stop", "last_assistant_message": tt.reply})))
		if !ok || note.Kind != inbox.Asks {
			t.Fatalf("%s: note = %+v, want an asks note", tt.name, note)
		}
		tl := tile{sess: sidebar.Session{Name: tt.name, Agent: "Claude", State: agent.Idle, Branch: tt.name}, note: note.Summary(), tail: read}
		tl.body = previewLines(screenGolden(t, tt.golden), tl)
		m := Model{width: 130, height: 64, snap: snapshot{tiles: []tile{tl}}}
		got := ansi.Strip(strings.Join(m.board().lines, "\n"))
		if n := strings.Count(got, tt.start); n != 1 || !strings.Contains(got, "│    "+tt.start) {
			t.Errorf("%s: question's start shown %d times, want once in the status:\n%s", tt.name, n, got)
		}
		_, screen, _ := strings.Cut(got, "┄ screen")
		if !strings.Contains(screen, "❯ This is a UI demo") {
			t.Errorf("%s: screen not the prompt above the question:\n%s", tt.name, got)
		}
		for _, s := range []string{"Which way?", "wait for CI?"} {
			if strings.Contains(screen, s) {
				t.Errorf("%s: %q on screen, want the question once, in the status:\n%s", tt.name, s, got)
			}
		}
		if tt.name != "long" {
			continue
		}
		for _, o := range options {
			if n := strings.Count(got, o); n != 1 || !strings.Contains(screen, o) {
				t.Errorf("long: %q shown %d times, want once, on screen:\n%s", o, n, got)
			}
		}
		if rows := strings.Split(screen, "\n"); !strings.Contains(rows[slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, "Open  Message") })-1], options[2]) {
			t.Errorf("long: screen doesn't end with the options:\n%s", got)
		}
	}
}

// A working card running a long Bash call shows its tool rows with their elapsed times on screen, though
// Claude pins its spinner far below them, over its input box.
func TestWorkingCardToolRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	data, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{
		"type": "tool_use", "name": "Bash", "input": map[string]any{
			"command": "python3 -c 'import time; time.sleep(100)'", "description": "Waiting 100 seconds via Python (run 2 of 4)"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := desktop.ReadTail(path)
	if err != nil {
		t.Fatal(err)
	}
	tl := tile{sess: sidebar.Session{Name: "busy", Agent: "Claude", State: agent.Working, Branch: "demo-busy"}, tail: read}
	tl.body = previewLines(screenGolden(t, "claude-working-bash"), tl)
	m := Model{width: 130, height: 64, snap: snapshot{tiles: []tile{tl}}}
	got := ansi.Strip(strings.Join(m.board().lines, "\n"))
	if !strings.Contains(got, "│    Waiting 100 seconds via Python (run 2 of 4)") {
		t.Errorf("status not the tool's description:\n%s", got)
	}
	_, screen, _ := strings.Cut(got, "┄ screen")
	for _, s := range []string{"Waiting 100 seconds via Python (run 2 of 4) · 11s", "⎿  $ python3 -c 'import time; time.sleep(100)' (9s)", "✶ Beaming… (15s"} {
		if !strings.Contains(screen, s) {
			t.Errorf("%q not on screen:\n%s", s, got)
		}
	}
}

// The same reply on a card not asking it keeps its question as a last line.
func TestSaidKeepsQuestionNotAsked(t *testing.T) {
	tl := asksTile(t)
	tl.note = "finished: done"
	if got := said(tl); len(got) != 2 || got[1] != "Want the same breakdown for sidebar?" {
		t.Errorf("said = %q", got)
	}
}

// A card's last lines leave out table rows and rules, markdown or box-drawn.
func TestSaidSkipsTables(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		// dev-demo-count's first reply as its transcript ends
		{"markdown rows", []string{"| skill | 31 |", "| integration | 10 |", "Should I count code lines only, leaving out blanks and comments?"},
			[]string{"Should I count code lines only, leaving out blanks and comments?"}},
		{"separator", []string{"Counts:", "|---|:--:|", "Done."}, []string{"Counts:", "Done."}},
		{"box-drawn", []string{"│ integration │ 1 │ 10 │", "└─────────────┴───┴────┘", "Done."}, []string{"Done."}},
		{"ascii box", []string{"+-----+----+", "Done."}, []string{"Done."}},
		{"a line with a pipe stays", []string{"- use a | b", "Done."}, []string{"- use a | b", "Done."}},
		{"only a table: none, not the note", []string{"| a | 1 |", "| b | 2 |"}, nil},
		{"only a marker: the note", []string{"IDLE"}, []string{"Counted. | a | 1 |"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := tile{note: "finished: Counted. | a | 1 |", tail: desktop.Tail{Lines: tt.lines}}
			if got := said(tl); !slices.Equal(got, tt.want) {
				t.Errorf("said = %q, want %q", got, tt.want)
			}
		})
	}
	// The asks card under a table says nothing more than its question
	tl := tile{sess: sidebar.Session{State: agent.Idle}, note: "asks: Should I count code lines only, leaving out blanks and comments?",
		tail: desktop.Tail{Lines: tests[0].lines}}
	if got := said(tl); len(got) != 0 {
		t.Errorf("asks said = %q", got)
	}
}

// A card entering Needs you shows screenRows of its screen though expanded before; v expands it there, and
// it stays expanded while it's there.
func TestNeedsYouDropsExpand(t *testing.T) {
	m := board4()
	m.expands = map[string]bool{"old-spike": true, "docs-api": true}
	snap := board4().snap
	snap.tiles[3].note = "asks: Merge #90?"
	snap.tiles[3].body = slices.Repeat([]string{"x"}, 30)
	sortBySection(snap.tiles)
	next, _ := m.Update(refreshMsg{snap: snap})
	m = next.(Model)
	spike := m.snap.tiles[index(m.snap.tiles, "old-spike")]
	if got := len(m.screenRows(spike, nil, 80)); got != screenRows || !m.expands["docs-api"] {
		t.Errorf("entering Needs you: %d rows, want %d; docs-api expanded %v", got, screenRows, m.expands["docs-api"])
	}
	m.selected = "old-spike"
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	next, _ = next.(Model).Update(refreshMsg{snap: snap})
	if m = next.(Model); len(m.screenRows(spike, nil, 80)) != m.height/2 {
		t.Errorf("v under Needs you: %d rows, want %d", len(m.screenRows(spike, nil, 80)), m.height/2)
	}
}

// A reply line still on a card's screen, below what the screen starts with, shows there once, not again
// above it; on any card (dev-herd-input-style's working card as the board showed it).
func TestSaidOnScreenOnce(t *testing.T) {
	said := "Codex review started in the background. ⏳ Waiting on it and /code-review."
	body := []string{`⏺ Agent "Code review herd box diff" finished · 56s`, "", "  Ran 1 shell command", "", "⏺ " + said, "", "✶ Perambulating… (3m 54s · ↓ 14.7k tokens)"}
	for _, state := range []string{agent.Working, agent.Idle} {
		m := board4()
		m.width, m.folds = 130, map[string]bool{"docs-api": false}
		m.snap.tiles[1].sess.State = state
		m.snap.tiles[1].tail.Lines = []string{"Second-opinion reviews:", said}
		m.snap.tiles[1].body = body
		got := ansi.Strip(strings.Join(m.board().lines, "\n"))
		if n := strings.Count(got, said); n != 1 || !strings.Contains(got, "Second-opinion reviews:") || !strings.Contains(got, "Ran 1 shell command") {
			t.Errorf("%s: reply line shown %d times:\n%s", state, n, got)
		}
	}
}

// An asks card's last lines and screen stop above its question and options, wherever the question is.
func TestAsksOptions(t *testing.T) {
	end := []string{"", "✻ Worked for 6s · done 13:36"}
	tests := []struct {
		name       string
		lines      []string
		body, said []string
		screen     []string
	}{
		{"question and options read", []string{"Intro.", "Which DB?", "- Postgres", "- SQLite"},
			[]string{"│ a │ 1 │", "", "⏺ Intro.", "", "  Which DB?", "", "  - Postgres", "  - SQLite"}, []string{"Intro."}, []string{"│ a │ 1 │"}},
		{"only options read", []string{"1. Postgres", "2. SQLite", "3. Both"},
			[]string{"│ a │ 1 │", "", "  Which DB", "  should it use?", "", "  1. Postgres", "  2. SQLite", "  3. Both"}, nil, []string{"│ a │ 1 │"}},
		{"only some options read", []string{"2. SQLite", "3. Both", "4. None"},
			[]string{"│ a │ 1 │", "", "  Which DB?", "  1. Postgres", "  2. SQLite", "  3. Both", "  4. None"}, nil, []string{"│ a │ 1 │"}},
		{"question wrapped on screen", []string{"Intro.", strings.Repeat("word ", 30) + "ok?"},
			[]string{"│ a │ 1 │", "", "⏺ Intro.", "", "  " + strings.Repeat("word ", 20), "  " + strings.Repeat("word ", 10) + "ok?"}, []string{"Intro."}, []string{"│ a │ 1 │"}},
		{"only * options read", []string{"* Postgres", "* SQLite"},
			[]string{"│ a │ 1 │", "", "  Which DB?", "", "  * Postgres", "  * SQLite"}, nil, []string{"│ a │ 1 │"}},
		{"italic question", []string{"Intro.", "*Which DB?*"},
			[]string{"│ a │ 1 │", "", "⏺ Intro.", "", "  Which DB?"}, []string{"Intro."}, []string{"│ a │ 1 │"}},
		{"question not on screen: screen whole", []string{"Intro.", "Which DB?"},
			[]string{"│ a │ 1 │", "", "  scrolled"}, []string{"Intro."}, []string{"│ a │ 1 │", "", "  scrolled", "", "✻ Worked for 6s · done 13:36"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := tile{sess: sidebar.Session{Name: "a", State: agent.Idle}, note: "asks: Which DB?", tail: desktop.Tail{Lines: tt.lines},
				body: append(slices.Clone(tt.body), end...)}
			if got := said(tl); !slices.Equal(got, tt.said) {
				t.Errorf("said = %q, want %q", got, tt.said)
			}
			if got := (Model{height: 40}).screenRows(tl, said(tl), 80); !slices.Equal(got, tt.screen) {
				t.Errorf("screen = %q, want %q", got, tt.screen)
			}
			if got := statusText(tl, needsYouSection); inbox.IsOption(got) {
				t.Errorf("status = %q, want the question", got)
			}
		})
	}
}

// An asks card with only its options read and its note cut keeps the options on screen, the only place they
// show whole.
func TestAsksOptionsNoteCut(t *testing.T) {
	tl := tile{sess: sidebar.Session{Name: "a", State: agent.Idle}, note: "asks: …" + strings.Repeat("word ", 40) + "ok? 1. Move",
		tail: desktop.Tail{Lines: []string{"1. Move", "2. Keep", "3. Both"}},
		body: []string{"⏺ Intro.", "", "  " + strings.Repeat("word ", 40) + "ok?", "", "  1. Move", "  2. Keep", "  3. Both"}}
	if got := (Model{}).screenRows(tl, said(tl), 80); !slices.Contains(got, "  3. Both") {
		t.Errorf("screen = %q, want the options", got)
	}
}

// An asks card's status says the reply's question and options from its start; its screen stops above the
// question whatever the status's width, then shows the options when the status doesn't fit them.
func TestAsksStatusFromReply(t *testing.T) {
	tl := tile{sess: sidebar.Session{Name: "a", State: agent.Idle}, note: "asks: Which DB? - Postgres - SQLite",
		tail: desktop.Tail{Lines: []string{"Intro.", "Which **DB**?", "- `Postgres`", "- SQLite"}},
		body: []string{"⏺ Ran 1 shell command", "", "⏺ Intro.", "", "  Which DB?", "", "  - Postgres", "  - SQLite"}}
	if got := statusText(tl, needsYouSection); got != "Which DB? - Postgres - SQLite" {
		t.Errorf("status = %q, want the reply's question and options", got)
	}
	listed := tl
	listed.tail.Lines = []string{"Intro.", "- Should I push?"}
	if got := statusText(listed, needsYouSection); got != "- Should I push?" {
		t.Errorf("status = %q, want the reply's question written as a list item", got)
	}
	if got := (Model{}).screenRows(tl, said(tl), 80); !slices.Equal(got, []string{"⏺ Ran 1 shell command"}) {
		t.Errorf("screen = %q, want the rows above the question", got)
	}
	if got := (Model{}).screenRows(tl, said(tl), 5); !slices.Equal(got, []string{"⏺ Ran 1 shell command", "  - Postgres", "  - SQLite"}) {
		t.Errorf("screen = %q, want the rows above the question, then its options: the status is cut", got)
	}
}

// A reply line kept above the screen when its screen rows are cut short on the card.
func TestSaidOnScreenCut(t *testing.T) {
	said := "Codex review started in the background."
	m := board4()
	m.folds = map[string]bool{"docs-api": false}
	m.snap.tiles[1].tail.Lines = []string{said}
	m.snap.tiles[1].body = []string{"⏺ Agent finished", "", "  " + strings.Repeat("x", 70), "", strings.Repeat(" ", 40) + said, "", "✶ Perambulating…"}
	if got := ansi.Strip(strings.Join(m.board().lines, "\n")); !strings.Contains(got, "    "+said+" ") {
		t.Errorf("reply line dropped for a cut screen row:\n%s", got)
	}
}

// A Claude worker's meta row carries its model and effort, a gold default model warning when no model was
// passed, nothing for Codex and Grok; a narrow card drops the branch first, then the model, never the cost.
func TestModelOnCard(t *testing.T) {
	claude := func(model, effort string) tile {
		return tile{sess: sidebar.Session{Agent: "Claude", Branch: "feature/long-branch-name", Cost: "$4.10"}, context: "62", model: model, effort: effort}
	}
	tests := []struct {
		name, want string
		t          tile
		w          int
	}{
		{"model and effort", "opus·medium  $4.10 · 62%", claude("opus", "medium"), 60},
		{"model alone", "opus  $4.10 · 62%", claude("opus", ""), 60},
		{"default", "default model  $4.10 · 62%", claude("", ""), 60},
		{"branch cut first", "opus·medium  $4.10 · 62%", claude("opus", "medium"), 36},
		{"model dropped before cost", " $4.10 · 62%", claude("opus", "medium"), 20},
	}
	for _, tt := range tests {
		got := ansi.Strip(metaRow(tt.t, tt.w))
		if !strings.HasSuffix(got, tt.want) || strings.Contains(got, "opus") != strings.Contains(tt.want, "opus") {
			t.Errorf("%s: %q, want suffix %q", tt.name, got, tt.want)
		}
		if w := ansi.StringWidth(got); w != tt.w-1 {
			t.Errorf("%s: %d wide, want %d", tt.name, w, tt.w-1)
		}
	}
	if got := metaRow(claude("", ""), 60); !strings.Contains(got, sidebar.Gold+"default model") {
		t.Errorf("default not gold: %q", got)
	}
	cx := claude("opus", "medium")
	cx.sess.Agent = "Codex"
	if got := ansi.Strip(metaRow(cx, 60)); strings.Contains(got, "opus") || strings.Contains(got, "default") {
		t.Errorf("codex shows a model: %q", got)
	}
}
