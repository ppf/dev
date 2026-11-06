//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The previews stay collapsed until the first worker, then fit to 40% of the window and show a card per worker:
// an idle one folded to its row, f opens it on its screen's end, f again folds it; closing the last collapses them.
func TestHerdCards(t *testing.T) {
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
	collapsed := func(pane string) func() bool {
		return func() bool { return d.T("display", "-p", "-t", pane, "#{pane_width}") == "1" }
	}
	waitFor(t, "previews collapsed without workers", collapsed(previews))
	_, errOut, status := devAgent(append(env, "TMUX_PANE="+coordinator), "", "start", "app", "--timeout", "30s")
	check(t, "start from the coordinator", status+errOut, "0")
	_, errOut, status = devAgent(env, "", "prompt", "app", "--wait", "--timeout", "30s", "hello")
	check(t, "prompt", status+errOut, "0")
	worker := d.T("display", "-t", "=app:", "-p", "#{pane_id}")
	note := func() string { return d.T("show-option", "-pqv", "-t", worker, "@dev_boss_note") }
	waitFor(t, "worker's note on its pane", func() bool { return note() == "finished" })
	waitPane(t, d, previews, "app's card folded", func(screen string) bool {
		return strings.Contains(screen, "IDLE 1") && strings.Contains(screen, "▸ ● app  finished") && !strings.Contains(screen, "got: hello")
	})
	check(t, "previews at 40%", d.T("display", "-p", "-t", previews, "#{pane_width}"), "80")

	d.T("send-keys", "-t", previews, "f")
	waitPane(t, d, previews, "app's card open on its screen", func(screen string) bool {
		return strings.Contains(screen, "▾ ● app") && strings.Contains(screen, "┄ screen") && strings.Contains(screen, "got: hello")
	})
	d.T("send-keys", "-t", previews, "f")
	waitPane(t, d, previews, "app's card folded again", func(screen string) bool {
		return strings.Contains(screen, "▸ ● app") && !strings.Contains(screen, "got: hello")
	})

	// A new prompt clears the last turn's note; with the inbox off the next one is still recorded
	_, errOut, status = devAgent(env, "", "prompt", "app", "slow")
	check(t, "slow prompt", status+errOut, "0")
	waitFor(t, "note cleared by the prompt", func() bool { return note() == "" })
	waitFor(t, "slow turn's note", func() bool { return note() == "finished" })
	d.T("set-option", "-t", "=boss:", "@dev_boss_inbox", "off")
	_, errOut, status = devAgent(env, "", "prompt", "app", "ask")
	check(t, "ask prompt", status+errOut, "0")
	waitFor(t, "decision note with the inbox off", func() bool { return note() == "needs a decision" })
	d.T("kill-session", "-t", "=app")
	waitFor(t, "previews collapsed after the last worker", collapsed(previews))

	// A previews pane started at another width (an older dev's 55%) on a boss with a worker fits to 40%; once the
	// worker goes it collapses, handing the focus split-window gave it to the pane on its left: a hand-made boss
	// (no pane-died binding)
	d.T("new-session", "-d", "-s", "boss2", "-x", "200", "-y", "50", "sleep 600")
	d.T("set-option", "-t", "=boss2:", "@dev_herd", "1")
	_, errOut, status = devAgent(append(env, "TMUX_PANE="+d.T("display", "-p", "-t", "=boss2:", "#{pane_id}")), "", "start", "app", "--timeout", "30s")
	check(t, "start from boss2", status+errOut, "0")
	again := d.T("split-window", "-t", "=boss2:", "-h", "-l", "55%", "-PF", "#{pane_id}", devBin, "boss", "--previews")
	waitFor(t, "reopened previews fit to 40%", func() bool { return d.T("display", "-p", "-t", again, "#{pane_width}") == "80" })
	d.T("kill-session", "-t", "=app")
	waitFor(t, "reopened previews collapsed", collapsed(again))
	check(t, "focus handed left", d.T("display", "-p", "-t", "=boss2:", "#{pane_active}#{pane_index}"), "11")
}
