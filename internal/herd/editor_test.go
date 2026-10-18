package herd

import (
	"slices"
	"testing"
)

func TestEditorMoves(t *testing.T) {
	var e editor
	e.insert([]rune("first line\nsecond"))
	if e.down() || e.pos != 17 {
		t.Fatalf("down on the last line moved to %d", e.pos)
	}
	if !e.up() || e.pos != 6 {
		t.Errorf("up keeps the column: %d", e.pos)
	}
	if e.up() {
		t.Error("up on the first line moved")
	}
	e.end()
	if !e.down() || e.pos != 17 {
		t.Errorf("down from a longer line clamps to the end: %d", e.pos)
	}
	e.home()
	e.insert([]rune("a "))
	if e.String() != "first line\na second" {
		t.Errorf("home then insert: %q", e.String())
	}
	e.end()
	e.deleteWord()
	if e.String() != "first line\na " {
		t.Errorf("ctrl+w: %q", e.String())
	}
	e.killLine()
	if e.String() != "first line\n" || e.pos != 11 {
		t.Errorf("ctrl+u: %q at %d", e.String(), e.pos)
	}
	e.backspace()
	e.left()
	e.del()
	e.right()
	if e.String() != "first lin" || e.pos != 9 {
		t.Errorf("backspace, left, delete, right: %q at %d", e.String(), e.pos)
	}
}

func TestEditorRows(t *testing.T) {
	var e editor
	e.set("abcdef\nxy\tz\x1b")
	rows, cur := e.rows(4)
	if want := []string{"abcd", "ef", "xy z", "⏎\x1b[7m \x1b[27m"}; !slices.Equal(rows, want) || cur != 3 {
		t.Errorf("rows = %q cursor %d", rows, cur)
	}
	e.pos = 1
	if rows, cur = e.rows(4); rows[0] != "a\x1b[7mb\x1b[27mcd" || cur != 0 {
		t.Errorf("cursor mid-row: %q %d", rows, cur)
	}
	// A cursor at the end of a full row wraps to its own
	e.set("abcd")
	if rows, cur = e.rows(4); !slices.Equal(rows, []string{"abcd", "\x1b[7m \x1b[27m"}) || cur != 1 {
		t.Errorf("full row: %q %d", rows, cur)
	}
	e.set("")
	if rows, cur = e.rows(4); !slices.Equal(rows, []string{"\x1b[7m \x1b[27m"}) || cur != 0 {
		t.Errorf("empty: %q %d", rows, cur)
	}
}
