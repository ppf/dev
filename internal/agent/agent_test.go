package agent

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name, state, input string
		want               string
		ok                 bool
	}{
		{"working", Working, `{"session_id":"s"}`, Working, true},
		{"working subagent", Working, `{"agent_id":"a1"}`, "", false},
		{"working subagent null id", Working, `{"agent_id":null}`, "", false},
		{"working nested agent_id is not a subagent", Working, `{"tool_input":{"agent_id":"a1"}}`, Working, true},
		{"working ignores interrupt", Working, `{"is_interrupt":true}`, Working, true},
		{"tool-failed interrupted", ToolFailed, `{"is_interrupt": true}`, Idle, true},
		{"tool-failed not interrupted", ToolFailed, `{"is_interrupt":false}`, Working, true},
		{"tool-failed no interrupt field", ToolFailed, `{}`, Working, true},
		{"tool-failed interrupt string is not true", ToolFailed, `{"is_interrupt":"true"}`, Working, true},
		{"tool-failed nested interrupt", ToolFailed, `{"tool_response":{"is_interrupt":true}}`, Working, true},
		{"tool-failed subagent", ToolFailed, `{"agent_id":"a1","is_interrupt":true}`, "", false},
		{"waiting subagent still recorded", Waiting, `{"agent_id":"a1"}`, Waiting, true},
		{"idle", Idle, `{}`, Idle, true},
		{"clear", Clear, `{}`, Clear, true},
		{"unknown passes through", "bogus", `{}`, "bogus", true},
		{"json null", Working, `null`, Working, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := Resolve(tt.state, []byte(tt.input))
			if err != nil || got != tt.want || ok != tt.ok {
				t.Errorf("Resolve(%q, %s) = %q, %v, %v; want %q, %v, nil", tt.state, tt.input, got, ok, err, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveMalformed(t *testing.T) {
	for _, input := range []string{"", "{", "[]", `"x"`, "not json"} {
		for _, state := range []string{Working, Idle} {
			if got, ok, err := Resolve(state, []byte(input)); err == nil {
				t.Errorf("Resolve(%q, %q) = %q, %v, nil; want error", state, input, got, ok)
			}
		}
	}
}

func TestDetect(t *testing.T) {
	claude := Pane{Window: 1, ID: "%2", Command: "2.1.3", State: Waiting, StateCmd: "2.1.3"}
	tests := []struct {
		name    string
		panes   []Pane
		sidebar string
		wantID  string
		label   string
		state   string
	}{
		{"none", nil, "%0", "", "", ""},
		{"claude", []Pane{{Window: 1, ID: "%1", Command: "zsh"}, claude}, "%0", "%2", "Claude", Waiting},
		{"claude in other window ignored", []Pane{{Window: 2, ID: "%2", Command: "2.1.3", StateCmd: "2.1.3"}}, "%0", "", "", ""},
		{"sidebar excluded", []Pane{claude}, "%2", "", "", ""},
		{"no sidebar", []Pane{claude}, "", "%2", "Claude", Waiting},
		{"crash leftover in lazygit", []Pane{{Window: 1, ID: "%2", Command: "lazygit", State: Working, StateCmd: "2.1.3"}}, "%0", "", "", ""},
		{"nvim without hook", []Pane{{Window: 1, ID: "%2", Command: "nvim"}}, "%0", "", "", ""},
		{"shell without hook", []Pane{{Window: 1, ID: "%2", Command: "zsh"}}, "%0", "", "", ""},
		{"codex", []Pane{{Window: 1, ID: "%3", Command: "codex", State: Working}}, "%0", "%3", "Codex", ""},
		{"codex with matching state", []Pane{{Window: 1, ID: "%3", Command: "codex", State: Idle, StateCmd: "codex"}}, "%0", "%3", "Codex", Idle},
		{"codex with stale state", []Pane{{Window: 1, ID: "%3", Command: "codex-aarch64", State: Idle, StateCmd: "2.1.3"}}, "%0", "%3", "Codex", ""},
		{"codexx is not codex", []Pane{{Window: 1, ID: "%3", Command: "codexx"}}, "%0", "", "", ""},
		{"claude beats earlier codex", []Pane{{Window: 1, ID: "%3", Command: "codex"}, claude}, "%0", "%2", "Claude", Waiting},
		{"first codex wins", []Pane{{Window: 1, ID: "%3", Command: "codex"}, {Window: 1, ID: "%4", Command: "codex"}}, "%0", "%3", "Codex", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Detect(tt.panes, tt.sidebar)
			if ok != (tt.wantID != "") || got.Pane.ID != tt.wantID || got.Label != tt.label || got.State != tt.state {
				t.Errorf("Detect = %+v, %v; want ID %q, Label %q, State %q", got, ok, tt.wantID, tt.label, tt.state)
			}
		})
	}
}
