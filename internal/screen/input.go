package screen

import (
	"regexp"
	"strings"
)

// Input is what Claude's input box holds: Text, and Suggestion when all of it is Claude Code's own dim text (a prompt
// suggestion or a placeholder, never the user's typing).
type Input struct {
	Text       string `json:"text"`
	Suggestion bool   `json:"suggestion"`
}

// InputBox reads Claude's input box in capture (capture-pane -p -e): the last prompt row between two horizontal
// rules and any draft lines under it. Typed text (outside dim runs) is the Text; a box holding only dim text has that
// as Text, a Suggestion. ok=false when it finds no box.
func InputBox(capture string) (in Input, ok bool) {
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	for i := len(lines) - 1; i > 0; i-- {
		if !edge(lines[i]) {
			continue
		}
		// lines[i] is the box's bottom rule: find its top rule
		for j := i - 1; j >= 0; j-- {
			if !edge(lines[j]) && !IsTopRule(strings.TrimSpace(plainLine(lines[j]))) {
				continue
			}
			if j+1 >= i {
				return Input{}, false
			}
			typed := promptRow.FindStringSubmatch(undimmed(lines[j+1]))
			all := promptRow.FindStringSubmatch(plainLine(lines[j+1]))
			if typed == nil || all == nil {
				return Input{}, false
			}
			text := []string{strings.TrimSpace(typed[1])}
			for _, l := range lines[j+2 : i] {
				text = append(text, strings.TrimRight(plainLine(l), " "))
			}
			if t := strings.TrimSpace(strings.Join(text, "\n")); t != "" {
				return Input{Text: t}, true
			}
			if dim := strings.TrimSpace(all[1]); dim != "" {
				return Input{Text: dim, Suggestion: true}, true
			}
			return Input{}, true
		}
		return Input{}, false
	}
	return Input{}, false
}

// PromptRow is the text after a plain line's Claude input prompt (promptRow); ok=false for no prompt row.
func PromptRow(line string) (text string, ok bool) {
	m := promptRow.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// promptRow is Claude's input row: "❯" (or ">" in older versions) and a space or no-break space.
var promptRow = regexp.MustCompile(`^[│|]?\s*[❯>][ \x{00a0}]?(.*)$`)

// sgr is one ANSI SGR escape.
var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// edge: a line that is only box-drawing horizontals (Claude's input box edges), at least MinRule wide.
func edge(line string) bool {
	p := strings.TrimSpace(plainLine(line))
	return len([]rune(p)) >= MinRule && strings.Trim(p, "─━╌╍┄┅╭╮╰╯") == ""
}

// plainLine drops all escape sequences.
func plainLine(line string) string {
	return ansiAll.ReplaceAllString(line, "")
}

var ansiAll = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\))`)

// undimmed is line's text outside dim (SGR 2) runs, without escapes.
func undimmed(line string) string {
	line = ansiAll.ReplaceAllStringFunc(line, func(e string) string {
		if sgr.MatchString(e) {
			return e
		}
		return ""
	})
	var b strings.Builder
	dim := false
	for len(line) > 0 {
		loc := sgr.FindStringSubmatchIndex(line)
		if loc == nil {
			if !dim {
				b.WriteString(line)
			}
			break
		}
		if !dim {
			b.WriteString(line[:loc[0]])
		}
		params := strings.Split(line[loc[2]:loc[3]], ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "2":
				dim = true
			case "", "0", "22":
				dim = false
			case "38", "48", "58":
				// Extended colours: their operands (5;n or 2;r;g;b) aren't attributes
				if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				} else if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				}
			}
		}
		line = line[loc[1]:]
	}
	return b.String()
}
