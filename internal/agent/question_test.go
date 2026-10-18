package agent

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// The goldens are Claude Code 2.1.302's hook input, captured on a throwaway tmux server.
func TestHookQuestionGolden(t *testing.T) {
	ask := []Ask{
		{Question: "Which color?", Header: "Color", Options: []Choice{{"Red", "Warm"}, {"Blue", "Cool"}}},
		{Question: "Which extras?", Header: "Extras", Options: []Choice{{"Tests", "Unit tests"}, {"Docs", "README"}}, MultiSelect: true},
	}
	tests := []struct {
		file string
		want Question
		ok   bool
	}{
		{"claude-permission-request-ask.json", Question{Tool: AskUserQuestion, Questions: ask}, true},
		{"claude-pretooluse-ask.json", Question{Tool: AskUserQuestion, Questions: ask}, true},
		{"claude-permission-request-bash.json", Question{Tool: "Bash", Command: "mkdir -p build/out && touch build/out/x.txt",
			Description: "Create build/out directory and touch x.txt"}, true},
		// The file's content is left out
		{"claude-permission-request-write.json", Question{Tool: "Write", File: "/tmp/proj/notes.txt"}, true},
		{"claude-notification-permission.json", Question{}, false},
		{"claude-posttooluse-ask.json", Question{}, false},
	}
	for _, tt := range tests {
		input, err := os.ReadFile("testdata/" + tt.file)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := HookQuestion(input); ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: %+v %v, want %+v %v", tt.file, got, ok, tt.want, tt.ok)
		}
	}
}

func TestHookQuestionFilters(t *testing.T) {
	for _, input := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		`{"hook_event_name":"PermissionRequest"}`,
		`not json`,
	} {
		if q, ok := HookQuestion([]byte(input)); ok {
			t.Errorf("%s: %+v", input, q)
		}
	}
	long := strings.Repeat("x", 400)
	q, _ := HookQuestion([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"` + long + `"}}`))
	if r := []rune(q.Command); len(r) != maxDetail || r[maxDetail-1] != '…' {
		t.Errorf("long command kept %d runes", len(r))
	}
}

func TestRecordQuestionRoundTrip(t *testing.T) {
	var got []string
	c := tmux.NewFake(func(args []string) (string, error) { got = args; return "", nil })
	q := Question{Tool: "Bash", Command: "echo \"a\tb\"\nls"}
	if err := RecordQuestion(c, "%1", q); err != nil {
		t.Fatal(err)
	}
	// One list-panes row per pane: the value must hold no tab or newline
	if len(got) != 6 || got[4] != OptQuestion || strings.ContainsAny(got[5], "\t\n") {
		t.Fatalf("ran %q", got)
	}
	if back, ok := ParseQuestion(got[5]); !ok || !reflect.DeepEqual(back, q) {
		t.Errorf("round trip %+v", back)
	}
	for _, v := range []string{"", "{", `{"tool":""}`} {
		if _, ok := ParseQuestion(v); ok {
			t.Errorf("ParseQuestion(%q) ok", v)
		}
	}
}
