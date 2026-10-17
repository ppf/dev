//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// dev agent list --mine, run in a coordinator's pane, lists only the sessions it started; the full list
// names the starter of each worker.
func TestAgentListMine(t *testing.T) {
	// dev agent looks for the agent in window 1
	s := newServer(t, "set -g base-index 1\n")
	pane := map[string]string{}
	for _, name := range []string{"coord", "worker", "user"} {
		s.T("new-session", "-d", "-s", name, "sleep 600")
		pane[name] = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
	}
	s.T("set-option", "-t", "=worker:", "@dev_started_by", s.T("display", "-t", "=coord:", "-p", "#{session_id}"))
	run := func(asPane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + asPane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %s: %v", strings.Join(argv, " "), err)
		}
		return string(out)
	}
	for _, name := range []string{"coord", "worker", "user"} {
		run(pane[name], `{"hook_event_name":"SessionStart"}`, "state", "idle")
	}
	sessions := func(out string) string {
		var agents []map[string]any
		if err := json.Unmarshal([]byte(out), &agents); err != nil {
			t.Fatalf("❌ %v: %s", err, out)
		}
		var b []string
		for _, a := range agents {
			by, _ := a["started_by"].(string)
			b = append(b, a["session"].(string)+"<"+by)
		}
		return strings.Join(b, " ")
	}
	check(t, "full list names the starter", sessions(run(pane["user"], "", "agent", "list", "--json")), "coord< user< worker<coord")
	check(t, "--mine in the coordinator", sessions(run(pane["coord"], "", "agent", "list", "--mine", "--json")), "worker<coord")
	check(t, "--mine elsewhere", sessions(run(pane["user"], "", "agent", "list", "--mine", "--json")), "")
}
