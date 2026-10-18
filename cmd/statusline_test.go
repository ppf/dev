package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/open"
)

func TestStatuslinePrintsCost(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	for input, want := range map[string]string{
		`{"cost":{"total_cost_usd":1.236}}`: "$1.24\n",
		`{"model":{}}`:                      "",
		`{`:                                 "",
	} {
		out, errOut, err := execute(t, input, "statusline")
		if err != nil || out != want || errOut != "" {
			t.Errorf("statusline <%s = %q, %q, %v; want %q", input, out, errOut, err, want)
		}
	}
}

func TestStatuslineWrapsCommand(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	input := `{"cost":{"total_cost_usd":2},"model":{"display_name":"Opus"}}`
	out, _, err := execute(t, input, "statusline", "--", "cat")
	if err != nil || out != input {
		t.Errorf("statusline -- cat = %q, %v; want the input passed through", out, err)
	}
	out, errOut, err := execute(t, input, "statusline", "--", "sh", "-c", "echo line; echo why >&2; exit 3")
	if !errors.Is(err, open.ErrShown) || out != "line\n" || errOut != "why\n" {
		t.Errorf("failing command = %q, %q, %v; want its output and ErrShown", out, errOut, err)
	}
	if out, _, err := execute(t, input, "statusline", "sh", "-c", `echo "$1"`, "sh", "--compact"); err != nil || out != "--compact\n" {
		t.Errorf("wrapped flags without -- = %q, %v; want them passed to the command", out, err)
	}
	if _, _, err := execute(t, input, "statusline", "--", "/nonexistent/statusline"); err == nil || errors.Is(err, open.ErrShown) {
		t.Errorf("missing command: %v; want a start error", err)
	}
}

func TestPrintAgentsCost(t *testing.T) {
	usd := 1.236
	var out bytes.Buffer
	agents := []control.Agent{
		{Session: "app", Pane: "%1", Label: "Claude", State: "idle", Branch: "main", Path: "/src/app", CostUSD: &usd},
		{Session: "api", Pane: "%2", Label: "Codex", Path: "/src/api"},
	}
	if err := printAgents(&out, false, agents); err != nil {
		t.Fatal(err)
	}
	want := "app\t%1\tClaude\tidle\tmain\t/src/app\t$1.24\napi\t%2\tCodex\t-\t\t/src/api\t-\n"
	if out.String() != want {
		t.Errorf("printAgents =\n%q\nwant\n%q", out.String(), want)
	}
	out.Reset()
	if err := printAgents(&out, true, agents); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"cost_usd": 1.236`)) || bytes.Count(out.Bytes(), []byte("cost_usd")) != 1 {
		t.Errorf("JSON: %s; want cost_usd on the Claude agent only", out.String())
	}
}

func TestStatuslineRecordFailureStillRenders(t *testing.T) {
	// Under test tmux refuses the default server (ErrNoSocket), so recording fails without reaching one
	t.Setenv("TMUX_PANE", "%1")
	input := `{"cost":{"total_cost_usd":1.236}}`
	out, errOut, err := execute(t, input, "statusline")
	if err != nil || out != "$1.24\n" || !strings.Contains(errOut, "❌ dev statusline: ") {
		t.Errorf("bare: %q, %q, %v; want the cost printed and the error on stderr", out, errOut, err)
	}
	out, _, err = execute(t, input, "statusline", "--", "cat")
	if err != nil || out != input {
		t.Errorf("wrapped: %q, %v; want the input passed through", out, err)
	}
}

func TestPrintAgentsHideCost(t *testing.T) {
	usd := 1.5
	agents := []control.Agent{{Session: "app", Pane: "%1", Label: "Claude", CostUSD: &usd, HideCost: true}}
	var out bytes.Buffer
	if err := printAgents(&out, false, agents); err != nil {
		t.Fatal(err)
	}
	if want := "app\t%1\tClaude\t-\t\t\t-\n"; out.String() != want {
		t.Errorf("printAgents = %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := printAgents(&out, true, agents); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"cost_usd": 1.5`)) || bytes.Contains(out.Bytes(), []byte("HideCost")) {
		t.Errorf("JSON: %s; want cost_usd kept and no HideCost key", out.String())
	}
}
