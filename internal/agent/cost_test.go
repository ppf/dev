package agent

import (
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		input string
		want  Status
	}{
		{`{"cost":{"total_cost_usd":1.2345,"total_duration_ms":10}}`, Status{Cost: 1.2345, CostOK: true}},
		{`{"cost":{"total_cost_usd":0}}`, Status{CostOK: true}},
		{`{"cost":{}}`, Status{}},
		{`{"cost":{"total_cost_usd":null}}`, Status{}},
		{`{"cost":{"total_cost_usd":-1}}`, Status{}},
		{`{"cost":{"total_cost_usd":"1"}}`, Status{}},
		{`{"model":{"display_name":"X"}}`, Status{}},
		{`{"cost":{"total_cost_usd":2},"context_window":{"used_percentage":81.6,"remaining_percentage":18.4}}`, Status{Cost: 2, CostOK: true, Context: 82, ContextOK: true}},
		{`{"context_window":{"used_percentage":0}}`, Status{ContextOK: true}},
		{`{"context_window":{"used_percentage":null}}`, Status{}},
		{`{"context_window":{"total_input_tokens":5}}`, Status{}},
		{`{"cost":{"total_cost_usd":1.5},"context_window":{"used_percentage":"42%"}}`, Status{Cost: 1.5, CostOK: true}},
		{`{"cost":"x","context_window":{"used_percentage":42}}`, Status{Context: 42, ContextOK: true}},
		{`{`, Status{}},
		{``, Status{}},
	}
	for _, tt := range tests {
		if got := ParseStatus([]byte(tt.input)); got != tt.want {
			t.Errorf("ParseStatus(%s) = %+v; want %+v", tt.input, got, tt.want)
		}
	}
}

func TestParseCost(t *testing.T) {
	tests := []struct {
		v   string
		usd float64
		ok  bool
	}{
		{"1.2345", 1.2345, true}, {"0", 0, true}, {"", 0, false}, {"x", 0, false}, {"-0.5", 0, false},
		{"NaN", 0, false}, {"+Inf", 0, false}, {"inf", 0, false},
	}
	for _, tt := range tests {
		if usd, ok := ParseCost(tt.v); usd != tt.usd || ok != tt.ok {
			t.Errorf("ParseCost(%q) = %v, %v; want %v, %v", tt.v, usd, ok, tt.usd, tt.ok)
		}
	}
}

func TestRecordStatus(t *testing.T) {
	tests := []struct {
		st   Status
		want string
	}{
		{Status{Cost: 1.2345, CostOK: true, Context: 81, ContextOK: true}, "set-option -p -t %3 @agent_cost 1.2345 ; set-option -p -t %3 @agent_context 81"},
		{Status{}, "set-option -pu -t %3 @agent_cost ; set-option -pu -t %3 @agent_context"},
	}
	for _, tt := range tests {
		var calls []string
		c := tmux.NewFake(func(args []string) (string, error) { calls = append(calls, strings.Join(args, " ")); return "", nil })
		if err := RecordStatus(c, "%3", tt.st); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0] != tt.want {
			t.Errorf("RecordStatus(%+v) ran %q, want one: %q", tt.st, calls, tt.want)
		}
	}
}

func TestDetectCost(t *testing.T) {
	claude := Pane{Window: 1, ID: "%1", Command: "claude", StateCmd: "claude", State: Idle, Cost: "2.5"}
	a, ok := Detect([]Pane{claude}, "")
	if !ok || !a.CostOK || a.Cost != 2.5 {
		t.Errorf("Claude pane: %+v, %v; want cost 2.5", a, ok)
	}
	claude.Cost = ""
	if a, _ := Detect([]Pane{claude}, ""); a.CostOK {
		t.Errorf("Claude pane without @agent_cost: %+v; want no cost", a)
	}
	// A Codex pane can carry a Claude's leftover @agent_cost; it isn't Codex's
	codex := Pane{Window: 1, ID: "%2", Command: "codex", Cost: "9"}
	if a, ok := Detect([]Pane{codex}, ""); !ok || a.CostOK {
		t.Errorf("Codex pane: %+v, %v; want no cost", a, ok)
	}
}
