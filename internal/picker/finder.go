package picker

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// chromeLines is the prompt, info and header lines above the list.
const chromeLines = 3

var (
	pointerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	currentStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
	headerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	infoStyle    = lipgloss.NewStyle().Faint(true)
	caretStyle   = lipgloss.NewStyle().Reverse(true)
	previewStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
)

// finder is an fzf-style list in reverse layout: query line on top, fuzzy-filtered rows below.
type finder struct {
	header  string
	rows    []string
	runes   [][]rune
	keys    [][]rune // lowercased rows
	preview func(row int) string
	cycle   bool

	query         []rune
	matches       []int // row indices, best match first
	cursor        int   // index into matches
	offset        int   // first visible match
	width, height int

	done    bool
	aborted bool
}

func newFinder(header string, rows []string, cycle bool, preview func(row int) string) finder {
	f := finder{header: header, rows: rows, cycle: cycle, preview: preview}
	for _, row := range rows {
		f.runes = append(f.runes, []rune(row))
		f.keys = append(f.keys, []rune(strings.ToLower(row)))
	}
	f.filter()
	return f
}

// picked returns the row under the cursor, -1 when nothing matches.
func (f finder) picked() int {
	if len(f.matches) == 0 {
		return -1
	}
	return f.matches[f.cursor]
}

func (f finder) Init() tea.Cmd { return nil }

func (f finder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	f = f.update(msg)
	if f.done {
		return f, tea.Quit
	}
	return f, nil
}

func (f finder) update(msg tea.Msg) finder {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width, f.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			f.done = true
		case tea.KeyEsc, tea.KeyCtrlC:
			f.done, f.aborted = true, true
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyCtrlK:
			f.move(-1)
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyCtrlJ:
			f.move(1)
		default:
			if q := edit(f.query, msg); !slices.Equal(q, f.query) {
				f.query = q
				f.filter()
			}
		}
	}
	f.scroll()
	return f
}

func (f *finder) move(delta int) {
	n := len(f.matches)
	if n == 0 {
		return
	}
	if f.cycle {
		f.cursor = (f.cursor + delta + n) % n
	} else {
		f.cursor = min(max(f.cursor+delta, 0), n-1)
	}
}

// filter re-ranks rows against the query and, like fzf, puts the cursor back on the top match.
// As in fzf's default extended mode, space-separated terms must all match, and a term is
// case-sensitive only when it has an uppercase letter.
func (f *finder) filter() {
	type hit struct{ row, start, span int }
	terms := strings.Fields(string(f.query))
	var hits []hit
rows:
	for i := range f.rows {
		h := hit{row: i}
		for j, term := range terms {
			text := f.keys[i]
			if term != strings.ToLower(term) {
				text = f.runes[i]
			}
			start, span, ok := match(text, []rune(term))
			if !ok {
				continue rows
			}
			if j == 0 {
				h.start = start
			}
			h.span += span
		}
		hits = append(hits, h)
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(a.span, b.span), cmp.Compare(a.start, b.start))
	})
	f.matches = make([]int, len(hits))
	for i, h := range hits {
		f.matches[i] = h.row
	}
	f.cursor, f.offset = 0, 0
}

// match finds query as a subsequence of text and returns the shortest span ending at the first
// complete match (fzf's v1 algorithm), so tighter and earlier matches rank first.
func match(text, query []rune) (start, span int, ok bool) {
	if len(query) == 0 {
		return 0, 0, true
	}
	end, q := -1, 0
	for i, r := range text {
		if r == query[q] {
			if q++; q == len(query) {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return 0, 0, false
	}
	q = len(query) - 1
	for start = end; ; start-- {
		if text[start] == query[q] {
			if q--; q < 0 {
				return start, end - start + 1, true
			}
		}
	}
}

func (f finder) visible() int {
	if f.height == 0 {
		return len(f.matches)
	}
	return max(f.height-chromeLines, 1)
}

func (f *finder) scroll() {
	if f.cursor < f.offset {
		f.offset = f.cursor
	}
	if v := f.visible(); f.cursor >= f.offset+v {
		f.offset = f.cursor - v + 1
	}
}

func (f finder) View() string {
	lines := []string{
		pointerStyle.Render(">") + " " + string(f.query) + caretStyle.Render(" "),
		infoStyle.Render(fmt.Sprintf("  %d/%d", len(f.matches), len(f.rows))),
		headerStyle.Render("  " + f.header),
	}
	for i := f.offset; i < min(len(f.matches), f.offset+f.visible()); i++ {
		row := f.rows[f.matches[i]]
		if i == f.cursor {
			lines = append(lines, pointerStyle.Render(">")+currentStyle.Render(" "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	if f.width == 0 {
		return strings.Join(lines, "\n")
	}

	listWidth := f.width
	if f.preview != nil {
		listWidth -= f.width / 2
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, listWidth, "")
	}
	list := lipgloss.NewStyle().Width(listWidth).Render(strings.Join(lines, "\n"))
	if f.preview == nil {
		return list
	}

	// Border takes 2 columns and 2 lines of the half-screen pane.
	paneWidth := f.width / 2
	var text string
	if row := f.picked(); row >= 0 {
		text = ansi.Truncate(f.preview(row), max(paneWidth-2, 0), "")
	}
	pane := previewStyle.Width(max(paneWidth-2, 0)).Height(max(f.height-2, 0)).Render(text)
	return lipgloss.JoinHorizontal(lipgloss.Top, list, pane)
}

// edit applies a line-editing keystroke: typing, space, backspace, ctrl+u.
func edit(line []rune, k tea.KeyMsg) []rune {
	switch k.Type {
	case tea.KeyRunes:
		if !k.Alt {
			return append(slices.Clip(line), k.Runes...)
		}
	case tea.KeySpace:
		return append(slices.Clip(line), ' ')
	case tea.KeyBackspace:
		if len(line) > 0 {
			return line[:len(line)-1]
		}
	case tea.KeyCtrlU:
		return nil
	}
	return line
}
