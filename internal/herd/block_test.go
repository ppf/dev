package herd

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// blockBoard is the manager's board with boss-noc's items handed out (a worker, Carl, reviewers twice) and a closed
// boss's, statuses set as a refresh sets them.
func blockBoard(t *testing.T) Model {
	m := needsBoard(t, new([]listAnswer))
	noc := managerAsks[0]
	noc.Items = append(slices.Clone(noc.Items),
		todo.Shown{ID: 3, Text: "Deploy the cache fix", On: "noc-pr"},
		todo.Shown{ID: 4, Text: "Carl reviews the key change", On: "Carl"},
		todo.Shown{ID: 5, Text: "Second review A", On: "reviewers"},
		todo.Shown{ID: 6, Text: "Second review B", On: "reviewers"},
	)
	m.snap.asks = []todo.BossList{noc, {Boss: "boss-kite", Closed: true, List: todo.List{Items: []todo.Shown{
		{ID: 1, Text: "Ask Mara for the API key", Check: &sidebar.Check{Kind: sidebar.CheckBossClosed}}}}}}
	m.snap.rollUp()
	return m
}

// The block: whose items and the split count in its header, then the groups in sidebar.Groups order, each row's
// status word at its right edge naming whom it's on, Waiting on others folded to its count and whom it waits on.
func TestBlockGroups(t *testing.T) {
	m := blockBoard(t)
	lines := plain(m.render())
	if got := strings.TrimRight(lines[0], " "); got != " TO DO · all bosses   2 need you · 1 to decide · 2 yours · n" {
		t.Errorf("header = %q", got)
	}
	var titles []string
	for _, l := range lines {
		for _, title := range []string{"Needs you", "To decide", "Yours", "In progress", "Waiting on others", "Check", "Boss closed"} {
			if strings.HasPrefix(strings.TrimLeft(l, " ▸▾"), title+"  ") {
				titles = append(titles, title)
			}
		}
	}
	if want := []string{"Needs you", "To decide", "Yours", "In progress", "Waiting on others", "Boss closed"}; !slices.Equal(titles, want) {
		t.Errorf("titles %q, want %q", titles, want)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"Deploy the cache fix", "working · noc-pr", " ▸ Waiting on others  3   Carl · reviewers ×2", "Ask Mara for the API key", "boss closed"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Second review A") {
		t.Errorf("Waiting on others shows its rows while folded:\n%s", all)
	}
	head := m.render()[0]
	if !strings.Contains(head, sidebar.Red+"1 to decide") || strings.Contains(head, sidebar.Red+"2 yours") {
		t.Errorf("only the decide number red: %q", head)
	}
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Deploy the cache fix") })
	if !strings.Contains(m.render()[i], sectionColours[workingSection]+"working · noc-pr") {
		t.Errorf("status word in the working colour: %q", m.render()[i])
	}
}

// ⏎ or a click on Waiting on others' row opens it under its row, ⏎ again folds it; its rows wait on whom they name.
func TestBlockFoldWaiting(t *testing.T) {
	m := blockBoard(t)
	m.height = 60
	fold := slices.IndexFunc(m.needRows(), func(r needRow) bool { return r.fold != nil })
	m, _ = keys(m, "n")
	for range fold {
		m, _ = keys(m, "j")
	}
	m, _ = keys(m, "enter")
	all := strings.Join(plain(m.render()), "\n")
	if !m.waitOpen || !strings.Contains(all, " ▾ Waiting on others  3") || !strings.Contains(all, "Second review A") || !strings.Contains(all, "waiting · reviewers") {
		t.Fatalf("open:\n%s", all)
	}
	m, _ = keys(m, "enter")
	if m.waitOpen {
		t.Error("⏎ again didn't fold it")
	}
	lines := plain(m.render())
	y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Waiting on others") })
	next, _ := m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = next.(Model); !m.waitOpen {
		t.Error("a click didn't open it")
	}
}

// At 64 columns the label column shrinks to 12 and the status word keeps the right edge without whom it names.
func TestBlockNarrow(t *testing.T) {
	m := blockBoard(t)
	m.width = 64
	lines := plain(m.render())
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Deploy the cache") })
	if got := strings.TrimRight(lines[i], " "); !strings.HasSuffix(got, " working") || strings.Contains(got, "noc-pr") {
		t.Errorf("row = %q", got)
	}
	if w := labelWidth(m.needRows(), 64); w != 12 {
		t.Errorf("label column %d, want 12", w)
	}
	for j, l := range m.render() {
		if w := ansi.StringWidth(l); w > 64 {
			t.Errorf("line %d is %d wide", j, w)
		}
	}
}

// Many items keep the block in half the pane: each group shows as many rows as the others, "+N more" for the rest,
// every group's title on screen; the selected row stays shown, expanded with its action words (no Go to boss on the
// board's own list: its boss is here).
func TestBlockBudget(t *testing.T) {
	m := blockBoard(t)
	for i := range 20 {
		m.snap.todo.Items = append(m.snap.todo.Items, todo.Shown{ID: 10 + i, Text: "Chore " + strconv.Itoa(i)})
	}
	m.snap.rollUp()
	m.height = 40
	gh := m.height - len(m.bottom())
	block, _, _ := m.needBlock(gh)
	all := strings.Join(plain(block), "\n")
	if len(block) > gh/2 {
		t.Errorf("block %d lines in %d rows:\n%s", len(block), gh, all)
	}
	for _, want := range []string{"Yours  22", "In progress  1", "Boss closed  1", "more"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	// The last chore, selected: shown with its action words
	m, _ = keys(m, "n")
	last := slices.IndexFunc(m.needRows(), func(r needRow) bool { return r.Text == "Chore 19" })
	for range last {
		m, _ = keys(m, "j")
	}
	block, _, _ = m.needBlock(gh)
	all = strings.Join(plain(block), "\n")
	if !strings.Contains(all, "Chore 19") || !strings.Contains(all, "Tick      d tick · esc back") {
		t.Errorf("selected row not shown with its actions:\n%s", all)
	}
}

// A click on an expanded row's Tick ticks it, as d does; on Go to boss it goes there, as g does.
func TestBlockActionClick(t *testing.T) {
	m := blockBoard(t)
	var ticked []int
	m.tickList = func(boss string, id int, text string) (string, error) {
		ticked = append(ticked, id)
		return "", nil
	}
	i := slices.IndexFunc(m.needRows(), func(r needRow) bool { return r.Text == "DM David" })
	m, _ = keys(m, "n")
	for range i {
		m, _ = keys(m, "j")
	}
	_, at, spans := m.needBlock(m.height - len(m.bottom()))
	y := -1
	for j := range at {
		if at[j] == i && len(spans[j]) > 0 && spans[j][0].key == "d" {
			y = j
		}
	}
	if y < 0 {
		t.Fatalf("no Tick on the expanded row:\n%s", strings.Join(plain(m.render()), "\n"))
	}
	next, cmd := m.Update(tea.MouseMsg{X: spans[y][0].x0, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatalf("click on Tick: msg %q", next.(Model).msg.text)
	}
	cmd()
	if !slices.Equal(ticked, []int{2}) {
		t.Errorf("ticked %v, want #2", ticked)
	}
}

// An item an older dev filed on its own boss's name is the user's: Yours, tickable with d.
func TestBlockOwnName(t *testing.T) {
	m := blockBoard(t)
	m.snap.asks[0].Items = append(m.snap.asks[0].Items, todo.Shown{ID: 9, Text: "Filed on its boss", On: "boss-noc"})
	m.snap.rollUp()
	rows := m.needRows()
	i := slices.IndexFunc(rows, func(r needRow) bool { return r.Text == "Filed on its boss" })
	if i < 0 || rows[i].Kind != sidebar.NeedPlain || rows[i].Status != sidebar.StatusYours {
		t.Fatalf("row %+v, want the user's own", rows)
	}
	if c := m.tally(); c.ToDo != 3 {
		t.Errorf("tally %+v, want 3 yours", c)
	}
	var ticked [][2]any
	m.tickList = func(boss string, id int, text string) (string, error) {
		ticked = append(ticked, [2]any{boss, id})
		return "", nil
	}
	m.needMode, m.needIdx, m.needKey = true, i, rows[i].sig
	_, cmd := keys(m, "d")
	if cmd == nil {
		t.Fatal("d: nothing sent")
	}
	cmd()
	if !slices.Equal(ticked, [][2]any{{"boss-noc", 9}}) {
		t.Errorf("ticked %v, want boss-noc #9", ticked)
	}
}

// t folds the block to its header, from the cards or from the block, and n opens it again.
func TestBlockFold(t *testing.T) {
	m := blockBoard(t)
	m, _ = keys(m, "n", "t")
	block, _, _ := m.needBlock(m.height - len(m.bottom()))
	if m.needMode || len(block) != 2 || !strings.HasPrefix(ansi.Strip(block[0]), " ▸ TO DO") {
		t.Errorf("t in the block: mode %v, block %q", m.needMode, plain(block))
	}
	m, _ = keys(m, "t")
	if m.todoFolded {
		t.Error("t on the cards didn't open it")
	}
	m, _ = keys(m, "t", "n")
	if m.todoFolded || !m.needMode {
		t.Errorf("n: folded %v mode %v", m.todoFolded, m.needMode)
	}
}

// At rest in a short pane the groups that don't fit go whole into one "+N more", counting a folded group's rows:
// the block keeps to its rows and every row is accounted for.
func TestBlockShortAtRest(t *testing.T) {
	m := blockBoard(t)
	gh := 20
	block, _, _ := m.needBlock(gh)
	all := strings.Join(plain(block), "\n")
	if len(block) > gh/2 {
		t.Errorf("block %d lines in %d rows:\n%s", len(block), gh, all)
	}
	shown := 0
	for _, l := range plain(block) {
		if strings.HasPrefix(l, " ● ") || strings.HasPrefix(l, " ? ") || strings.HasPrefix(l, " ☐ ") || strings.HasPrefix(l, " ○ ") {
			shown++
		}
	}
	more := 0
	for _, l := range plain(block) {
		if f := strings.Fields(l); len(f) == 2 && f[1] == "more" {
			n, _ := strconv.Atoi(strings.TrimPrefix(f[0], "+"))
			more += n
		}
	}
	if total := len(m.listRows()); shown+more != total {
		t.Errorf("%d shown + %d more, want %d rows:\n%s", shown, more, total, all)
	}
}

// In a short pane the selected row shows even when the groups before it fill the room: they go to "+N more".
func TestBlockShortSelectedLast(t *testing.T) {
	m := blockBoard(t)
	rows := m.needRows()
	last := len(rows) - 1
	m.needMode, m.needIdx, m.needKey = true, last, rows[last].sig
	block, _, _ := m.needBlock(20)
	if all := strings.Join(plain(block), "\n"); !strings.Contains(all, rows[last].Text) || !strings.Contains(all, "more") {
		t.Errorf("selected %q not shown:\n%s", rows[last].Text, all)
	}
}

// A short pane at rest still shows the first row whole: a lone dialog with its options, not just "+1 more".
func TestBlockShortFirstRow(t *testing.T) {
	m := needsBoard(t, new([]listAnswer))
	m.snap.asks, m.snap.todo = nil, todo.List{}
	m.snap.rollUp()
	block, _, _ := m.needBlock(10)
	all := strings.Join(plain(block), "\n")
	if !strings.Contains(all, "allow Bash: rm -rf build") || !strings.Contains(all, "1 Yes   2 No") {
		t.Errorf("dialog not shown:\n%s", all)
	}
}

// A header too wide keeps its counts and key in, leaving out whose items.
func TestBlockHeadNarrow(t *testing.T) {
	m := blockBoard(t)
	m.width = 50
	if got := strings.TrimRight(plain(m.render())[0], " "); got != " TO DO   2 need you · 1 to decide · 2 yours · n" {
		t.Errorf("header = %q", got)
	}
}

// An item on a worker keeps a check its status doesn't say: "working · noc-pr · ⚠ unchanged 3d".
func TestBlockOnCheck(t *testing.T) {
	m := blockBoard(t)
	m.snap.asks[0].Items[2].Check = &sidebar.Check{Kind: sidebar.CheckUnchanged, Since: m.clk.Now().Add(-72 * time.Hour).Unix()}
	m.snap.rollUp()
	if all := strings.Join(plain(m.render()), "\n"); !strings.Contains(all, "working · noc-pr · ⚠ unchanged 3d") {
		t.Errorf("check missing:\n%s", all)
	}
}

// Over every short height from 9 rows (below, the block may take the pane): at rest the first row shows and every row is
// shown or in a "+N more"; any selected row shows; the block keeps within the pane.
func TestBlockShortSweep(t *testing.T) {
	base := blockBoard(t)
	count := func(block []string) (shown, more int) {
		for _, l := range plain(block) {
			if strings.HasPrefix(l, " ● ") || strings.HasPrefix(l, " ? ") || strings.HasPrefix(l, " ☐ ") || strings.HasPrefix(l, " ○ ") ||
				strings.HasPrefix(l, " ▸ Waiting") {
				shown++
			}
			if f := strings.Fields(l); len(f) == 2 && f[1] == "more" {
				n, _ := strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				more += n
			}
		}
		return shown, more
	}
	rows := base.needRows()
	total := len(rows) - 1 + len(rows[slices.IndexFunc(rows, func(r needRow) bool { return r.fold != nil })].fold)
	for gh := 9; gh <= 30; gh++ {
		block, _, _ := base.needBlock(gh)
		all := strings.Join(plain(block), "\n")
		if len(block) > gh-1 {
			t.Errorf("gh %d: block of %d lines", gh, len(block))
		}
		if !strings.Contains(all, "allow Bash: rm -rf build") {
			t.Errorf("gh %d: first row hidden:\n%s", gh, all)
		}
		// The fold row stands for its rows
		shown, more := count(block)
		if strings.Contains(all, "Waiting on others") {
			shown += len(rows[slices.IndexFunc(rows, func(r needRow) bool { return r.fold != nil })].fold) - 1
		}
		if shown+more != total {
			t.Errorf("gh %d: %d shown + %d more, want %d:\n%s", gh, shown, more, total, all)
		}
		for i, r := range rows {
			m := base
			m.needMode, m.needIdx, m.needKey = true, i, r.sig
			block, _, _ := m.needBlock(gh)
			text := r.Text
			if r.fold != nil {
				text = "Waiting on others"
			}
			if all := strings.Join(plain(block), "\n"); !strings.Contains(all, ansi.Truncate(text, 10, "")) {
				t.Errorf("gh %d: selected row %d %q hidden:\n%s", gh, i, text, all)
			}
		}
	}
}
