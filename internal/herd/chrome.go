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

// previewLines is t's captured screen as its tile shows it: a Claude tile without Claude Code's input box,
// unless it waits on a decision, as its question or dialog is what the user answers.
func previewLines(capture string, t tile) []string {
	lines := screenLines(capture)
	if t.sess.Agent == "Claude" && !t.deciding {
		return cropInput(lines)
	}
	return lines
}

// cropInput drops Claude Code's own input box from the bottom of its captured screen: a ❯ prompt row
// between two full-width ─ rules, and the status lines under it. It looks like an input in a tile but
// takes nothing typed there. A screen without that box, or with a numbered option or dialog text between
// the rules (a question waiting for an answer), is returned unchanged.
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
	if !strings.HasPrefix(plain[top+1], "❯") {
		return lines
	}
	for _, l := range plain[top+1 : bottom] {
		// A question menu marks its selected option with ❯ too
		if l = strings.TrimSpace(strings.TrimPrefix(l, "❯")); startsNumbered(l) || strings.Contains(l, "Do you want") {
			return lines
		}
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
