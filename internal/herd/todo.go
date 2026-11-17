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
)

const (
	// todoShown is how many items the to do block lists before "+N more".
	todoShown = 5
	strike    = "\x1b[9m"
)

// todoRows is the boss's list above the cards, in at most half the gh rows above the bottom rows (header in any):
// header (TO DO n · k on others ── updated <age>), up to todoShown items, "+N more"; header only while folded (t);
// an alert row if unreadable; none if empty or no rows.
func (m Model) todoRows(gh int) []string {
	w := m.width
	if gh < 1 {
		return nil
	}
	if m.snap.todoErr != nil {
		return []string{sidebar.AlertRow("❌ to do: "+m.snap.todoErr.Error(), w)}
	}
	l := m.snap.todo
	if len(l.Items) == 0 {
		return nil
	}
	rows := []string{m.todoHeader(l, w)}
	room := max(0, gh/2-1)
	if m.todoFolded || room == 0 {
		return rows
	}
	all := m.todoOrder(l.Items)
	items := all[:min(todoShown, len(all))]
	if len(items) < len(all) || len(items) > room {
		// Room for the "+N more" row
		items = items[:min(len(items), room-1)]
	}
	for _, it := range items {
		rows = append(rows, todoItem(it.Shown, it.owner, w))
	}
	if more := len(l.Items) - len(items); more > 0 {
		rows = append(rows, sidebar.HintRow("  +"+strconv.Itoa(more)+" more", w))
	}
	return rows
}

// todoHeader is the block's header: ▸ first while folded; its age dims once the list is stale.
func (m Model) todoHeader(l todo.List, w int) string {
	n, on := l.Open()
	left := ""
	if m.todoFolded {
		left = " " + rule + "▸" + reset
	}
	left += " " + bold + "TO DO " + strconv.Itoa(n) + reset + " "
	if on > 0 {
		left += sidebar.Muted + "· " + strconv.Itoa(on) + " on others" + reset + " "
	}
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
	name string
	sec  section
	gone bool
}

// todoOwner is the worker it is on: its linked session, else a worker named as its on is; nil for a person or
// none. A linked session with no agent shows idle, one no longer listed gone.
func (m Model) todoOwner(it todo.Shown) *owner {
	if it.Done || it.On == "" {
		return nil
	}
	tiles := make([]todo.Session, len(m.snap.tiles))
	for i, t := range m.snap.tiles {
		tiles[i] = todo.Session{ID: t.sess.Name, Name: t.sess.Name}
	}
	on := it.On
	if it.Session != nil {
		on = it.Session.ID
	}
	if w, ok := todo.Match(on, tiles); ok && (it.Session == nil || w.ID == on) {
		return &owner{name: w.Name, sec: m.snap.tiles[slices.IndexFunc(tiles, func(s todo.Session) bool { return s == w })].section()}
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

// todoOrder is the items as the block lists them: the user's own first, then those on a worker that needs the
// user, then on a worker that's gone, then the rest, each group in the list's order.
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
		mark, state := sectionColours[o.sec]+"●"+reset+" ", sidebar.Muted+" · "+reset+sectionColours[o.sec]+strings.ToLower(sectionNames[o.sec])+reset
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
