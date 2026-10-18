package herd

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// editor is the reply box's draft: text with a cursor, edited across lines.
type editor struct {
	text []rune
	pos  int
}

func (e *editor) set(s string) { e.text, e.pos = []rune(s), len([]rune(s)) }

func (e editor) String() string { return string(e.text) }

func (e *editor) insert(r []rune) {
	e.text = slices.Insert(e.text, e.pos, r...)
	e.pos += len(r)
}

func (e *editor) backspace() {
	if e.pos > 0 {
		e.text = slices.Delete(e.text, e.pos-1, e.pos)
		e.pos--
	}
}

func (e *editor) del() {
	if e.pos < len(e.text) {
		e.text = slices.Delete(e.text, e.pos, e.pos+1)
	}
}

func (e *editor) left()  { e.pos = max(0, e.pos-1) }
func (e *editor) right() { e.pos = min(len(e.text), e.pos+1) }

// lineStart is the index starting the line holding i.
func (e editor) lineStart(i int) int {
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd is the index of the newline ending the line holding i, or the text's end.
func (e editor) lineEnd(i int) int {
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

func (e *editor) home() { e.pos = e.lineStart(e.pos) }
func (e *editor) end()  { e.pos = e.lineEnd(e.pos) }

// up moves to the line above at the same column; false on the first line.
func (e *editor) up() bool {
	start := e.lineStart(e.pos)
	if start == 0 {
		return false
	}
	prev := e.lineStart(start - 1)
	e.pos = prev + min(e.pos-start, start-1-prev)
	return true
}

// down moves to the line below at the same column; false on the last line.
func (e *editor) down() bool {
	end := e.lineEnd(e.pos)
	if end == len(e.text) {
		return false
	}
	col := e.pos - e.lineStart(e.pos)
	e.pos = end + 1 + min(col, e.lineEnd(end+1)-end-1)
	return true
}

// killLine deletes back to the line's start (ctrl+u).
func (e *editor) killLine() {
	start := e.lineStart(e.pos)
	e.text = slices.Delete(e.text, start, e.pos)
	e.pos = start
}

// deleteWord deletes the word before the cursor and the spaces after it (ctrl+w).
func (e *editor) deleteWord() {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.text[i-1]) && e.text[i-1] != '\n' {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.text[i-1]) {
		i--
	}
	e.text = slices.Delete(e.text, i, e.pos)
	e.pos = i
}

// rows is the draft wrapped to width columns, the cursor in reverse video, and the row holding it.
func (e editor) rows(width int) (rows []string, cursor int) {
	width = max(1, width)
	var b strings.Builder
	w := 0
	flush := func() {
		rows = append(rows, b.String())
		b.Reset()
		w = 0
	}
	for i := 0; i <= len(e.text); i++ {
		if i == len(e.text) || e.text[i] == '\n' {
			if i == e.pos {
				if w == width {
					flush()
				}
				cursor = len(rows)
				b.WriteString(cursorCell)
			}
			flush()
			continue
		}
		c := shown(e.text[i])
		cw := max(1, ansi.StringWidth(c))
		if w+cw > width {
			flush()
		}
		if i == e.pos {
			cursor = len(rows)
			c = "\x1b[7m" + c + "\x1b[27m"
		}
		b.WriteString(c)
		w += cw
	}
	return rows, cursor
}

// shown is how a draft rune shows: a tab as a space, other control characters (a pasted escape) as ⏎, as
// they would break the row; the text sent keeps them.
func shown(r rune) string {
	switch {
	case r == '\t':
		return " "
	case r < ' ' || r == 0x7f:
		return "⏎"
	}
	return string(r)
}
