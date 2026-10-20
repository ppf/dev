//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// dev statusline records the session cost on the agent pane for dev agent and the sidebar, and its context
// use for the boss inbox, passes the status line input to the wrapped command, and the SessionEnd hook clears both.
func TestStatuslineCost(t *testing.T) {
	// dev agent looks for the agent in window 1
	s := newServer(t, "set -g base-index 1\n")
	s.T("new-session", "-d", "-s", "agents", "sleep 600")
	pane := s.T("display", "-t", "=agents:", "-p", "#{pane_id}")
	run := func(stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %s: %v", strings.Join(argv, " "), err)
		}
		return string(out)
	}
	// The hook registers the pane as a Claude pane (@agent_cmd = its command)
	run(`{"hook_event_name":"SessionStart"}`, "state", "idle")

	input := `{"session_id":"s1","cost":{"total_cost_usd":1.25},"context_window":{"used_percentage":81.6},"model":{"display_name":"Opus"}}`
	check(t, "wrapped command gets the input", run(input, "statusline", "--", "cat"), input)
	check(t, "cost recorded on the pane", s.T("show-option", "-pqv", "-t", pane, "@agent_cost"), "1.25")
	check(t, "context use too", s.T("show-option", "-pqv", "-t", pane, "@agent_context"), "82")

	var agents []map[string]any
	if err := json.Unmarshal([]byte(run("", "agent", "list", "--json")), &agents); err != nil || len(agents) != 1 {
		t.Fatalf("❌ dev agent list --json: %v, %v", agents, err)
	}
	if usd, _ := agents[0]["cost_usd"].(float64); usd != 1.25 {
		t.Errorf("❌ cost_usd = %v, want 1.25", agents[0]["cost_usd"])
	}
	row := strings.Split(strings.TrimSpace(run("", "agent", "list")), "\t")
	check(t, "text row ends with the cost", row[len(row)-1], "$1.25")

	check(t, "a status line without cost prints nothing", run(`{"model":{}}`, "statusline"), "")
	check(t, "and clears the stale cost", s.T("show-option", "-pqv", "-t", pane, "@agent_cost"), "")
	check(t, "and context use", s.T("show-option", "-pqv", "-t", pane, "@agent_context"), "")

	run(input, "statusline")
	run(`{"hook_event_name":"SessionEnd"}`, "state", "clear")
	check(t, "SessionEnd clears the cost and context use", s.T("show-option", "-pqv", "-t", pane, "@agent_cost")+s.T("show-option", "-pqv", "-t", pane, "@agent_context"), "")
}
