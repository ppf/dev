package agent

import (
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestStatusCost(t *testing.T) {
	tests := []struct {
		input string
		usd   float64
		ok    bool
	}{
		{`{"cost":{"total_cost_usd":1.2345,"total_duration_ms":10}}`, 1.2345, true},
		{`{"cost":{"total_cost_usd":0}}`, 0, true},
		{`{"cost":{}}`, 0, false},
		{`{"cost":{"total_cost_usd":null}}`, 0, false},
		{`{"cost":{"total_cost_usd":-1}}`, 0, false},
		{`{"cost":{"total_cost_usd":"1"}}`, 0, false},
		{`{"model":{"display_name":"X"}}`, 0, false},
		{`{`, 0, false},
		{``, 0, false},
	}
	for _, tt := range tests {
		if usd, ok := StatusCost([]byte(tt.input)); usd != tt.usd || ok != tt.ok {
			t.Errorf("StatusCost(%s) = %v, %v; want %v, %v", tt.input, usd, ok, tt.usd, tt.ok)
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

func TestRecordCost(t *testing.T) {
	var got string
	c := tmux.NewFake(func(args []string) (string, error) { got = strings.Join(args, " "); return "", nil })
	if err := RecordCost(c, "%3", 1.2345); err != nil {
		t.Fatal(err)
	}
	if want := "set-option -p -t %3 @agent_cost 1.2345"; got != want {
		t.Errorf("RecordCost ran %q, want %q", got, want)
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
