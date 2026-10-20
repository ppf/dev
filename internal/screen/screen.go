// Package screen reads agents' captured screens: where Claude Code's own input box is, and what its
// screen shows when no dialog is open.
package screen

import (
	"regexp"
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
// between two ─ rules (MinRule or longer), and the status lines under it, maybe with its agent panel under
// them; top and bottom are its rules. ok=false without that box, or with a numbered option or dialog text
// between the rules (a question waiting for an answer). Claude draws a dialog in the box's place.
func ClaudeInput(plain []string) (top, bottom int, ok bool) {
	bottom = lastRule(plain, len(withoutPanel(plain))-1, MaxBelowBox+1)
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

// panelRow is a row of the agent panel Claude draws under its status lines while it has teammates or
// background agents ("⏺ main", "◯ sleep-a  Sleep 90 and report  21s"), the selected one marked ❯.
var panelRow = regexp.MustCompile(`^(❯\s+)?[⏺◯●○◉]\s+\S`)

// withoutPanel is plain without the agent panel at its bottom and the blank lines above it: one row per
// agent, so it grows past MaxBelowBox.
func withoutPanel(plain []string) []string {
	end := len(plain)
	for end > 0 && panelRow.MatchString(plain[end-1]) {
		end--
	}
	if end == len(plain) {
		return plain
	}
	for end > 0 && plain[end-1] == "" {
		end--
	}
	return plain[:end]
}

// plainScreen is a captured screen's Plain lines without the blank ones at its bottom.
func plainScreen(capture string) []string {
	plain := Plain(strings.Split(capture, "\n"))
	for len(plain) > 0 && plain[len(plain)-1] == "" {
		plain = plain[:len(plain)-1]
	}
	return plain
}

// ClaudeState is what Claude's captured screen shows when its own input box is at the bottom, so no
// dialog is open: agent.Working while it offers "esc to interrupt" (in the status lines under the box, or
// on the spinner line above it) or shows its spinner line ("✻ Nucleating… (5s)", no "esc to interrupt"
// since 2.1.304; maybe with a tip under it), else agent.Idle. "" without the box: a dialog, or a screen it
// doesn't know.
func ClaudeState(capture string) string {
	plain := plainScreen(capture)
	top, bottom, ok := ClaudeInput(plain)
	if !ok {
		return ""
	}
	if slices.ContainsFunc(plain[bottom+1:], interruptible) {
		return agent.Working
	}
	raw := strings.Split(capture, "\n")
	for i, seen := top-1, 0; i >= 0 && seen < 2; i-- {
		if plain[i] == "" {
			continue
		}
		// The spinner starts its line; a reply's lines (a "* item…" too) are indented
		if interruptible(plain[i]) || spinner.MatchString(ansi.Strip(raw[i])) && !strings.HasPrefix(plain[i], "⎿") {
			return agent.Working
		}
		seen++
	}
	return agent.Idle
}

func interruptible(l string) bool { return strings.Contains(l, "esc to interrupt") }

// ClaudeInterrupted reports Claude's screen after Esc ended its turn, which fires no hook: its idle input box
// under "⎿  Interrupted · What should Claude do instead?", maybe with a status line ("◐ medium · /effort")
// between. A prompt sent since, or a spinner, sits between them instead.
func ClaudeInterrupted(capture string) bool {
	if ClaudeState(capture) != agent.Idle {
		return false
	}
	plain := plainScreen(capture)
	top, _, _ := ClaudeInput(plain)
	for i, seen := top-1, 0; i >= 0 && seen < 3; i-- {
		switch l := plain[i]; {
		case l == "":
			continue
		case strings.HasPrefix(l, "⎿") && strings.Contains(l, "Interrupted"):
			return true
		case strings.HasPrefix(l, "❯") || spinner.MatchString(l):
			return false
		}
		seen++
	}
	return false
}

// ClaudeQueued reports a prompt queued while Claude works: it reads it at its turn's next step, showing
// "Press up to edit queued messages" in its empty input box until then.
func ClaudeQueued(capture string) bool {
	plain := plainScreen(capture)
	top, _, ok := ClaudeInput(plain)
	return ok && strings.Contains(plain[top+1], "edit queued messages")
}

// ClaudeBox is the text in Claude's input box (its rows between the rules, ❯ dropped, placeholder text
// included); ok=false without the box.
func ClaudeBox(capture string) (text string, ok bool) {
	plain := plainScreen(capture)
	top, bottom, ok := ClaudeInput(plain)
	if !ok {
		return "", false
	}
	rows := slices.Clone(plain[top+1 : bottom])
	rows[0] = strings.TrimSpace(strings.TrimPrefix(rows[0], "❯"))
	return strings.Join(rows, "\n"), true
}

// ClaudeCacheDialog reports the dialog /effort and /model open mid-conversation ("Change effort level?",
// "Switch model?" under a ▔ rule), as the switch re-reads the conversation without its prompt cache; its
// option 1 switches.
func ClaudeCacheDialog(capture string) bool {
	plain := plainScreen(capture)
	if _, _, ok := ClaudeInput(plain); ok {
		return false
	}
	i := len(plain) - 1
	for i >= 0 && !strings.HasPrefix(plain[i], strings.Repeat("▔", MinRule)) {
		i--
	}
	if i < 0 {
		return false
	}
	dialog := plain[i+1:]
	text := strings.Join(strings.Fields(strings.Join(dialog, " ")), " ")
	return strings.Contains(text, "This conversation is cached") &&
		slices.ContainsFunc(dialog, func(l string) bool {
			return strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(l, "❯")), "1. Yes")
		})
}

var (
	// spinner is Claude's working line ("✻ Brewing… (5m 6s · ↓ 2.8k tokens)") and the tip under it: its
	// glyph, verb, counters and tip change on their own.
	spinner = regexp.MustCompile(`^[·✢✳✶✻✽*]\s+[^…]+…|^⎿\s+Tip:`)
	// ticking is what changes on its own in other lines: elapsed times ("59s", then "1m 0s"), clocks (not a
	// file.go:12:34 position), token counts, costs, and a braille spinner's frame (Grok's).
	ticking = regexp.MustCompile(`^[\x{2800}-\x{28FF}]|\b\d+(\.\d+)?(ms|[hms])(\s+\d+(\.\d+)?(ms|[hms]))*\b|(^|[\s(])\d{1,2}:\d\d(:\d\d)?(\s?[AP]M)?\b|[↑↓]\s*[\d.,]+k?\s+tokens|[⇡⇣][\d.,]+k?|\$\d+(\.\d+)?`)
)

// Snapshot is a captured screen without what changes while nothing happens: Claude's input box with the
// status lines under it, its spinner line, and elapsed times, clocks, token counts and costs. Two
// snapshots differ when the agent's output moves on (a resized pane rewraps it too).
func Snapshot(capture string) string {
	plain := plainScreen(capture)
	if top, _, ok := ClaudeInput(plain); ok {
		plain = plain[:top]
	}
	var b strings.Builder
	for _, l := range plain {
		if !spinner.MatchString(l) {
			b.WriteString(ticking.ReplaceAllString(l, "#") + "\n")
		}
	}
	return b.String()
}

// ClaudeDialog is the dialog Claude draws in its input box's place: the Plain lines under its top ─ rule,
// the last rule not followed by a numbered option, maybe under the cursor (one above "Chat about this" is
// inside a question). ""
// without one, or with Claude's input box back. Unlike the whole screen it leaves out the conversation,
// which shows earlier tool calls.
func ClaudeDialog(capture string) string {
	plain := plainScreen(capture)
	if _, _, ok := ClaudeInput(plain); ok {
		return ""
	}
	for i := len(plain) - 1; i >= 0; i-- {
		if i = lastRule(plain, i, i+1); i < 0 {
			break
		}
		if i+1 < len(plain) && !startsNumbered(strings.TrimSpace(strings.TrimPrefix(plain[i+1], "❯"))) {
			return strings.Join(plain[i+1:], "\n")
		}
	}
	return ""
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
