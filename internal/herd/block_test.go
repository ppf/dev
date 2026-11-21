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

// The block: whose items and the counts in its header, then Needs you and the columns but Done in board order, each
// row labelled by its assignee, a worker's state at its right edge, Waiting folded to its count and whom it waits on.
func TestBlockGroups(t *testing.T) {
	m := blockBoard(t)
	lines := plain(m.render())
	if got := strings.TrimRight(lines[0], " "); got != " TO DO · all bosses   3 need you · 4 mine · n" {
		t.Errorf("header = %q", got)
	}
	var titles []string
	for _, l := range lines {
		for _, title := range []string{"Needs you", "To do", "In progress", "Waiting", "Review", "Done"} {
			if strings.HasPrefix(strings.TrimLeft(l, " ▸▾"), title+"  ") {
				titles = append(titles, title)
			}
		}
	}
	if want := []string{"Needs you", "To do", "In progress", "Waiting"}; !slices.Equal(titles, want) {
		t.Errorf("titles %q, want %q", titles, want)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"noc › pr       Deploy the cache fix", "working", " ▸ Waiting  3   Carl · reviewers ×2",
		"kite › you     Ask Mara for the API key", "boss closed", "noc › you      Release now?"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Second review A") {
		t.Errorf("Waiting on others shows its rows while folded:\n%s", all)
	}
	head := m.render()[0]
	if !strings.Contains(head, sidebar.Red+"3 need you") || strings.Contains(head, sidebar.Red+"4 mine") {
		t.Errorf("only the need you number red: %q", head)
	}
	if strings.Contains(all, "boss closed · boss closed") {
		t.Errorf("boss closed said twice:\n%s", all)
	}
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "Deploy the cache fix") })
	if !strings.Contains(m.render()[i], sectionColours[workingSection]+"working") {
		t.Errorf("status word in the working colour: %q", m.render()[i])
	}
}

// ⏎ or a click on Waiting's row opens it under its row, ⏎ again folds it; its rows wait on whom they name.
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
	if !m.waitOpen || !strings.Contains(all, " ▾ Waiting  3") || !strings.Contains(all, "Second review A") || !strings.Contains(all, "waiting for reviewers") {
		t.Fatalf("open:\n%s", all)
	}
	m, _ = keys(m, "enter")
	if m.waitOpen {
		t.Error("⏎ again didn't fold it")
	}
	lines := plain(m.render())
	y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "▸ Waiting") })
	next, _ := m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = next.(Model); !m.waitOpen {
		t.Error("a click didn't open it")
	}
}

// At 64 columns the label column shrinks to 12 and a worker's state keeps the right edge.
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
// board's own list: its boss is here; the user's own on the manager's list starts, as on the web board).
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
	for _, want := range []string{"To do  24", "In progress  1", "Waiting  3", "more"} {
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
	if !strings.Contains(all, "Chore 19") || !strings.Contains(all, "Start   Tick      s start · d tick · space open · esc back") {
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
		if at[j] == i && len(spans[j]) > 1 && spans[j][1].key == "d" {
			y = j
		}
	}
	if y < 0 {
		t.Fatalf("no Tick on the expanded row:\n%s", strings.Join(plain(m.render()), "\n"))
	}
	next, cmd := m.Update(tea.MouseMsg{X: spans[y][1].x0, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd == nil {
		t.Fatalf("click on Tick: msg %q", next.(Model).msg.text)
	}
	cmd()
	if !slices.Equal(ticked, []int{2}) {
		t.Errorf("ticked %v, want #2", ticked)
	}
}

// An item an older dev filed on its own boss's name is the user's: mine, tickable with d.
func TestBlockOwnName(t *testing.T) {
	m := blockBoard(t)
	m.snap.asks[0].Items = append(m.snap.asks[0].Items, todo.Shown{ID: 9, Text: "Filed on its boss", On: "boss-noc"})
	m.snap.rollUp()
	rows := m.needRows()
	i := slices.IndexFunc(rows, func(r needRow) bool { return r.Text == "Filed on its boss" })
	if i < 0 || rows[i].Kind != sidebar.NeedPlain || rows[i].Status != sidebar.StatusYours {
		t.Fatalf("row %+v, want the user's own", rows)
	}
	if c := m.tally(); c.Mine != 5 {
		t.Errorf("tally %+v, want 5 mine", c)
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
	m.width = 40
	if got := strings.TrimRight(plain(m.render())[0], " "); got != " TO DO   3 need you · 4 mine · n" {
		t.Errorf("header = %q", got)
	}
}

// An item on a worker keeps a check its state doesn't say: "working · ⚠ unchanged 3d".
func TestBlockOnCheck(t *testing.T) {
	m := blockBoard(t)
	m.snap.asks[0].Items[2].Check = &sidebar.Check{Kind: sidebar.CheckUnchanged, Since: m.clk.Now().Add(-72 * time.Hour).Unix()}
	m.snap.rollUp()
	if all := strings.Join(plain(m.render()), "\n"); !strings.Contains(all, "working · ⚠ unchanged 3d") {
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
				strings.HasPrefix(l, " · ") || strings.HasPrefix(l, " ▸ Waiting") {
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
		if strings.Contains(all, "▸ Waiting") {
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
				text = "Waiting"
			}
			if all := strings.Join(plain(block), "\n"); !strings.Contains(all, ansi.Truncate(text, 10, "")) {
				t.Errorf("gh %d: selected row %d %q hidden:\n%s", gh, i, text, all)
			}
		}
	}
}

// s on a To do card asks for it to be started; the row then shows In progress, the boss's, whom it went to and since
// when, Tick kept for the user's own, Start back once late ("start not picked up"); a closed boss's row offers Start
// only while the manager runs; Review and the manager's own cards don't start.
func TestBlockStart(t *testing.T) {
	m := blockBoard(t)
	var started [][2]any
	m.startList = func(boss string, id int, text string) (string, error) {
		started = append(started, [2]any{boss, id})
		return "", nil
	}
	rows := m.needRows()
	i := slices.IndexFunc(rows, func(r needRow) bool { return r.Text == "DM David" })
	m.needMode, m.needIdx, m.needKey = true, i, rows[i].sig
	_, cmd := keys(m, "s")
	if cmd == nil {
		t.Fatal("s: nothing sent")
	}
	cmd()
	if !slices.Equal(started, [][2]any{{rows[i].Boss, rows[i].ID}}) {
		t.Errorf("started %v, want %s #%d", started, rows[i].Boss, rows[i].ID)
	}

	now := m.clk.Now()
	for _, tt := range []struct {
		late       bool
		want, acts string
	}{{false, "sent to boss-noc · 2m", "Tick   Go to boss      d tick"}, {true, "start not picked up", "Start   Tick   Go to boss      s start"}} {
		m := blockBoard(t)
		for j, it := range m.snap.asks[0].Items {
			if it.Text == "DM David" {
				m.snap.asks[0].Items[j].Started = &todo.Started{At: now.Add(-2 * time.Minute).Unix(), To: "boss-noc"}
				m.snap.asks[0].Items[j].StartLate = tt.late
			}
		}
		m.snap.rollUp()
		rows := m.needRows()
		i := slices.IndexFunc(rows, func(r needRow) bool { return r.Text == "DM David" })
		if rows[i].Status != sidebar.StatusStarting || rows[i].Column != sidebar.ColumnProgress || !rows[i].Tickable() {
			t.Fatalf("row %+v, want the user's own, starting", rows[i])
		}
		if c := m.tally(); c.Mine != 3 {
			t.Errorf("tally %+v, want 3 mine: a starting item is the boss's", c)
		}
		m, _ = keys(m, "n")
		for range i {
			m, _ = keys(m, "j")
		}
		all := strings.Join(plain(m.render()), "\n")
		if !strings.Contains(all, "In progress  2") || !strings.Contains(all, " noc            DM David") || !strings.Contains(all, tt.want) ||
			!strings.Contains(all, "         "+tt.acts) {
			t.Errorf("late %v: want In progress, the boss's, %q and %q:\n%s", tt.late, tt.want, tt.acts, all)
		}
		if rows[i].Yours() {
			t.Error("a starting row is in the sidebar's TO DO")
		}
	}

	check := rows[i]
	check.Column = sidebar.ColumnReview
	if _, cmd := m.startNeed(check); cmd != nil {
		t.Error("s started a Review card")
	}
	note := rows[i]
	note.Boss, note.On, note.Assignee = Manager, "boss", sidebar.NeedAssignee{Kind: sidebar.AssigneeBoss, Name: Manager}
	if acts, _ := m.actions(note); slices.ContainsFunc(acts, func(a action) bool { return a.key == "s" }) {
		t.Errorf("manager's own note offers Start: %v", acts)
	}
	if _, cmd := m.startNeed(note); cmd != nil {
		t.Error("s started the manager's own note")
	}
	// A boss's own To do card starts too, not only the user's
	parked := rows[i]
	parked.Assignee = sidebar.NeedAssignee{Kind: sidebar.AssigneeBoss, Name: "boss-noc"}
	if acts, _ := m.actions(parked); !slices.ContainsFunc(acts, func(a action) bool { return a.key == "s" }) {
		t.Errorf("a boss's To do card offers no Start: %v", acts)
	}
	closed := slices.IndexFunc(rows, func(r needRow) bool { return r.Closed })
	m.snap.all = slices.DeleteFunc(m.snap.all, func(s sidebar.Session) bool { return s.Name == sidebar.Manager })
	if acts, _ := m.actions(rows[closed]); len(acts) != 1 || acts[0].key != "d" {
		t.Errorf("closed boss, no manager: %v, want Tick alone", acts)
	}
	m.snap.all = append(m.snap.all, sidebar.Session{Name: sidebar.Manager})
	if acts, _ := m.actions(rows[closed]); len(acts) != 2 || acts[0].key != "s" {
		t.Errorf("closed boss, manager runs: %v, want Start then Tick", acts)
	}
}

// A started row names whom the start went to only where names fit, and keeps a moved PR's warning; one not the
// user's says so too, late or not.
func TestStartingRight(t *testing.T) {
	now := time.Unix(1700000000, 0)
	r := needRow{NeedRow: sidebar.NeedRow{Kind: sidebar.NeedPlain, Status: sidebar.StatusStarting, StartedTo: "boss-x",
		StartedAt: now.Add(-2 * time.Minute).Unix()}}
	if got, _ := r.right(now, true); got != "sent to boss-x · 2m" {
		t.Errorf("named = %q", got)
	}
	r.Check = &sidebar.Check{Kind: sidebar.CheckMerged, PR: 18, Since: now.Unix()}
	if got, _ := r.right(now, false); !strings.HasPrefix(got, "sent · 2m · ⚠ ") {
		t.Errorf("narrow, PR moved = %q", got)
	}
	parked := needRow{NeedRow: sidebar.NeedRow{Kind: sidebar.NeedOn, Status: sidebar.StatusIdle, StatusOn: "boss-x", StartedTo: "boss-x",
		StartedAt: now.Add(-12 * time.Minute).Unix(), StartLate: true}}
	if got, colour := parked.right(now, true); got != "start not picked up" || colour != sidebar.Red {
		t.Errorf("a boss's late start = %q", got)
	}
}

// Every list row sits in its item's column as the model placed it (todo.Placement, set by the refresh as dev todo
// --all --json sets it): the own list's, a boss's and a closed boss's alike; a boss's own item is the boss's, a moved
// PR's in Review, the user's doing in In progress with its doing text, a ticked one in none.
func TestBlockByColumn(t *testing.T) {
	m := blockBoard(t)
	now := m.clk.Now()
	m.snap.todo.Items = append(m.snap.todo.Items, todo.Shown{ID: 7, Text: "Parked", On: todo.Boss},
		todo.Shown{ID: 8, Text: "Ticked", Done: true})
	m.snap.asks[0].Items = append(m.snap.asks[0].Items,
		todo.Shown{ID: 9, Text: "Merged", Check: &sidebar.Check{Kind: sidebar.CheckMerged, PR: 4, Since: now.Unix()}},
		todo.Shown{ID: 10, Text: "On it", Doing: &todo.Doing{Text: "drafting", At: now.Unix()}, PartOf: 2})
	m.snap.rollUp()
	placed := map[string]todo.Shown{}
	for _, l := range append([]todo.BossList{{Boss: m.snap.name, List: m.snap.todo}}, m.snap.asks...) {
		for _, it := range l.Items {
			placed[l.Boss+"#"+strconv.Itoa(it.ID)] = it
		}
	}
	m.waitOpen = true
	n := 0
	for _, r := range m.needRows() {
		if r.Boss == "" || r.fold != nil {
			continue
		}
		n++
		it := placed[r.Boss+"#"+strconv.Itoa(r.ID)]
		if it.Column == "" || r.Column != it.Column || r.Group() != slices.Index(sidebar.Columns, it.Column) || r.Assignee.Kind != it.Assignee.Kind {
			t.Errorf("%s #%d %q: row %s/%s, item %s/%s", r.Boss, r.ID, r.Text, r.Column, r.Assignee.Kind, it.Column, it.Assignee.Kind)
		}
		if r.Text == "Ticked" {
			t.Error("a ticked item shown")
		}
	}
	if n != 11 {
		t.Errorf("%d list rows, want 11", n)
	}
	m.height = 60
	all := strings.Join(plain(m.render()), "\n")
	for _, want := range []string{"Review  1", "· manager        Parked", "On it  ▸ drafting", "⇄ #2"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing:\n%s", want, all)
		}
	}
	if strings.Contains(all, "\n Done  ") {
		t.Errorf("a Done group:\n%s", all)
	}
}

// s on a closed boss's row starts it through the manager; with no manager running, startNeed only says how to reopen it.
func TestBlockStartClosedKey(t *testing.T) {
	m := blockBoard(t)
	var started []int
	m.startList = func(boss string, id int, text string) (string, error) {
		started = append(started, id)
		return "", nil
	}
	rows := m.needRows()
	i := slices.IndexFunc(rows, func(r needRow) bool { return r.Closed })
	m.needMode, m.needIdx, m.needKey = true, i, rows[i].sig
	// The manager's board goes with the manager: no key reaches this, startNeed still refuses it
	all := m.snap.all
	m.snap.all = slices.DeleteFunc(slices.Clone(all), func(s sidebar.Session) bool { return s.Name == sidebar.Manager })
	next, cmd := m.startNeed(rows[i])
	if cmd != nil || next.(Model).msg.text != rows[i].ClosedHint() {
		t.Errorf("s, no manager: cmd %v msg %q", cmd != nil, next.(Model).msg.text)
	}
	m.snap.all = all
	if _, cmd := keys(m, "s"); cmd == nil {
		t.Fatal("s, manager runs: nothing sent")
	} else {
		cmd()
	}
	if !slices.Equal(started, []int{rows[i].ID}) {
		t.Errorf("started %v, want #%d", started, rows[i].ID)
	}
}

// A Review card says its check once, gold, no ⚠; a gone worker's card says gone.
func TestReviewRight(t *testing.T) {
	now := time.Unix(1700000000, 0)
	r := needRow{NeedRow: sidebar.NeedRow{Kind: sidebar.NeedOn, Boss: "b", Column: sidebar.ColumnReview, Status: sidebar.StatusCheck,
		Assignee: sidebar.NeedAssignee{Kind: sidebar.AssigneeWorker, Name: "w"}, Check: &sidebar.Check{Kind: sidebar.CheckMerged, PR: 4, Since: now.Unix()}}}
	for _, named := range []bool{true, false} {
		if got, colour := r.right(now, named); got != r.Check.Short(now) || colour != sidebar.Gold {
			t.Errorf("named %v: %q", named, got)
		}
	}
	gone := needRow{NeedRow: sidebar.NeedRow{Kind: sidebar.NeedOn, Boss: "b", Column: sidebar.ColumnTodo, Status: sidebar.StatusGone,
		Assignee: sidebar.NeedAssignee{Kind: sidebar.AssigneeBoss, Name: "b"}}}
	if got, _ := gone.right(now, true); got != "gone" {
		t.Errorf("gone = %q", got)
	}
	person := r
	person.Assignee, person.WaitingFor = sidebar.NeedAssignee{Kind: sidebar.AssigneeBoss, Name: "b"}, "Carl"
	if got, _ := person.right(now, true); got != r.Check.Short(now)+" · waiting for Carl" {
		t.Errorf("a person's Review card = %q", got)
	}
	unread := needRow{NeedRow: sidebar.NeedRow{Kind: sidebar.NeedOn, Boss: "b", Column: sidebar.ColumnProgress,
		Assignee: sidebar.NeedAssignee{Kind: sidebar.AssigneeWorker, Name: "w"}}}
	if got, _ := unread.right(now, true); got != "?" {
		t.Errorf("workers not read = %q", got)
	}
}
