package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// execute runs dev with args and stdin in a clean env and returns stdout, stderr and the error.
func execute(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("TMUX", "")
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestRootPrintsHelp(t *testing.T) {
	out, _, err := execute(t, "", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tmux workspace launcher") || strings.Contains(out, "completion") {
		t.Errorf("help output:\n%s", out)
	}
}

func TestState(t *testing.T) {
	tests := []struct {
		name, pane, stdin string
		args              []string
		wantErr           error // nil = success
		wantArgErr        bool
		wantStderr        string
	}{
		{name: "no TMUX_PANE is a no-op", args: []string{"working"}, stdin: "not json"},
		{name: "malformed JSON reported, exit 0", pane: "%1", args: []string{"working"}, stdin: "{",
			wantStderr: "❌ dev state: agent: hook input: unexpected end of JSON input\n"},
		{name: "subagent skipped", pane: "%1", args: []string{"working"}, stdin: `{"agent_id":"a1"}`},
		{name: "records on the pane", pane: "%1", args: []string{"idle"}, stdin: `{}`, wantErr: tmux.ErrNoSocket},
		{name: "unknown state", args: []string{"busy"}, wantArgErr: true},
		{name: "no state", args: nil, wantArgErr: true},
		{name: "two states", args: []string{"idle", "working"}, wantArgErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX_PANE", tt.pane)
			_, errOut, err := execute(t, tt.stdin, append([]string{"state"}, tt.args...)...)
			switch {
			case tt.wantArgErr:
				if err == nil {
					t.Error("want an argument error")
				}
			case !errors.Is(err, tt.wantErr):
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if errOut != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", errOut, tt.wantStderr)
			}
		})
	}
}
