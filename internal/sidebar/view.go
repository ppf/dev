package sidebar

import (
	"fmt"
	"slices"
	"strings"

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
	f := frame{lines: []string{header, ""}}

	if len(m.snap.sessions) == 0 {
		f.lines = append(f.lines, " "+muted+"No sessions"+reset)
	}
	prevGroup := ""
	for _, s := range m.snap.sessions {
		if s.Group != prevGroup {
			if prevGroup != "" {
				f.lines = append(f.lines, "")
			}
			label := truncate(s.Group, w-6)
			// printf '%*s' pads a negative width to its absolute value, so a cut label gets one dash
			n := w - 7 - ansi.StringWidth(label)
			if n < 0 {
				n = -n
			}
			f.lines = append(f.lines, " "+faint+"─"+reset+" "+muted+label+reset+" "+faint+strings.Repeat("─", n)+reset)
			prevGroup = s.Group
		}
		selected := s.Name == m.selected
		f.rows = append(f.rows, rowStart{line: len(f.lines), session: s.Name})

		display := truncate(s.Name, maxName)
		styled := display
		if s.Name == m.snap.current {
			styled = bold + display + reset
		}
		line := "  " + statusDot(s.Agent, s.State) + " " + styled
		vis := 4 + ansi.StringWidth(display)
		if s.Branch != "" {
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
		if s.Agent != "" {
			body, colour = s.Agent, muted
			if s.State != "" {
				body += " · " + s.State
			}
			if s.Title != "" {
				body += " · " + s.Title
			}
		}
		if s.State == agent.Waiting {
			colour = red
		}
		meta, vis := "", 4
		if s.PR.Number != 0 {
			// First, so the end-cut takes the title before the tag
			tag := truncate(fmt.Sprintf("#%d %s", s.PR.Number, s.PR.State), maxMeta)
			tw := ansi.StringWidth(tag)
			f.links = append(f.links, link{line: len(f.lines), from: vis, to: vis + tw, url: s.PR.URL})
			meta, vis = prColour(s.PR.State)+tag+reset, vis+tw
			body = " · " + body
		}
		body = truncate(body, maxMeta-(vis-4))
		meta += colour + body + reset
		f.lines = append(f.lines, cardLine("    "+meta, vis+ansi.StringWidth(body), w, selected))
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

// bottomRow is the last line: the refresh error, else the hint while focused on a non-empty list
// (bash returned before the hint when there were no sessions).
func (m Model) bottomRow() string {
	switch {
	case m.err != nil:
		return errorRow(m.err, m.snap.width)
	case m.snap.focused && len(m.snap.sessions) > 0:
		return hint
	}
	return ""
}

// errorRow is err in red on one line of width columns.
func errorRow(err error, width int) string {
	// One line: a tmux error carries stderr, whose newlines would add rows and scroll the header away
	msg := strings.Join(strings.Fields(err.Error()), " ")
	return " " + red + truncate(msg, width-1) + reset
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
	switch state {
	case agent.Working:
		return gold + "●" + reset
	case agent.Waiting:
		return red + "●" + reset
	case agent.Idle:
		return green + "●" + reset
	}
	if label != "" {
		return muted + "●" + reset
	}
	return faint + "○" + reset
}
