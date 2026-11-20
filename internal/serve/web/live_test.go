package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// node is the node binary; without one the test skips, but not in CI, where it must run.
func node(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node not found: CI runs the page's JS tests")
		}
		t.Skip("node not found")
	}
	return bin
}

// The page's scripts parse.
func TestScriptsParse(t *testing.T) {
	bin := node(t)
	for _, f := range []string{"static/live.js", "static/model.js", "static/app.js"} {
		if out, err := exec.Command(bin, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
}

// The page on a stub DOM: stream, reads per event and ping, a mid-read change, version reload, 401 to Pair and back, a long text's sheet.
func TestPageLive(t *testing.T) {
	if out, err := exec.Command(node(t), "testdata/page.js", "static").CombinedOutput(); err != nil || len(out) > 0 {
		t.Errorf("%v\n%s", err, out)
	}
}

// A new version reloads the page once; typed words or a pick, or a reload of its own a minute ago, offer it instead.
func TestVersionStep(t *testing.T) {
	const cases = `
const now = 1e9, gap = RELOAD_GAP_MS;
const got = [
  ['first board', versionStep('', 'v2', false, 0, now), 'keep'],
  ['same version', versionStep('v1', 'v1', true, 0, now), 'keep'],
  ['new version', versionStep('v1', 'v2', false, 0, now), 'reload'],
  ['busy', versionStep('v1', 'v2', true, 0, now), 'offer'],
  ['reloaded just now', versionStep('v1', 'v2', false, now - 1000, now), 'offer'],
  ['reloaded a while ago', versionStep('v1', 'v2', false, now - gap - 1, now), 'reload'],
];
for (const [name, step, want] of got) if (step !== want) console.log(name + ': ' + step + ', want ' + want);
`
	src, err := static.ReadFile("static/live.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node(t), "-e", string(src)+cases).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("%v\n%s", err, out)
	}
}

// A sheet's title: its first line, a short text whole, else the text to a sentence or clause end 20 to 120 characters in.
func TestLead(t *testing.T) {
	const cases = `
const long = s => s + ' ' + 'and more words after it '.repeat(6).trim();
const got = [
  ['short', 'Read the plan', ['Read the plan', '']],
  ['first line', 'Ship it?\nThe notes are in notes/release.md.', ['Ship it?', 'The notes are in notes/release.md.']],
  ['a blank line after the first', 'Ship it?\r\n\r\nThe notes.\n', ['Ship it?', 'The notes.']],
  ['a first line too long', 'a'.repeat(121) + '\nnext. more', ['', 'a'.repeat(121) + '\nnext. more']],
  ['120 characters whole', 'a'.repeat(120), ['a'.repeat(120), '']],
  ['a question first', long('Ship the sheet fix tonight?  Then'), ['Ship the sheet fix tonight?', long('Then')]],
  ['first sentence', long('Fix the sheet so it reads well. Then'), ['Fix the sheet so it reads well.', long('Then')]],
  ['dots inside words and dates', long('Moduł faktur v0.71.0 z 19.11 (wersja B2). 19.11 16:10'), ['Moduł faktur v0.71.0 z 19.11 (wersja B2).', long('19.11 16:10')]],
  ['a sentence ending in a bracket', long('Ask the accountant (the October one.) Then'), ['Ask the accountant (the October one.)', long('Then')]],
  ['too short a sentence: a clause', long('Ok. Restart the board and the manager, then test v0.71.0: live'), ['Ok. Restart the board and the manager, then test v0.71.0:', long('live')]],
  ['an abbreviation is no sentence end', long('Use the new flag, e.g. the one we added: then'), ['Use the new flag, e.g. the one we added:', long('then')]],
  ['no cut inside brackets or quotes', long('Do pt 27.11 (termin na piśmie: mail) — usuń mail „FW: Faktura” z skrzynki; potem'), ['Do pt 27.11 (termin na piśmie: mail) — usuń mail „FW: Faktura” z skrzynki;', long('potem')]],
  ['nor inside straight quotes', long('Forward the message titled "Release notes: Final draft" to the team; then'), ['Forward the message titled "Release notes: Final draft" to the team;', long('then')]],
  ['e.g. before a capital', long('Run the smoke tests on a supported browser, e.g. Safari on an iPhone. Then'), ['Run the smoke tests on a supported browser, e.g. Safari on an iPhone.', long('Then')]],
  ['a 120-character first line before CRLF', 'a'.repeat(120) + '\r\nnext', ['a'.repeat(120), 'next']],
  ['blank lines first', '\n\nShip it?\nNotes', ['Ship it?', 'Notes']],
  ['121 characters, no break', 'a'.repeat(121), ['', 'a'.repeat(121)]],
  ['no break within the limit', 'word '.repeat(40).trim(), ['', 'word '.repeat(40).trim()]],
  ['first sentence too long', 'word '.repeat(30) + 'end. ' + long('Then'), ['', 'word '.repeat(30) + 'end. ' + long('Then')]],
];
for (const [name, text, want] of got) {
  const [title, rest] = lead(text);
  if (title !== want[0] || rest !== want[1]) console.log(name + ': ' + JSON.stringify([title, rest]) + ', want ' + JSON.stringify(want));
}
`
	src, err := static.ReadFile("static/live.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node(t), "-e", string(src)+cases).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("%v\n%s", err, out)
	}
}
