package sidebar

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
)

// Escapes byte for byte as sidebar.sh prints them; lipgloss would re-encode them per colour profile.
const (
	gold       = "\033[38;2;192;163;110m"
	green      = "\033[38;2;152;187;108m"
	red        = "\033[38;2;228;104;118m"
	muted      = "\033[38;2;114;113;105m"
	faint      = "\033[38;2;84;84;109m"
	bold       = "\033[1m"
	reset      = "\033[0m"
	selectedBG = "\033[48;2;42;42;55m"
)

// hint is the bottom row while the sidebar is focused: the keys only act then.
const hint = " " + faint + "j/k ⏎  d new  x kill  r ren  q back" + reset

// View renders the latest snapshot.
func (m Model) View() string {
	if m.snap.height == 0 {
		// No snapshot yet: only a refresh error shows, on the bottom row of Bubble Tea's window size.
		// Without one the height is unknown, and Bubble Tea keeps only the last height lines.
		if m.err == nil || m.winHeight == 0 {
			return ""
		}
		lines := make([]string, m.winHeight)
		lines[m.winHeight-1] = errorRow(m.err, m.winWidth)
		return strings.Join(lines, "\n")
	}
	return strings.Join(m.render().lines, "\n")
}

// render lays the sidebar out for the model's snapshot and selection (sidebar.sh render).
func (m Model) render() frame {
	w, h := m.snap.width, m.snap.height
	btn := w - 4
	buttons := btn >= 12
	right := w - 2
	if buttons {
		right = btn - 1
	}
	maxName := max(1, right-4)
	maxMeta := max(1, w-5)

	header := " " + gold + bold + "SESSIONS" + reset
	if buttons {
		header += strings.Repeat(" ", btn-9) + gold + bold + "+" + reset
	}
	needs, hits := m.renderNeeds(w, h)
	f := frame{lines: append(needs, header, ""), needs: hits, header: len(needs)}

	if len(m.snap.sessions) == 0 {
		f.lines = append(f.lines, " "+muted+"No sessions"+reset)
	}
	lists := NeedCounts(m.needs)
	unwrapped := len(needs) + listLines(m.snap.sessions, lists)
	rolls := Subtree(m.snap.sessions, lists)
	for i, s := range m.snap.sessions {
		if h, ok := headFor(m.snap.sessions, i); ok {
			if h.gap {
				f.lines = append(f.lines, "")
			}
			f.lines = append(f.lines, h.line(w))
		}
		selected := s.Name == m.selected
		f.rows = append(f.rows, rowStart{line: len(f.lines), session: s.Name})

		// A worker sits under the session that started it. Deep nesting gives way before the name: one name
		// column always stays left of the buttons, so a click on the name never lands on ×
		indent := ""
		if s.Depth > 0 && maxName > 2 {
			indent = strings.Repeat("  ", min(s.Depth-1, (maxName-3)/2)) + "└ "
		}
		iw := ansi.StringWidth(indent)
		display := truncate(s.Name, max(1, maxName-iw))
		styled := display
		if s.Name == m.snap.current {
			styled = bold + display + reset
		}
		lead := s.lead()
		// An idle session whose note asks a question reads as a dialog does: red dot, "asks"; one its boss answers
		// rests, "asks its boss", Claude's idle reminder too
		asks := s.State != agent.Waiting && s.Rank() == RankNeedsYou
		asksBoss := s.AsksBoss()
		dotState := s.State
		switch {
		case asks:
			dotState = agent.Waiting
		case asksBoss:
			dotState = agent.Idle
		}
		line := lead + " " + statusDot(s.Agent, dotState) + " " + styled
		if indent != "" {
			line = lead + " " + faint + indent + reset + statusDot(s.Agent, dotState) + " " + styled
		}
		vis := 4 + iw + ansi.StringWidth(display)
		if s.Branch != "" && !s.Named {
			if bmax := right - vis - 2; bmax > 3 {
				bt := truncate(s.Branch, bmax)
				pad := max(1, bmax-ansi.StringWidth(bt))
				line += strings.Repeat(" ", pad) + muted + " " + bt + reset
				vis += pad + 1 + ansi.StringWidth(bt)
			}
		}
		if buttons {
			x := faint
			if selected {
				x = red
			}
			line += strings.Repeat(" ", max(1, btn-vis)) + x + "×" + reset
			vis = btn + 1
		}
		f.lines = append(f.lines, cardLine(line, vis, w, selected))

		body, colour := "no agent", faint
		cost, title := "", ""
		if s.Agent != "" {
			body, colour = s.Agent, muted
			switch {
			// A dialog says waiting, not the planned wait
			case s.Wait.Held() && !s.Deciding && s.Agent == "Claude":
				body = s.Wait.Text(time.Now())
			case s.Wait.Held() && !s.Deciding:
				body += " · " + s.Wait.Text(time.Now())
			case asks && s.Agent == "Claude":
				body = "asks"
			case asks:
				body += " · asks"
			case asksBoss && s.Agent == "Claude":
				body, colour = StatusWord(StatusAsks), gold
			case asksBoss:
				body, colour = body+" · "+StatusWord(StatusAsks), gold
			case s.State == "":
			case s.Agent == "Claude":
				// Claude is the default agent: its name is left out of the line
				body = s.State
			default:
				body += " · " + s.State
			}
			cost, title = s.Cost, s.Title
		}
		if s.State == agent.Waiting && !asksBoss || asks {
			colour = red
		}
		// The second line starts under the name, past the indent
		meta, vis := "", 4+iw
		if s.PR.Number != 0 {
			// First, so the end-cut takes the title before the tag
			tag := truncate(fmt.Sprintf("#%d %s", s.PR.Number, s.PR.State), maxMeta-iw)
			tw := ansi.StringWidth(tag)
			f.links = append(f.links, link{line: len(f.lines), from: vis, to: vis + tw, url: s.PR.URL})
			meta, vis = prColour(s.PR.State)+tag+reset, vis+tw
			body = " · " + body
		}
		avail := maxMeta - (vis - 4)
		// The cost only whole, before the title: a cut "$18…" would read as another amount
		withCost := cost != "" && ansi.StringWidth(body+" · "+cost) <= avail
		// Selected row wraps a cut title onto lines below only if every session still fits (no scrolling)
		var wrapped []string
		if selected && title != "" {
			full := body
			if withCost {
				full += " · " + cost
			}
			if ansi.StringWidth(full+" · "+title) > avail {
				if l := strings.Split(ansi.Wrap(title, maxMeta-iw, ""), "\n"); unwrapped+len(l) < h {
					wrapped, title = l, ""
				}
			}
		}
		if withCost {
			body += " · " + cost
			// " · ", then at least one character of the title before its "…"
			if rest := avail - ansi.StringWidth(body); title != "" && rest > 4 {
				body += " · " + truncate(title, rest-3)
			}
		} else if title != "" {
			body += " · " + title
		}
		body = truncate(body, avail)
		meta += colour + body + reset
		f.lines = append(f.lines, cardLine(lead+"   "+strings.Repeat(" ", iw)+meta, vis+ansi.StringWidth(body), w, selected))
		for _, l := range wrapped {
			f.lines = append(f.lines, cardLine(lead+"   "+strings.Repeat(" ", iw)+colour+l+reset, 4+iw+ansi.StringWidth(l), w, selected))
		}
		if r := rolls[s.Name]; hasRollLine(r) {
			text, tw := rollLine(r, maxMeta-iw)
			f.lines = append(f.lines, cardLine(lead+"   "+strings.Repeat(" ", iw)+text, 4+iw+tw, w, selected))
		}
	}

	// Exactly h lines: content reaching the bottom row gives way to it, as bash drew that row last
	f.lines = f.lines[:min(len(f.lines), h)]
	for len(f.lines) < h {
		f.lines = append(f.lines, "")
	}
	if h > 0 {
		f.lines[h-1] = m.bottomRow()
	}
	// A tag cut off or under the bottom row isn't on screen to click
	f.links = slices.DeleteFunc(f.links, func(l link) bool { return l.line >= h-1 })
	return f
}

// listLines is how many lines render draws for sessions when no title wraps.
func listLines(sessions []Session, lists map[string]ListCount) int {
	n := 2
	rolls := Subtree(sessions, lists)
	for i := range sessions {
		if h, ok := headFor(sessions, i); ok {
			n++
			if h.gap {
				n++
			}
		}
		n += 2
		if hasRollLine(rolls[sessions[i].Name]) {
			n++
		}
	}
	return n
}

// head is a divider line above sessions[i]: a group's, a manager's, or a boss's group inside a manager's.
type head struct {
	label string
	kind  headKind
	gap   bool // a blank line goes first
}

type headKind int

const (
	headGroup   headKind = iota // ─ label ───
	headManager                 // ┌─ MANAGER ───, opening a manager's group
	headSub                     // ├─ label ───, a boss's group inside it
)

// headFor is the divider above sessions[i], if any: a manager's group has one header, then a divider
// each time its boss's group changes; other rows get one per group, and one right after a manager's group.
func headFor(sessions []Session, i int) (head, bool) {
	s := sessions[i]
	if i == 0 {
		if s.Root != "" {
			return head{label: "MANAGER", kind: headManager}, true
		}
		return head{label: s.Group}, true
	}
	prev := sessions[i-1]
	switch {
	case s.Root != prev.Root && s.Root != "":
		return head{label: "MANAGER", kind: headManager, gap: true}, true
	case s.Root != "" && s.Root == prev.Root:
		// Each boss opens its own, even in the manager's group
		return head{label: s.Group, kind: headSub}, s.Group != prev.Group || s.Depth == 0
	case s.Group != prev.Group || prev.Root != "":
		return head{label: s.Group, gap: true}, true
	}
	return head{}, false
}

func (h head) line(width int) string {
	start, colour := " "+faint+"─"+reset, muted
	switch h.kind {
	case headManager:
		start, colour = faint+"┌─"+reset, gold+bold
	case headSub:
		start = faint + "├─" + reset
	}
	label := truncate(h.label, width-6)
	// printf '%*s' pads a negative width to its absolute value, so a cut label gets one dash
	n := width - 7 - ansi.StringWidth(label)
	if n < 0 {
		n = -n
	}
	return start + " " + colour + label + reset + " " + faint + strings.Repeat("─", n) + reset
}

// lead is a row's first column: a faint rail inside a manager's group, else a space.
func (s Session) lead() string {
	if s.Root == "" {
		return " "
	}
	return faint + "│" + reset
}

// bottomRow is the last line: the refresh error, else the hint while focused on a non-empty list
// (bash returned before the hint when there were no sessions).
func (m Model) bottomRow() string {
	switch {
	case m.err != nil:
		return errorRow(m.err, m.snap.width)
	case m.answerErr != nil:
		return AlertRow(m.answerErr.Error(), m.snap.width)
	case m.reload.Err != nil:
		return AlertRow("❌ "+m.reload.Err.Error(), m.snap.width)
	case m.reload.Held() != "":
		// Held: above the TO DO hint, since TO DO mode is what holds it
		return " " + gold + truncate("↻ "+m.reload.Held()+" ready: reloads when idle", m.snap.width-1) + reset
	case m.needMode:
		return m.needsHint()
	case m.needsErr != nil:
		return AlertRow(m.needsErr.Error(), m.snap.width)
	case m.reload.Flash != "":
		return " " + green + truncate("↻ dev "+m.reload.Flash, m.snap.width-1) + reset
	case m.snap.focused && len(m.snap.sessions) > 0:
		return hint
	}
	return ""
}

// errorRow is err in red on one line of width columns.
func errorRow(err error, width int) string {
	// One line: a tmux error carries stderr, whose newlines would add rows and scroll the header away
	return AlertRow(err.Error(), width)
}

// truncate cuts s to max display columns, ending in … when cut; max <= 0 gives "" (truncate_text).
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	return ansi.Truncate(s, max, "…")
}

// cardLine gives a selected line the selection background across width columns; visible is the
// content's display width (build_card_line).
func cardLine(content string, visible, width int, selected bool) string {
	if !selected {
		return content
	}
	// Each reset would end the background mid-row, e.g. after the bold current name
	with := strings.ReplaceAll(content, reset, reset+selectedBG)
	return selectedBG + with + strings.Repeat(" ", max(0, width-visible)) + reset
}

// prColour is a PR tag's colour by state.
func prColour(state string) string {
	switch state {
	case "open":
		return green
	case "merged":
		return gold
	case "closed":
		return muted
	}
	return faint
}

// statusDot is the coloured dot for an agent label and state (status_dot).
func statusDot(label, state string) string {
	if label == "" && state == "" {
		return faint + "○" + reset
	}
	return StateColour(label, state) + "●" + reset
}

// StateColour is the escape colouring an agent label and state's dot: gold working, red waiting, green
// idle, muted for an agent without state, faint for none.
func StateColour(label, state string) string {
	switch state {
	case agent.Working:
		return gold
	case agent.Waiting:
		return red
	case agent.Idle:
		return green
	}
	if label != "" {
		return muted
	}
	return faint
}

// CostHidden reports whether a session's @dev_cost (set globally or on the session) hides its cost.
func CostHidden(opt string) bool {
	return opt == "off"
}

// FormatCost renders a session cost in USD for the sidebar and dev agent.
func FormatCost(usd float64) string {
	return fmt.Sprintf("$%.2f", usd)
}

// hasRollLine reports a boss row with a third line: a boss with sessions under it, questions or to do items on no
// one, or a cost of closed sessions (or a ledger not read).
func hasRollLine(r *Rollup) bool {
	return r != nil && (r.Under > 0 || r.Questions > 0 || r.Decide > 0 || r.ToDo > 0 || r.ClosedUSD > 0 || r.CostErr != "")
}

// rollLine is a boss's roll-up (Subtree) as its third line in width columns and its display width: the state
// with its ⚠ (a bare idle or working is left out, the row above says it), what waits on the user as the sidebar's TO
// DO says it, "N need you" and "N decide" red, "N to do" muted, then its counts (a manager's bosses · workers first),
// what it and everything under it cost, closed sessions included, and "N on others" last and only whole; the cost
// goes before the counts do, "N on others" before the cost.
func rollLine(r *Rollup, width int) (string, int) {
	var plain, styled string
	add := func(colour, text string) {
		if plain != "" {
			plain += " · "
			styled += muted + " · " + reset
		}
		plain += text
		styled += colour + text + reset
	}
	fits := func(text string) bool {
		if plain == "" {
			return ansi.StringWidth(text) <= width
		}
		return ansi.StringWidth(plain+" · "+text) <= width
	}
	// What waits on the user, never cut: by the state, or leading
	var user, parts []string
	for _, p := range r.Parts() {
		if strings.HasSuffix(p, " "+NeedYouWord) || strings.HasSuffix(p, " "+DecideWord) || strings.HasSuffix(p, " "+ToDoWord) {
			user = append(user, p)
		} else {
			parts = append(parts, p)
		}
	}
	// "no workers" ranks last: the rows under the boss already show it, so it only fills an otherwise empty line
	n := len(parts)
	parts = slices.DeleteFunc(parts, func(p string) bool { return p == "no workers" })
	noWorkers := len(parts) < n
	// A bare idle or working is the row above's, left out; a held wait or resume ("waiting") it doesn't say
	state := ""
	if r.Rank != RankNeedsYou && len(parts) > 0 && (parts[0] == "idle" || parts[0] == "working") {
		state = parts[0]
	}
	stateDropped := state != "" && (len(user) > 0 || !r.Warn)
	if stateDropped {
		parts = parts[1:]
	}
	lead := 0        // the parts before a manager's bosses and workers
	byState := false // what waits on the user follows the state
	switch {
	case len(user) == 0:
	case r.Rank == RankNeedsYou || stateDropped || len(parts) == 0:
		parts = append(user, parts...)
		lead = len(user)
	default:
		// The counts stay by the state, where the width never cuts them
		parts = slices.Concat(parts[:1], user, parts[1:])
		lead, byState = 1+len(user), true
	}
	if r.Bosses == 0 && !byState {
		lead = len(parts)
	} else if lead == 0 && !stateDropped {
		// A manager's: its bosses and workers after the state, then the other counts only whole
		lead = 1
	}
	if r.OnOthers > 0 {
		// Last, only whole
		lead = min(lead, len(parts)-1)
	}
	for i, p := range parts[:lead] {
		colour := muted
		if strings.HasSuffix(p, " "+NeedYouWord) || strings.HasSuffix(p, " "+DecideWord) {
			colour = red
		}
		add(colour, p)
		if i == 0 && r.Warn {
			// By the state, where the cut never takes it
			plain, styled = plain+" ⚠", styled+" "+gold+"⚠"+reset
		}
	}
	rest, others := parts[lead:], ""
	if r.OnOthers > 0 {
		rest, others = rest[:len(rest)-1], rest[len(rest)-1]
	}
	dropped := false
	if r.Bosses > 0 {
		// Whole or dropped from the end: workers before bosses
		group := []string{plural(r.Bosses, "boss", "bosses")}
		if w := r.Under - r.Bosses; w > 0 {
			group = append(group, plural(w, "worker", "workers"))
		}
		for _, p := range group {
			if !dropped && fits(p) {
				add(muted, p)
			} else {
				dropped = true
			}
		}
	}
	for _, p := range rest {
		if !dropped && fits(p) {
			add(muted, p)
		} else {
			dropped = true
		}
	}
	if r.HasCost && !dropped {
		if cost := FormatCost(r.CostUSD); fits(cost) {
			add(muted, cost)
		}
	}
	if others != "" && fits(others) {
		add(muted, others)
	}
	if plain == "" && stateDropped {
		// Nothing else fits: the state rather than an empty line
		if noWorkers {
			state = "no workers"
		}
		add(muted, state)
	}
	return truncate(styled, width), min(ansi.StringWidth(plain), max(width, 0))
}

// plural is "1 one" or "n many".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
