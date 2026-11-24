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
	top, bottom, ok = agyBox(plain)
	if !ok || slices.ContainsFunc(plain[bottom+1:], func(l string) bool { return strings.HasPrefix(l, "esc to cancel") }) {
		return 0, 0, false
	}
	return top, bottom, true
}

// agyBox is AgyInput whatever its footer says: also while agy works.
func agyBox(plain []string) (top, bottom int, ok bool) {
	bottom = lastRule(plain, len(plain)-1, agyBelowBox+1, isRule)
	if bottom < 0 {
		return 0, 0, false
	}
	top = lastRule(plain, bottom-1, MaxBoxLines+1, isRule)
	if top < 0 || top == bottom-1 || !strings.HasPrefix(plain[top+1], ">") {
		return 0, 0, false
	}
	return top, bottom, true
}

// AgyBox reports agy's input box at the bottom of its captured screen, idle or working: no dialog in its place.
func AgyBox(capture string) bool {
	_, _, ok := agyBox(plainScreen(capture))
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
