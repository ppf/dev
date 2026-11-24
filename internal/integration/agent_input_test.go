//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// input is get --json's input box.
type input struct {
	Text       string `json:"text"`
	Suggestion bool   `json:"suggestion"`
}

// dev agent get --json reports a Claude pane's input box, Claude Code's dim prompt suggestion marked as one, and
// dev agent read marks that row; the same words typed are the user's (real captures, drawn in a pane).
func TestAgentInputSuggestion(t *testing.T) {
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(t.TempDir(), "state"))
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %v: %v", argv, err)
		}
		return string(out)
	}
	for _, tt := range []struct {
		name, file string
		want       input
		row        string
	}{
		{"suggested", "claude-input-suggestion", input{Text: "run it", Suggestion: true}, "run it  [suggestion]\n"},
		{"typed", "claude-input-typed", input{Text: "run it"}, "run it\n"},
	} {
		raw, err := os.ReadFile(filepath.Join(checkout, "internal", "screen", "testdata", tt.file+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		draw := filepath.Join(t.TempDir(), tt.name)
		writeFile(t, draw, strings.ReplaceAll(string(raw), `\x1b`, "\x1b"))
		s.T("new-session", "-d", "-s", tt.name, "-x", "250", "-y", "20", "sh", "-c", "cat "+draw+"; exec sleep 600")
		pane := s.T("display", "-t", "="+tt.name+":", "-p", "#{pane_id}")
		dev([]string{"TMUX_PANE=" + pane}, "state", "idle")
		waitFor(t, tt.name+" drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane), "auto mode on") })

		var got struct {
			Input *input `json:"input"`
		}
		out := dev(nil, "agent", "get", tt.name, "--json")
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.Input == nil || *got.Input != tt.want {
			t.Errorf("❌ %s: get --json input = %+v (%v), want %+v\n%s", tt.name, got.Input, err, tt.want, out)
		}
		read := dev(nil, "agent", "read", tt.name)
		// Its input row; ❯ is followed by a no-break space
		if !strings.Contains(read, "❯\u00a0"+tt.row) || tt.name == "typed" && strings.Contains(read, "[suggestion]") {
			t.Errorf("❌ %s: read lacks %q:\n%s", tt.name, tt.row, read)
		}
	}
}
