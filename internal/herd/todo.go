package herd

import (
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
	items := l.Items[:min(todoShown, len(l.Items))]
	if len(items) < len(l.Items) || len(items) > room {
		// Room for the "+N more" row
		items = items[:min(len(items), room-1)]
	}
	for _, it := range items {
		rows = append(rows, todoItem(it, w))
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

// todoItem is one item's row: its number, ☐ (◌ waiting on someone else, ☑ ticked, struck through), its text
// cut with …, and at the right who it waits on, else its PR's number (↗ for another link), cut to a third
// of the row.
func todoItem(it todo.Shown, w int) string {
	box, text := "☐", ""
	switch {
	case it.Done:
		box, text = sidebar.Green+"☑"+reset, rule+strike
	case it.On != "":
		box, text = sidebar.Muted+"◌"+reset, sidebar.Muted
	}
	right := ""
	switch {
	case it.On != "":
		right = sidebar.Gold + "on " + it.On + reset
	case it.PR() != 0:
		right = sidebar.Green + "○ #" + strconv.Itoa(it.PR()) + reset
	case it.Link != "":
		right = sidebar.Muted + "↗" + reset
	}
	if right != "" {
		right = " " + ansi.Truncate(right, max(1, w/3), "…") + reset + " "
	}
	left := " " + rule + strconv.Itoa(it.ID) + reset + " " + box + " " + text + it.Text + reset
	left = ansi.Truncate(left, max(1, w-max(1, ansi.StringWidth(right))), "…") + reset
	return left + strings.Repeat(" ", max(0, w-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}
