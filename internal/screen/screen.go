// Package screen reads agents' captured screens: where Claude Code's own input box is, and what its
// screen shows when no dialog is open.
package screen

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/ppf/dev/internal/agent"
)

const (
	// MaxBelowBox is how many status lines an agent draws under its input box.
	MaxBelowBox = 6
	// MaxBoxLines bounds a typed draft's lines.
	MaxBoxLines = 20
	// MinRule is the shortest ─ run taken for the box's border.
	MinRule = 10
)

// Plain is lines without colour or surrounding spaces.
func Plain(lines []string) []string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return plain
}

// ClaudeInput finds Claude Code's own input box at the bottom of a screen's Plain lines: a ❯ prompt row
// between two ─ rules (MinRule or longer), and the status lines under it; top and bottom are its rules.
// ok=false without that box, or with a numbered option or dialog text between the rules (a question waiting
// for an answer). Claude draws a dialog in the box's place.
func ClaudeInput(plain []string) (top, bottom int, ok bool) {
	bottom = lastRule(plain, len(plain)-1, MaxBelowBox+1)
	if bottom < 0 {
		return 0, 0, false
	}
	top = lastRule(plain, bottom-1, MaxBoxLines+1)
	if top < 0 || top == bottom-1 || !strings.HasPrefix(plain[top+1], "❯") {
		return 0, 0, false
	}
	for _, l := range plain[top+1 : bottom] {
		// A question menu marks its selected option with ❯ too
		if AsksChoice(strings.TrimPrefix(l, "❯")) {
			return 0, 0, false
		}
	}
	return top, bottom, true
}

// ClaudeState is what Claude's captured screen shows when its own input box is at the bottom, so no
// dialog is open: agent.Working while it offers "esc to interrupt" (in the status lines under the box, or
// on the spinner line above it), else agent.Idle. "" without the box: a dialog, or a screen it doesn't know.
func ClaudeState(capture string) string {
	plain := Plain(strings.Split(capture, "\n"))
	for len(plain) > 0 && plain[len(plain)-1] == "" {
		plain = plain[:len(plain)-1]
	}
	top, bottom, ok := ClaudeInput(plain)
	if !ok {
		return ""
	}
	interruptible := func(l string) bool { return strings.Contains(l, "esc to interrupt") }
	above := top - 1 // the spinner line: the last line above the box that isn't blank
	for above >= 0 && plain[above] == "" {
		above--
	}
	if slices.ContainsFunc(plain[bottom+1:], interruptible) || above >= 0 && interruptible(plain[above]) {
		return agent.Working
	}
	return agent.Idle
}

// AsksChoice reports an input line that is a numbered option or dialog text, not a draft.
func AsksChoice(l string) bool {
	l = strings.TrimSpace(l)
	return startsNumbered(l) || strings.Contains(l, "Do you want")
}

// lastRule is the index of the last ─ rule at or above from, looking at most n lines up; -1 when none.
func lastRule(plain []string, from, n int) int {
	for i := from; i >= 0 && i > from-n; i-- {
		if len([]rune(plain[i])) >= MinRule && strings.Trim(plain[i], "─") == "" {
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
