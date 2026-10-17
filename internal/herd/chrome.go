package herd

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	// maxBelowBox is how many status lines Claude Code draws under its input box.
	maxBelowBox = 6
	// maxBoxLines bounds a typed draft inside the box.
	maxBoxLines = 20
	// minRule is the shortest ─ run taken for the box's border.
	minRule = 10
)

// cropInput drops Claude Code's own input box from the bottom of its captured screen: a ❯ prompt row
// between two full-width ─ rules, and the status lines under it. It looks like an input in a tile but
// takes nothing typed there. A screen without that box, or with a numbered choice in it (a dialog
// waiting for an answer), is returned unchanged.
func cropInput(lines []string) []string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimSpace(ansi.Strip(l))
	}
	bottom := lastRule(plain, len(plain)-1, maxBelowBox+1)
	if bottom < 0 {
		return lines
	}
	top := lastRule(plain, bottom-1, maxBoxLines+1)
	if top < 0 || top == bottom-1 {
		return lines
	}
	prompt, ok := strings.CutPrefix(plain[top+1], "❯")
	if !ok || startsNumbered(strings.TrimSpace(prompt)) {
		return lines
	}
	end := top
	for end > 0 && plain[end-1] == "" {
		end--
	}
	return lines[:end]
}

// lastRule is the index of the last ─ rule at or above from, looking at most n lines up; -1 when none.
func lastRule(plain []string, from, n int) int {
	for i := from; i >= 0 && i > from-n; i-- {
		if len([]rune(plain[i])) >= minRule && strings.Trim(plain[i], "─") == "" {
			return i
		}
	}
	return -1
}

// startsNumbered reports a choice like "1. Yes".
func startsNumbered(s string) bool {
	digits := strings.TrimLeftFunc(s, unicode.IsDigit)
	return len(digits) < len(s) && strings.HasPrefix(digits, ".")
}
