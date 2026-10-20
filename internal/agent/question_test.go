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

func TestQuestionAsk(t *testing.T) {
	ask := Question{Tool: AskUserQuestion, Questions: []Ask{
		{Question: "Which test color should I pick?", Options: []Choice{{"Red", "Warm"}, {"Green", ""}, {"Blue", "Cool"}}},
		{Question: "Which extras?"},
	}}
	tests := []struct {
		q          Question
		want, seen string
	}{
		{ask, "Which test color should I pick? (Red / Green / Blue) +1 more", "Which test color\n should I pick?\n❯ 1. Red"},
		{Question{Tool: AskUserQuestion, Questions: []Ask{{Question: "Go on?"}}}, "Go on?", "Go on?"},
		{Question{Tool: "Bash", Command: "rm -rf build", Description: "Delete build/"}, "allow Bash: rm -rf build", "rm -rf\n  build\nDo you want to proceed?"},
		{Question{Tool: "Write", File: "/tmp/proj/notes.txt"}, "allow Write: /tmp/proj/notes.txt", "Do you want to create notes.txt?"},
		{Question{Tool: "WebSearch"}, "allow WebSearch", "WebSearch(cats)\nDo you want to proceed?"},
	}
	for _, tt := range tests {
		if got := tt.q.Ask(); got != tt.want {
			t.Errorf("Ask() = %q, want %q", got, tt.want)
		}
		if !tt.q.Shows(tt.seen) {
			t.Errorf("%q: not shown on %q", tt.want, tt.seen)
		}
		if tt.q.Shows("Do you want to proceed?\n❯ 1. Yes") {
			t.Errorf("%q: shown on another dialog", tt.want)
		}
	}
	// A permission needs its "Do you want": a short command is in many a question's words
	if q := (Question{Tool: "Bash", Command: "ls"}); q.Shows("Which tools should I install?\n❯ 1. Red") {
		t.Error("permission shown on a question")
	}
	if q := (Question{Tool: AskUserQuestion, Questions: []Ask{{Question: " "}}}); q.Shows("Which color?") {
		t.Error("blank question shown")
	}
}
