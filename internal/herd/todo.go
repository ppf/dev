package herd

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

const (
	// todoShown is how many items the to do block lists before "+N more".
	todoShown = 5
	strike    = "\x1b[9m"
)

// panelItems are the own list's items the to do block lists: those on someone else, or ticked.
func (m Model) panelItems() []todo.Shown {
	return slices.DeleteFunc(slices.Clone(m.snap.todo.Items), func(it todo.Shown) bool { return !it.Done && it.On == "" })
}

// todoRows is the to do block above the cards: the boss's own list (panelItems), in at most half the gh rows above
// the bottom rows (header in any): header (TO DO n, those still open on others, ── updated <age>), up to todoShown items, an answered ask's with
// its answer's row, "+N more"; header only while folded (t); none if empty, unreadable (NEEDS YOU says why) or no
// rows.
func (m Model) todoRows(gh int) []string {
	w := m.width
	items := m.panelItems()
	if gh < 1 || m.snap.todoErr != nil || len(items) == 0 {
		return nil
	}
	l := m.snap.todo
	rows := []string{m.todoHeader(l, w)}
	room := max(0, gh/2-1)
	if m.todoFolded || room == 0 {
		return rows
	}
	all := m.todoOrder(items)
	shown := 0
	for i, it := range all[:min(todoShown, len(all))] {
		item := []string{todoItem(it.Shown, it.owner, w)}
		if r := todoOptions(it.Shown, w); r != "" {
			item = append(item, r)
		}
		need := len(item)
		if i < len(all)-1 {
			// Room for the "+N more" row
			need++
		}
		if len(rows)-1+need > room {
			break
		}
		rows, shown = append(rows, item...), shown+1
	}
	if more := len(items) - shown; more > 0 {
		rows = append(rows, sidebar.HintRow("  +"+strconv.Itoa(more)+" more", w))
	}
	return rows
}

// todoHeader is the block's header: ▸ first while folded; its age dims once the list is stale.
func (m Model) todoHeader(l todo.List, w int) string {
	_, on := l.Open()
	left := ""
	if m.todoFolded {
		left = " " + rule + "▸" + reset
	}
	left += " " + bold + "TO DO"
	if on > 0 {
		left += " " + strconv.Itoa(on)
	}
	left += reset + " "
	age := "updated " + span(m.clk.Now().Sub(time.Unix(l.Updated, 0)))
	ageColour := sidebar.Muted
	if l.Stale {
		ageColour = rule
	}
	right := " " + ageColour + age + reset + " "
	if ansi.StringWidth(left)+ansi.StringWidth(right) > w {
		right = ""
	}
	left = ansi.Truncate(left, w-ansi.StringWidth(right), "…")
	fill := max(0, w-ansi.StringWidth(left)-ansi.StringWidth(right))
	return left + rule + strings.Repeat("─", fill) + reset + right
}

// owner is the worker a to do item is on, as this refresh saw it: its name and section, or gone.
type owner struct {
	name  string
	sec   section
	gone  bool
	until string // when its planned wait ends, as boards show it; "" for none
}

// todoOwner is the worker it is on: its linked session, else a worker named as its on is; nil for a person or
// none. A linked session with no agent shows idle, one no longer listed gone.
func (m Model) todoOwner(it todo.Shown) *owner {
	if it.Done || it.On == "" {
		return nil
	}
	// Its own workers: a deeper session is its boss's
	var tiles []todo.Session
	for _, t := range m.snap.tiles {
		if t.via == "" {
			tiles = append(tiles, todo.Session{ID: t.sess.Name, Name: t.sess.Name})
		}
	}
	on := it.On
	if it.Session != nil {
		on = it.Session.ID
	}
	if w, ok := todo.Match(on, tiles); ok && (it.Session == nil || w.ID == on) {
		t := m.snap.tiles[index(m.snap.tiles, w.Name)]
		o := &owner{name: w.Name, sec: t.section()}
		if o.sec == waitingSection && t.sess.Wait.Held() {
			o.until = wait.Clock(t.sess.Wait.Until, m.clk.Now())
		}
		return o
	}
	switch {
	case it.Session == nil:
		return nil
	case m.snap.started[on]:
		return &owner{name: on, sec: restingSection}
	}
	return &owner{name: it.Session.Name, gone: true}
}

// ownedItem is an item with the worker it is on, nil for none.
type ownedItem struct {
	todo.Shown
	owner *owner
}

// todoOrder is the items as the block lists them: those on no one (ticked: open ones are NEEDS YOU's) first, then
// on a worker that needs the user, then on a worker that's gone, then the rest, each group in the list's order.
func (m Model) todoOrder(items []todo.Shown) []ownedItem {
	rank := func(it ownedItem) int {
		switch o := it.owner; {
		case it.On == "":
			return 0
		case o != nil && o.gone:
			return 2
		case o != nil && o.sec == needsYouSection:
			return 1
		}
		return 3
	}
	out := make([]ownedItem, len(items))
	for i, it := range items {
		out[i] = ownedItem{it, m.todoOwner(it)}
	}
	slices.SortStableFunc(out, func(a, b ownedItem) int { return cmp.Compare(rank(a), rank(b)) })
	return out
}

// todoItem is one item's row: its number, ☐ (◌ waiting on someone else, ☑ ticked, struck through), its text
// cut with …, and at the right who does it: a worker's dot in its section's colour, name and section, or ✕ and
// session gone, in two fifths of the row (more for the state in a narrow pane), cutting the name first; else the
// person, else its PR's number (↗ for another link), cut to a third of the row.
func todoItem(it todo.Shown, o *owner, w int) string {
	box, text := "☐", ""
	switch {
	case it.Done:
		box, text = sidebar.Green+"☑"+reset, rule+strike
	case it.On != "":
		box, text = sidebar.Muted+"◌"+reset, sidebar.Muted
	}
	right := ""
	switch {
	case o != nil:
		what := strings.ToLower(sectionNames[o.sec])
		if o.until != "" {
			what += " until " + o.until
		}
		mark, state := sectionColours[o.sec]+"●"+reset+" ", sidebar.Muted+" · "+reset+sectionColours[o.sec]+what+reset
		name := sidebar.Muted + o.name + reset
		if o.gone {
			mark, name, state = sidebar.Red+"✕ ", sidebar.Red+o.name, sidebar.Red+" · session gone"+reset
		}
		// Room for the state and a cut name, past two fifths if need be; a pane too narrow for that cuts the state too
		cell := min(max(w*2/5, ansi.StringWidth(mark+state)+3), max(1, w-6))
		room := max(1, cell-ansi.StringWidth(mark+state)-2)
		right = " " + ansi.Truncate(mark+ansi.Truncate(name, room, "…")+reset+state, max(1, cell-2), "…") + reset + " "
	case it.On != "":
		right = sidebar.Gold + "on " + it.On + reset
	case it.PR() != 0:
		right = sidebar.Green + "○ #" + strconv.Itoa(it.PR()) + reset
	case it.Link != "":
		right = sidebar.Muted + "↗" + reset
	}
	if right != "" && o == nil {
		right = " " + ansi.Truncate(right, max(1, w/3), "…") + reset + " "
	}
	left := " " + rule + strconv.Itoa(it.ID) + reset + " " + box + " " + text + it.Text + reset
	left = ansi.Truncate(left, max(1, w-max(1, ansi.StringWidth(right))), "…") + reset
	return left + strings.Repeat(" ", max(0, w-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}

// todoOptions is the row under an open ask: its options numbered, ★ on the recommended one; under an answered item
// what was answered; "" for any other item.
func todoOptions(it todo.Shown, w int) string {
	var b strings.Builder
	switch {
	case it.Done && it.Answer != "":
		b.WriteString(sidebar.Green + "answered: " + it.Answer + reset)
	case !it.Done && len(it.Options) > 0:
		for i, o := range it.Options {
			label, rec := todo.Label(o)
			if rec {
				label = sidebar.Gold + "★ " + reset + label
			}
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(rule + strconv.Itoa(i+1) + reset + " " + label)
		}
	default:
		return ""
	}
	// Under the item's text: past its number and box
	row := ansi.Truncate(strings.Repeat(" ", len(strconv.Itoa(it.ID))+4)+b.String(), max(1, w-1), "…") + reset
	return row + strings.Repeat(" ", max(0, w-ansi.StringWidth(row)))
}
