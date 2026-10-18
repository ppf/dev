package herd

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/ppf/dev/internal/screen"
)

// maxDialogTail is how close to the screen's bottom a dialog's last option must be (it, then hints or a
// rule); numbered lines further up are conversation, not a dialog.
const maxDialogTail = 6

// dialog is a numbered choice an agent shows at the bottom of its screen: Claude's questions and permission
// prompts, Codex's and Grok's menus.
type dialog struct {
	title string // the text just above the options, wrapped lines joined
	// what is the text above the title up to the dialog's top rule (a permission's command or file), spaces
	// dropped: two prompts alike but for it are two dialogs
	what   string
	tabs   []string // Claude's question tabs ("Color", "Extras", "Submit"); nil for one plain question
	opts   []option
	cursor int  // the row the agent's cursor is on, see row; -1 unknown
	multi  bool // checkbox options
	// submitAt is where Claude's multi-select Submit row is among the rows its cursor moves over: the number
	// of options above it; 0 when none
	submitAt int
}

type option struct {
	num      int
	label    string
	desc     string // the indented lines under it: a description, or the label's wrapped rest
	checkbox bool   // a multi-select option; its question's other options (Chat about this) answer at once
	checked  bool
}

// sig identifies the question on screen: its title and labels, not where the cursor is or what's checked.
func (d dialog) sig() string {
	parts := []string{d.what, d.title}
	for _, o := range d.opts {
		parts = append(parts, o.label)
	}
	return strings.Join(parts, "\x00")
}

// row is option k's place among the rows the cursor moves over: options under the Submit row come one later.
func (d dialog) row(k int) int {
	if d.submitAt > 0 && k >= d.submitAt {
		return k + 1
	}
	return k
}

// option is the option on cursor row r; -1 for the Submit row or none.
func (d dialog) option(r int) int {
	switch {
	case d.submitAt > 0 && r == d.submitAt, r < 0:
		return -1
	case d.submitAt > 0 && r > d.submitAt:
		return r - 1
	}
	return r
}

// optionLine is a numbered option, maybe under the cursor (❯ Claude and Grok, › Codex).
var optionLine = regexp.MustCompile(`^(\s*)(?:([❯›])\s*)?(\d{1,2})\.\s(.*)$`)

// parseDialog finds the dialog at the bottom of a captured screen; ok=false when there is none.
func parseDialog(capture string) (dialog, bool) {
	lines := screenLines(capture)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = unbox(strings.TrimRight(ansi.Strip(l), " "))
	}
	last := -1
	for i := len(plain) - 1; i >= 0 && i >= len(plain)-maxDialogTail; i-- {
		if optionLine.MatchString(plain[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return dialog{}, false
	}
	col := digitCol(plain[last])
	// Up from the last option over options, their indented lines, the Submit row and rules between them
	first := last
	for i := last - 1; i >= 0; i-- {
		l := plain[i]
		switch {
		case optionLine.MatchString(l) && digitCol(l) == col, indent(l) > col && strings.TrimSpace(l) != "", submitRow(l):
			first = i
			continue
		case isRule(l) && i > 0 && (optionLine.MatchString(plain[i-1]) || indent(plain[i-1]) > col):
			continue
		}
		break
	}
	d := dialog{cursor: -1}
	for _, l := range plain[first : last+1] {
		if submitRow(l) {
			d.submitAt = len(d.opts)
			if strings.HasPrefix(strings.TrimSpace(l), "❯") {
				d.cursor = d.submitAt
			}
			continue
		}
		m := optionLine.FindStringSubmatch(l)
		if m == nil || digitCol(l) != col {
			if len(d.opts) > 0 && !isRule(l) {
				o := &d.opts[len(d.opts)-1]
				o.desc = strings.TrimSpace(o.desc + " " + strings.TrimSpace(l))
			}
			continue
		}
		n, _ := strconv.Atoi(m[3])
		if n != len(d.opts)+1 {
			return dialog{}, false
		}
		o := option{num: n, label: m[4]}
		for _, box := range []string{"[ ] ", "[✔] ", "[x] "} {
			if rest, ok := strings.CutPrefix(o.label, box); ok {
				o.label, o.checkbox, o.checked, d.multi = rest, true, box != "[ ] ", true
			}
		}
		o.label = strings.TrimSpace(o.label)
		if m[2] != "" {
			d.cursor = d.row(len(d.opts))
		}
		d.opts = append(d.opts, o)
	}
	if len(d.opts) == 0 {
		return dialog{}, false
	}
	// The title: the paragraph above the options, then the tab row above it
	i := first - 1
	for i >= 0 && strings.TrimSpace(plain[i]) == "" {
		i--
	}
	var title []string
	for ; i >= 0 && strings.TrimSpace(plain[i]) != "" && !isRule(plain[i]) && !tabRow(plain[i]); i-- {
		title = append([]string{strings.TrimSpace(plain[i])}, title...)
	}
	d.title = strings.Join(title, " ")
	for i >= 0 && strings.TrimSpace(plain[i]) == "" {
		i--
	}
	if i >= 0 && tabRow(plain[i]) {
		d.tabs = parseTabs(plain[i])
		return d, true
	}
	var what []string
	for ; i >= 0 && i > first-maxWhat && !isTopRule(plain[i]); i-- {
		if !isRule(plain[i]) {
			what = append([]string{squeeze(plain[i])}, what...)
		}
	}
	d.what = strings.Join(what, "")
	return d, true
}

// maxWhat bounds the lines above a dialog's options taken for what it asks.
const maxWhat = 30

// isTopRule is a full ─ rule, as tops Claude's dialogs; its ╌ rules frame a command inside one.
func isTopRule(l string) bool {
	t := strings.TrimSpace(l)
	return len([]rune(t)) >= screen.MinRule && strings.Trim(t, "─") == ""
}

// unbox is a │…│ box row's inside (Grok's dialogs), keeping its indentation.
func unbox(l string) string {
	t := strings.TrimSpace(l)
	if !strings.HasPrefix(t, "│") || !strings.HasSuffix(t, "│") || t == "│" {
		return l
	}
	return strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│"), " ")
}

// digitCol is the column of an option line's number.
func digitCol(l string) int {
	for i, r := range []rune(l) {
		if r >= '0' && r <= '9' {
			return i
		}
	}
	return -1
}

func indent(l string) int {
	return len([]rune(l)) - len([]rune(strings.TrimLeft(l, " ")))
}

// isRule is a line of ─ or ╌ only, or a box's top or bottom edge.
func isRule(l string) bool {
	t := strings.TrimSpace(l)
	return t != "" && strings.Trim(t, "─╌╭╮╰╯") == ""
}

// submitRow is the Submit row under Claude's multi-select options.
func submitRow(l string) bool {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "❯")) == "Submit"
}

// tabRow is Claude's row of question tabs: "←  ☐ Color  ☒ Extras  ✔ Submit  →", or " ☐ Size" alone.
func tabRow(l string) bool {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "←"))
	return strings.HasPrefix(t, "☐ ") || strings.HasPrefix(t, "☒ ")
}

// parseTabs is a tab row's names, the box marks dropped.
func parseTabs(l string) []string {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l), "←"), "→"))
	var tabs []string
	for _, f := range strings.Split(t, "  ") {
		f = strings.TrimSpace(f)
		for _, mark := range []string{"☐ ", "☒ ", "✔ "} {
			f = strings.TrimPrefix(f, mark)
		}
		if f != "" {
			tabs = append(tabs, f)
		}
	}
	return tabs
}
