//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A boss's cost total (dev agent list --json cost_total_usd, as the sidebar and the board roll it up) keeps what
// ended under it: a worker closed with dev agent close, a worker's /clear (Claude counts the next conversation from
// zero), a conversation resumed later counted once, and under the manager a boss the manager closed, with all it kept.
func TestCostTotalKeepsClosed(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	root := newRepo(t, "", "")
	dev := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = d.scriptEnv(append([]string{"TMUX_PANE=" + pane}, gitEnv...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		return string(out)
	}
	// conversation starts conversation id in pane, its cost usd so far (0 for a new one, its last for a resumed one)
	conversation := func(pane, id string, usd float64) {
		t.Helper()
		dev(pane, fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":%q}`, id), "state", "idle")
		dev(pane, fmt.Sprintf(`{"session_id":%q,"cost":{"total_cost_usd":%v}}`, id, usd), "statusline")
	}
	// session opens name in dir, started by by, a boss when boss, its conversation c-<name> at usd
	session := func(name, dir, by string, boss bool, usd float64) string {
		t.Helper()
		d.T("new-session", "-d", "-s", name, "-c", dir, "sleep 600")
		if by != "" {
			d.T("set-option", "-t", "="+name+":", "@dev_started_by", by)
		}
		if boss {
			d.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		}
		pane := d.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		conversation(pane, "c-"+name, usd)
		return pane
	}
	worker := func(name string, usd float64) string {
		t.Helper()
		wt := filepath.Join(root, ".claude", "worktrees", name)
		git(t, root, "worktree", "add", "-q", "-b", name, wt, "origin/main")
		return session(name, wt, "boss-a", false, usd)
	}
	// totals is each boss's "total closed" as dev agent list --json says it
	totals := func() string {
		t.Helper()
		var agents []struct {
			Session string   `json:"session"`
			Total   *float64 `json:"cost_total_usd"`
			Closed  *float64 `json:"cost_closed_usd"`
		}
		if err := json.Unmarshal([]byte(dev("", "", "agent", "list", "--json")), &agents); err != nil {
			t.Fatal(err)
		}
		var b []string
		for _, a := range agents {
			if a.Total != nil {
				closed := 0.0 // omitted when nothing closed
				if a.Closed != nil {
					closed = *a.Closed
				}
				b = append(b, fmt.Sprintf("%s %.2f %.2f", a.Session, *a.Total, closed))
			}
		}
		return strings.Join(b, ", ")
	}

	manager := session("manager", root, "", true, 1)
	boss := session("boss-a", root, "manager", true, 2)
	worker("w1", 4)
	w2 := worker("w2", 8)
	check(t, "live only", totals(), "boss-a 14.00 0.00, manager 15.00 0.00")

	dev(boss, "", "agent", "close", "w1")
	check(t, "a closed worker's cost stays", totals(), "boss-a 14.00 4.00, manager 15.00 4.00")

	// /clear: SessionEnd, then a new conversation from zero
	dev(w2, `{"hook_event_name":"SessionEnd","session_id":"c-w2"}`, "state", "clear")
	conversation(w2, "c-w2b", 0.5)
	check(t, "a cleared conversation's cost stays, the new one adds", totals(), "boss-a 14.50 12.00, manager 15.50 12.00")

	// The first conversation resumed: Claude brings its cost back, counted live, not twice
	dev(w2, `{"hook_event_name":"SessionEnd","session_id":"c-w2b"}`, "state", "clear")
	conversation(w2, "c-w2", 8.25)
	check(t, "a resumed conversation counts once", totals(), "boss-a 14.75 4.50, manager 15.75 4.50")

	dev(boss, "", "agent", "close", "w2")
	check(t, "both workers closed", totals(), "boss-a 14.75 12.75, manager 15.75 12.75")

	dev(manager, "", "agent", "close", "boss-a")
	check(t, "a closed boss's own cost and what it kept go to the manager", totals(), "manager 15.75 14.75")
}
