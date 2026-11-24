package screen

import (
	"slices"
	"strings"
)

// agyBelowBox is how many lines agy draws under its input box: its footer ("? for shortcuts", mode, model, effort).
const agyBelowBox = 1

// AgyInput finds agy's input box at the bottom of a screen's Plain lines: a > prompt row between two ─ rules,
// its footer under it; top and bottom are its rules. ok=false without it, or while its footer says "esc to
// cancel": working, or a dialog drawn in its place.
func AgyInput(plain []string) (top, bottom int, ok bool) {
	top, bottom, ok = AgyBoxAt(plain)
	if !ok || slices.ContainsFunc(plain[bottom+1:], func(l string) bool { return strings.HasPrefix(l, "esc to cancel") }) {
		return 0, 0, false
	}
	return top, bottom, true
}

// AgyBoxAt is AgyInput whatever its footer says: also while agy works.
func AgyBoxAt(plain []string) (top, bottom int, ok bool) {
	bottom = lastRule(plain, len(plain)-1, agyBelowBox+1, isRule)
	if bottom < 0 {
		return 0, 0, false
	}
	// The rule over its > row: a draft may hold a rule of its own
	for top = bottom - 2; top >= 0 && top > bottom-MaxBoxLines-2; top-- {
		if isRule(plain[top]) && strings.HasPrefix(plain[top+1], ">") {
			return top, bottom, true
		}
	}
	return 0, 0, false
}

// AgyDialog reports a dialog agy draws in its input box's place: its footer "esc to cancel" with no box rule right
// above it (working, the rule sits there). A draft of any height doesn't count.
func AgyDialog(capture string) bool {
	plain := plainScreen(capture)
	n := len(plain)
	return n > 1 && strings.HasPrefix(plain[n-1], "esc to cancel") && !isRule(plain[n-2])
}

// AgyBox reports agy's input box at the bottom of its captured screen, idle or working: no dialog in its place.
func AgyBox(capture string) bool {
	_, _, ok := AgyBoxAt(plainScreen(capture))
	return ok
}

// AgyIdle reports agy's input box at the bottom of its captured screen, idle: not working, no dialog open.
func AgyIdle(capture string) bool {
	_, _, ok := AgyInput(plainScreen(capture))
	return ok
}

// AgyInterrupted reports agy's screen after a turn ended without a Stop hook (a declined permission prompt, Esc):
// its input box right under "⎿  Interrupted · What should Antigravity CLI do instead?". A prompt sent since
// sits between them instead.
func AgyInterrupted(capture string) bool {
	plain := plainScreen(capture)
	top, _, ok := AgyInput(plain)
	if !ok {
		return false
	}
	for i := top - 1; i >= 0; i-- {
		if l := plain[i]; l != "" {
			return strings.HasPrefix(l, "⎿") && strings.Contains(l, "Interrupted")
		}
	}
	return false
}

// AgyTrust reads agy's trust dialog: the folder it asks about (the path under "Accessing workspace:", maybe
// wrapped over several lines) and whether its cursor is on "Yes, I trust this folder", which Enter confirms.
// ok=false without the dialog.
func AgyTrust(capture string) (dir string, yes, ok bool) {
	plain := plainScreen(capture)
	at := slices.Index(plain, "Accessing workspace:")
	if at < 0 {
		return "", false, false
	}
	ask := slices.Index(plain[at:], "Do you trust the contents of this project?")
	if ask < 0 {
		return "", false, false
	}
	ask += at
	return strings.Join(plain[at+1:ask], ""), slices.Contains(plain[ask:], "> Yes, I trust this folder"), true
}

// agyDialogLines bounds how far above its footer agy's permission prompt may start.
const agyDialogLines = 40

// AgyPermission reads agy's permission prompt at the bottom of its captured screen: the tool it's for (the "● Bash("
// line above it), what it asks to run (the lines under "Requesting permission for:", wrapped lines joined) and its
// question ("Run this command?"), over numbered options, its footer "esc to cancel" in the input box's place.
// ok=false without it: agy fires no hook for it, so only its screen tells it from an approved call.
func AgyPermission(capture string) (tool, what, title string, ok bool) {
	plain := plainScreen(capture)
	if len(plain) == 0 || !strings.HasPrefix(plain[len(plain)-1], "esc to cancel") || AgyBox(capture) {
		return "", "", "", false
	}
	at := -1
	for i := len(plain) - 1; i >= 0 && i >= len(plain)-agyDialogLines; i-- {
		if plain[i] == "Requesting permission for:" {
			at = i
			break
		}
	}
	if at < 0 {
		return "", "", "", false
	}
	i := at + 1
	var asked []string
	for ; i < len(plain) && plain[i] != ""; i++ {
		asked = append(asked, plain[i])
	}
	for i < len(plain) && plain[i] == "" {
		i++
	}
	if i+1 >= len(plain) || !strings.HasSuffix(plain[i], "?") || !startsNumbered(strings.TrimPrefix(plain[i+1], "> ")) {
		return "", "", "", false
	}
	// Up to the prompt it answers: agy may leave many blank lines between
	for k := at - 1; k >= 0 && k >= at-agyDialogLines && !strings.HasPrefix(plain[k], ">"); k-- {
		if name, ok := strings.CutPrefix(plain[k], "● "); ok {
			tool, _, _ = strings.Cut(name, "(")
			break
		}
	}
	return tool, strings.Join(asked, " "), plain[i], len(asked) > 0
}
