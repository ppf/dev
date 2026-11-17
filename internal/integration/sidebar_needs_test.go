//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A boss asks a decision: the sidebar shows it under NEEDS YOU, n goes in, ⏎ shows the options and 1 answers it as dev todo
// answer does: the item ticks and the boss's inbox gets "you answered". The server carries CLAUDECODE, as one started from a
// Claude session does: the sidebar still answers, as the user.
func TestSidebarNeedsYouAnswer(t *testing.T) {
	d := actServer(t)
	script := filepath.Join(d.tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(d.tmp, "boss-a.log")
	d.T("new-session", "-d", "-x", "200", "-y", "50", "-s", "boss-a", "bash", script, "", log)
	d.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	d.T("set-environment", "-g", "CLAUDECODE", "1")
	pane := d.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	cmd := exec.Command(devBin, "state", "idle")
	cmd.Env = d.scriptEnv("TMUX_PANE=" + pane)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("❌ register boss-a: %v %s", err, out)
	}
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", pane), "Try") })
	dev := func(argv ...string) string {
		t.Helper()
		c := exec.Command(devBin, argv...)
		c.Env = d.scriptEnv("TMUX_PANE="+pane, "CLAUDECODE=")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(argv, " "), err, out)
		}
		return string(out)
	}
	dev("todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait")

	// A stale board's answer names the question it showed: another one is refused and the item stays open
	stale := exec.Command(devBin, "todo", "answer", "--question", "Release now?", "boss-a", "1", "Merge")
	stale.Env = d.scriptEnv("TMUX_PANE=", "CLAUDECODE=")
	if out, err := stale.CombinedOutput(); err == nil || !strings.Contains(string(out), "look at the list again") {
		t.Fatalf("❌ answer for another question: %v %s, want refused", err, out)
	}

	sidebar := actEnsure(d, "boss-a")
	waitScreen(t, d.server, sidebar, "NEEDS YOU 1")
	d.T("select-pane", "-t", sidebar)
	waitScreen(t, d.server, sidebar, "NEEDS YOU 1 · n")
	d.T("send-keys", "-t", sidebar, "n")
	waitScreen(t, d.server, sidebar, "⏎ options")
	d.T("send-keys", "-t", sidebar, "Enter")
	waitScreen(t, d.server, sidebar, "1 ★ Merge")
	d.T("send-keys", "-t", sidebar, "1")
	waitFor(t, "boss-a gets the answer", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "- you answered #1 \"Merge #148?\": Merge\n")
	})
	check(t, "ticked with its answer", dev("todo"), "1\t☑\tMerge #148?\tanswered: Merge\n")
	waitFor(t, "NEEDS YOU gone", func() bool { return !strings.Contains(d.T("capture-pane", "-p", "-t", sidebar), "NEEDS YOU") })
}
