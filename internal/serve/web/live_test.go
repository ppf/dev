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
	for _, f := range []string{"static/live.js", "static/app.js"} {
		if out, err := exec.Command(bin, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
}

// The page on a stub DOM: stream, reads per event and ping, a mid-read change, version reload, 401 to Pair and back.
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
