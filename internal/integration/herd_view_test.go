//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// v in the previews switches to the compact list (each worker's header and latest note, no screen), records
// it on the boss session and narrows the pane to 30% of the window; v again restores the tiles at 55%. A
// previews pane started in a boss session keeping the compact view starts compact and narrows itself.
func TestHerdCompactView(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	env := d.scriptEnv(writeProjects(t, [2]string{"app", tempDir(t)}))
	boss := exec.Command(devBin, "boss")
	boss.Env = env
	if out, err := boss.CombinedOutput(); err != nil {
		t.Fatalf("dev boss: %v: %s", err, out)
	}
	panes := strings.Fields(d.T("list-panes", "-t", "=boss:1", "-F", "#{pane_id}"))
	coordinator, previews := panes[1], panes[2]
	_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", "app", "--timeout", "30s")
	check(t, "start from the coordinator", status+errOut, "0")
	_, errOut, status = devAgent(env, "", "prompt", "app", "--wait", "--timeout", "30s", "hello")
	check(t, "prompt", status+errOut, "0")
	worker := d.T("display", "-t", "=app:", "-p", "#{pane_id}")
	note := func() string { return d.T("show-option", "-pqv", "-t", worker, "@dev_boss_note") }
	waitFor(t, "worker's note on its pane", func() bool { return note() == "finished" })
	waitFor(t, "app tile with its screen", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", previews), "got: hello")
	})
	width := func() string { return d.T("display", "-p", "-t", previews, "#{pane_width}") }
	view := func() string { return d.T("show-option", "-qv", "-t", "=boss:", "@dev_boss_view") }
	check(t, "full view at 55%", width(), "110")

	d.T("send-keys", "-t", previews, "v")
	waitFor(t, "compact list", func() bool {
		screen := d.T("capture-pane", "-p", "-t", previews)
		return strings.Contains(screen, "● app Claude · idle") && strings.Contains(screen, "\n   finished") && !strings.Contains(screen, "got: hello")
	})
	waitFor(t, "compact recorded on the boss at 30%", func() bool { return view() == "compact" && width() == "60" })
	if screen := d.T("capture-pane", "-p", "-t", previews); !strings.Contains(screen, "v view") {
		t.Errorf("❌ no v in the key hints:\n%s", screen)
	}

	d.T("send-keys", "-t", previews, "v")
	waitFor(t, "tiles back", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", previews), "got: hello")
	})
	waitFor(t, "full recorded on the boss at 55%", func() bool { return view() == "" && width() == "110" })

	// A new prompt clears the last turn's note; with the inbox off the next one is still recorded
	_, errOut, status = devAgent(env, "", "prompt", "app", "slow")
	check(t, "slow prompt", status+errOut, "0")
	waitFor(t, "note cleared by the prompt", func() bool { return note() == "" })
	waitFor(t, "slow turn's note", func() bool { return note() == "finished" })
	d.T("set-option", "-t", "=boss:", "@dev_boss_inbox", "off")
	_, errOut, status = devAgent(env, "", "prompt", "app", "ask")
	check(t, "ask prompt", status+errOut, "0")
	waitFor(t, "decision note with the inbox off", func() bool { return note() == "needs a decision" })

	// A reopened previews pane keeps its session's view: a hand-made boss (no pane-exited binding)
	d.T("new-session", "-d", "-s", "boss2", "-x", "200", "-y", "50", "sleep 600")
	d.T("set-option", "-t", "=boss2:", "@dev_herd", "1")
	d.T("set-option", "-t", "=boss2:", "@dev_boss_view", "compact")
	again := d.T("split-window", "-t", "=boss2:", "-h", "-l", "55%", "-PF", "#{pane_id}", devBin, "boss", "--previews")
	waitFor(t, "reopened previews narrow to compact", func() bool {
		return d.T("display", "-p", "-t", again, "#{pane_width}") == "60"
	})
}
