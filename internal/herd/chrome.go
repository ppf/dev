package herd

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	// maxBelowBox is how many status lines an agent draws under its input box.
	maxBelowBox = 6
	// maxBoxLines bounds a typed draft's lines.
	maxBoxLines = 20
	// minRule is the shortest ─ run taken for the box's border.
	minRule = 10
	// maxBanner bounds Grok's opt-in banner, wrapped in a narrow pane.
	maxBanner = 10
)

// previewLines is t's captured screen as its tile shows it: without the agent's own input box, unless it
// waits on a decision, as its question or dialog is what the user answers.
func previewLines(capture string, t tile) []string {
	lines := screenLines(capture)
	if t.deciding {
		return lines
	}
	switch t.sess.Agent {
	case "Claude":
		return cropInput(lines)
	case "Codex":
		return squeezeBlank(cropCodexInput(lines))
	case "Grok":
		return squeezeBlank(cropGrokInput(lines))
	}
	return lines
}

// squeezeBlank drops leading blank lines and collapses each run of blank lines to one: Codex and Grok
// leave long blank gaps on screen, as between a short conversation and the spinner at the bottom.
func squeezeBlank(lines []string) []string {
	var out []string
	blank := true
	for _, l := range lines {
		b := strings.TrimSpace(ansi.Strip(l)) == ""
		if !b || !blank {
			out = append(out, l)
		}
		blank = b
	}
	return out
}

// cropInput drops Claude Code's own input box from the bottom of its captured screen: a ❯ prompt row
// between two full-width ─ rules, and the status lines under it. It looks like an input in a tile but
// takes nothing typed there. A screen without that box, or with a numbered option or dialog text between
// the rules (a question waiting for an answer), is returned unchanged.
func cropInput(lines []string) []string {
	plain := plainLines(lines)
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
		if asksChoice(strings.TrimPrefix(l, "❯")) {
			return lines
		}
	}
	return cutAt(lines, plain, top)
}

// cropCodexInput drops Codex's input from the bottom of its captured screen: a › prompt row starting the
// line, with only indented draft and status lines under it. A sent prompt is echoed as a › row too, but its
// reply under it starts the line. A › menu (its trust dialog) is returned unchanged.
func cropCodexInput(lines []string) []string {
	plain := plainLines(lines)
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-maxBoxLines-maxBelowBox; i-- {
		raw := ansi.Strip(lines[i])
		if strings.HasPrefix(raw, "›") {
			for _, l := range plain[i:] {
				if asksChoice(strings.TrimPrefix(l, "›")) {
					return lines
				}
			}
			return cutAt(lines, plain, i)
		}
		if plain[i] != "" && !strings.HasPrefix(raw, " ") {
			return lines
		}
	}
	return lines
}

// cropGrokInput drops Grok's input box from the bottom of its captured screen: a rounded box (╭…╮, │ ❯ …│,
// ╰… model ─╯), the status lines below it and the opt-in banner above it. A box holding a numbered option or
// dialog text is returned unchanged.
func cropGrokInput(lines []string) []string {
	plain := plainLines(lines)
	bottom := -1
	for i := len(plain) - 1; i >= 0 && i >= len(plain)-maxBelowBox-1; i-- {
		if strings.HasPrefix(plain[i], "╰") && strings.HasSuffix(plain[i], "╯") {
			bottom = i
			break
		}
	}
	if bottom < 0 {
		return lines
	}
	top := bottom - 1
	for top >= 0 && top > bottom-maxBoxLines-1 && strings.HasPrefix(plain[top], "│") {
		top--
	}
	if top < 0 || top == bottom-1 || !strings.HasPrefix(plain[top], "╭") || !strings.HasSuffix(plain[top], "╮") {
		return lines
	}
	if !strings.HasPrefix(boxText(plain[top+1]), "❯") {
		return lines
	}
	for _, l := range plain[top+1 : bottom] {
		if asksChoice(strings.TrimPrefix(boxText(l), "❯")) {
			return lines
		}
	}
	end := top
	for end > 0 && plain[end-1] == "" {
		end--
	}
	if end > 0 && plain[end-1] == "Read Terms and Privacy Policy." {
		for i := end - 1; i >= 0 && i >= end-maxBanner; i-- {
			if strings.HasPrefix(plain[i], "Help improve Grok") {
				return cutAt(lines, plain, i)
			}
		}
	}
	return cutAt(lines, plain, top)
}

// boxText is a │…│ box row's text without its borders.
func boxText(l string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(l, "│"), "│"))
}

// plainLines is lines without colour or surrounding spaces.
func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return plain
}

// cutAt is lines above the input starting at top, without the blank lines just above it.
func cutAt(lines, plain []string, top int) []string {
	for top > 0 && plain[top-1] == "" {
		top--
	}
	return lines[:top]
}

// asksChoice reports an input line that is a numbered option or dialog text, not a draft.
func asksChoice(l string) bool {
	l = strings.TrimSpace(l)
	return startsNumbered(l) || strings.Contains(l, "Do you want")
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
