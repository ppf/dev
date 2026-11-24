package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

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

func TestHookSession(t *testing.T) {
	tests := []struct {
		name, input string
		want        Session
	}{
		{"session", `{"session_id":"0a1b-2c","transcript_path":"/t/0a1b-2c.jsonl"}`, Session{"0a1b-2c", "/t/0a1b-2c.jsonl"}},
		{"no session", `{}`, Session{}},
		{"json null", `null`, Session{}},
		{"id with shell characters", `{"session_id":"x; rm -rf ~","transcript_path":"/t"}`, Session{}},
		{"id read as an option", `{"session_id":"-p","transcript_path":"/t"}`, Session{}},
		{"id not a string", `{"session_id":7,"transcript_path":"/t"}`, Session{}},
		{"transcript with a tab", `{"session_id":"s","transcript_path":"/t\tx"}`, Session{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HookSession([]byte(tt.input)); got != tt.want {
				t.Errorf("HookSession(%s) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRecord(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	session := " ; set-option -p -t %1 @agent_session s ; set-option -p -t %1 @agent_transcript /t/s.jsonl"
	noSession := " ; set-option -pu -t %1 @agent_session ; set-option -pu -t %1 @agent_transcript"
	noSocket := " ; set-option -pu -t %1 @agent_socket ; set-option -pu -t %1 @agent_pid"
	recorded := "set-option -pF -t %1 @agent_since #{?#{&&:#{==:#{@agent_state},idle},#{@agent_since}},#{@agent_since},100} ; set-option -p -t %1 @agent_active 100 ; set-option -p -t %1 @agent_state idle ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"
	tests := []struct {
		name, state string
		session     Session
		want        string
	}{
		{"state and session", Idle, Session{"s", "/t/s.jsonl"}, recorded + noSocket + session},
		{"no session unsets the old one", Idle, Session{}, recorded + noSocket + noSession},
		{"a decision starts its own stretch", Waiting, Session{}, "set-option -pF -t %1 @agent_since 100 ; set-option -p -t %1 @agent_active 100 ; set-option -p -t %1 @agent_state waiting ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -p -t %1 @agent_waiting_for permission_prompt" + noSocket + noSession},
		{"clear unsets everything but the ended conversation", Clear, Session{"s", "/t/s.jsonl"},
			"set-option -pu -t %1 @agent_state ; set-option -pu -t %1 @agent_cmd ; set-option -pu -t %1 @agent_cost ; set-option -pu -t %1 @agent_context ; set-option -pu -t %1 @agent_since ; set-option -pu -t %1 @agent_active ; set-option -pu -t %1 @agent_prompt_id ; set-option -pu -t %1 @agent_question ; set-option -pu -t %1 @agent_socket ; set-option -pu -t %1 @agent_mode ; set-option -pu -t %1 @agent_pid" +
				" ; set-option -p -t %1 @agent_ended_session s ; set-option -p -t %1 @agent_ended_transcript /t/s.jsonl" + noSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := tmux.NewFake(func(args []string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				return "", nil
			})
			h := Hook{}
			if tt.state == Waiting {
				h = Hook{Event: "Notification", NotificationType: "permission_prompt"}
			}
			if err := Record(c, "%1", tt.state, tt.session, h); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0] != tt.want {
				t.Errorf("calls = %q, want one: %q", calls, tt.want)
			}
		})
	}
}

func TestTouch(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	var calls []string
	c := tmux.NewFake(func(args []string) (string, error) { calls = append(calls, strings.Join(args, " ")); return "", nil })
	if err := Touch(c, "%1"); err != nil {
		t.Fatal(err)
	}
	if want := "set-option -p -t %1 @agent_active 100"; len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want one: %q", calls, want)
	}
}

func TestDialogGone(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	var calls [][]string
	c := tmux.NewFake(func(args []string) (string, error) {
		calls = append(calls, args)
		return "", nil
	})
	if err := DialogGone(c, "%1", Idle, "50"); err != nil {
		t.Fatal(err)
	}
	want := []string{"if-shell", "-F", "-t", "%1", "#{&&:#{==:#{@agent_state},waiting},#{==:#{@agent_since},50}}",
		"set-option -p -t %1 @agent_state idle ; set-option -p -t %1 @agent_since 100 ; set-option -pu -t %1 @agent_question"}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("calls = %q, want one: %q", calls, want)
	}
}

func TestInterrupted(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	var calls [][]string
	c := tmux.NewFake(func(args []string) (string, error) {
		calls = append(calls, args)
		return "", nil
	})
	if err := Interrupted(c, "%1", "50", "7"); err != nil {
		t.Fatal(err)
	}
	want := []string{"if-shell", "-F", "-t", "%1", "#{&&:#{==:#{@agent_state},working},#{&&:#{==:#{@agent_since},50},#{==:#{@agent_prompts},7}}}",
		"set-option -p -t %1 @agent_state idle ; set-option -p -t %1 @agent_since 100 ; set-option -pu -t %1 @agent_question"}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("calls = %q, want one: %q", calls, want)
	}
}

func TestAgyPermission(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	for _, claim := range []string{"agy-dialog-100000000000", "agy-dialog-99"} {
		var calls [][]string
		c := tmux.NewFake(func(args []string) (string, error) {
			calls = append(calls, args)
			if args[0] == "display-message" {
				return claim + "\n", nil
			}
			return "", nil
		})
		q, recorded, err := AgyPermission(c, "%1", "50", "7", "55", "Bash", `echo "$HOME" \ x`, "Run this command?")
		if err != nil || recorded != (claim == "agy-dialog-100000000000") || q.Ask() != `allow Bash: echo "$HOME" \ x` {
			t.Errorf("claim %s: AgyPermission = %+v, %v, %v", claim, q, recorded, err)
		}
		want := []string{"if-shell", "-F", "-t", "%1", "#{&&:#{==:#{@agent_state},working},#{&&:#{==:#{@agent_since},50},#{&&:#{==:#{@agent_prompts},7},#{==:#{@agent_active},55}}}}",
			"set-option -p -t %1 @agent_state waiting ; set-option -p -t %1 @agent_since 100 ; set-option -p -t %1 @agent_waiting_for permission_prompt ; " +
				`set-option -p -t %1 @agent_question "{\"tool\":\"Bash\",\"command\":\"echo \\\"\$HOME\\\" \\\\ x\",\"description\":\"Run this command?\"}" ; ` +
				"set-option -p -t %1 @agent_agy_claim agy-dialog-100000000000"}
		if len(calls) != 2 || !slices.Equal(calls[0], want) {
			t.Errorf("calls = %q, want first: %q", calls, want)
		}
	}
	if _, _, err := AgyPermission(tmux.NewFake(func([]string) (string, error) { return "", errors.New("boom") }), "%1", "50", "7", "55", "", "ls", "?"); err == nil {
		t.Error("a failed write: no error")
	}
}

func TestResumeCommand(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	noRC := `'--settings' '{"remoteControlAtStartup":false}'`
	both := Start{Model: "sonnet", Effort: "medium"}
	tests := []struct {
		name, kind, id, transcript string
		worker                     bool
		start                      Start
		want                       string
		resumed, ok                bool
	}{
		{"resumes its conversation", Claude, "s", transcript, false, Start{}, `'claude' '--resume' 's' '--remote-control=dev-x'`, true, true},
		{"transcript gone", Claude, "s", transcript + ".gone", false, Start{}, `'claude' '--remote-control=dev-x'`, false, true},
		{"no id", Claude, "", transcript, false, Start{}, `'claude' '--remote-control=dev-x'`, false, true},
		{"id unsafe to type", Claude, "s; rm -rf ~", transcript, false, Start{}, `'claude' '--remote-control=dev-x'`, false, true},
		{"a worker resumes without Remote Control", Claude, "s", transcript, true, Start{}, `'claude' '--resume' 's' ` + noRC, true, true},
		{"a fresh worker too", Claude, "", transcript, true, Start{}, `'claude' ` + noRC, false, true},
		{"unknown kind", "codex", "s", transcript, false, Start{}, "", false, false},
		{"a resume keeps the model itself, so only the effort", Claude, "s", transcript, true, both, `'claude' '--resume' 's' '--effort' 'medium' ` + noRC, true, true},
		{"a resume started with a model alone", Claude, "s", transcript, false, Start{Model: "sonnet"}, `'claude' '--resume' 's' '--remote-control=dev-x'`, true, true},
		{"a fresh agent gets the model too", Claude, "s", transcript + ".gone", true, both, `'claude' '--model' 'sonnet' '--effort' 'medium' ` + noRC, false, true},
		{"a model is typed as one argument", Claude, "", transcript, false, Start{Model: "claude-opus-5-5[1m]"}, `'claude' '--model' 'claude-opus-5-5[1m]' '--remote-control=dev-x'`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, resumed, ok := ResumeCommand(tt.kind, tt.id, tt.transcript, "dev-x", tt.worker, tt.start)
			if got != tt.want || resumed != tt.resumed || ok != tt.ok {
				t.Errorf("ResumeCommand = %q, %v, %v; want %q, %v, %v", got, resumed, ok, tt.want, tt.resumed, tt.ok)
			}
		})
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
		{"grok", []Pane{{Window: 1, ID: "%5", Command: "grok-1.0.62-mac", State: Waiting, StateCmd: "grok-1.0.62-mac"}}, "%0", "%5", "Grok", Waiting},
		{"grok without hook", []Pane{{Window: 1, ID: "%5", Command: "grok"}}, "%0", "%5", "Grok", ""},
		{"claude beats earlier grok", []Pane{{Window: 1, ID: "%5", Command: "grok"}, claude}, "%0", "%2", "Claude", Waiting},
		{"grok beats earlier codex", []Pane{{Window: 1, ID: "%3", Command: "codex"}, {Window: 1, ID: "%5", Command: "grok"}}, "%0", "%5", "Grok", ""},
		{"grokky is not grok", []Pane{{Window: 1, ID: "%5", Command: "grokky"}}, "%0", "", "", ""},
		{"first codex wins", []Pane{{Window: 1, ID: "%3", Command: "codex"}, {Window: 1, ID: "%4", Command: "codex"}}, "%0", "%3", "Codex", ""},
		{"agy", []Pane{{Window: 1, ID: "%6", Command: "agy", State: Working, StateCmd: "agy"}}, "%0", "%6", "Agy", Working},
		{"agy without hook", []Pane{{Window: 1, ID: "%6", Command: "agy"}}, "%0", "%6", "Agy", ""},
		{"claude beats earlier agy", []Pane{{Window: 1, ID: "%6", Command: "agy"}, claude}, "%0", "%2", "Claude", Waiting},
		{"agy beats earlier codex", []Pane{{Window: 1, ID: "%3", Command: "codex"}, {Window: 1, ID: "%6", Command: "agy"}}, "%0", "%6", "Agy", ""},
		{"grok beats earlier agy", []Pane{{Window: 1, ID: "%6", Command: "agy"}, {Window: 1, ID: "%5", Command: "grok"}}, "%0", "%5", "Grok", ""},
		{"agyx is not agy", []Pane{{Window: 1, ID: "%6", Command: "agyx"}}, "%0", "", "", ""},
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

func TestAgyHook(t *testing.T) {
	const ids = `"conversationId":"110c93f6-c88f","transcriptPath":"/t.jsonl","cwd":"/w"`
	const session = `"session_id":"110c93f6-c88f","transcript_path":"/t.jsonl"`
	const ask = `{"hookEventName":"PreToolUse","toolCall":{"name":"ask_question","args":{"questions":[{"is_multi_select":false,` +
		`"options":["(Recommended) Red","Blue"],"question":"Do you prefer red or blue?"}]}},` + ids + `}`
	tests := []struct {
		input string
		kind  AgyKind
		want  string // its input in Claude's shape
	}{
		{`{"hookEventName":"PreInvocation","invocationNum":0,"lastUserInput":"say ok",` + ids + `}`, AgyTurn,
			`{"cwd":"/w","hook_event_name":"UserPromptSubmit","prompt":"say ok",` + session + `}`},
		{`{"hookEventName":"Stop","fullyIdle":true,"lastUserInput":"say ok",` + ids + `}`, AgyTurn,
			`{"cwd":"/w","hook_event_name":"Stop","last_assistant_message":"","prompt":"say ok",` + session + `}`},
		{ask, AgyAsk, `{"cwd":"/w","hook_event_name":"Notification","message":"Do you prefer red or blue? ((Recommended) Red / Blue)",` +
			`"notification_type":"permission_prompt","prompt":"",` + session + `}`},
		{`{"hookEventName":"PostToolUse","toolCall":{"name":"ask_question"},` + ids + `}`, AgyToolDone,
			`{"cwd":"/w","hook_event_name":"PostToolUse","prompt":"",` + session + `}`},
		{`{"hookEventName":"PreToolUse","toolCall":{"name":"run_command"},` + ids + `}`, AgyTool, ""},
		{`{"hookEventName":"PreInvocation","invocationNum":1,` + ids + `}`, AgyProgress, ""},
		{`{"hookEventName":"PostInvocation","invocationNum":0,` + ids + `}`, AgyProgress, ""},
		{`{"hookEventName":"SessionStart",` + ids + `}`, AgyProgress, ""},
	}
	for _, tt := range tests {
		ev, err := AgyHook([]byte(tt.input))
		if err != nil || ev.Kind != tt.kind || string(ev.Input) != tt.want {
			t.Errorf("AgyHook(%s) = %v %s, %v; want %v %s", tt.input, ev.Kind, ev.Input, err, tt.kind, tt.want)
		}
		if tt.want != "" && HookSession(ev.Input) != (Session{ID: "110c93f6-c88f", Transcript: "/t.jsonl"}) {
			t.Errorf("AgyHook(%s): session %+v", tt.input, HookSession(ev.Input))
		}
	}
	ev, _ := AgyHook([]byte(ask))
	want := Question{Tool: AskUserQuestion, Questions: []Ask{{Question: "Do you prefer red or blue?",
		Options: []Choice{{Label: "(Recommended) Red"}, {Label: "Blue"}}}}}
	if !reflect.DeepEqual(ev.Question, want) {
		t.Errorf("AgyHook(ask_question).Question = %+v, want %+v", ev.Question, want)
	}
	ev, _ = AgyHook([]byte(`{"hookEventName":"PreToolUse","toolCall":{"name":"ask_question","args":{"questions":[` +
		`{"is_multi_select":true,"options":["A","B"],"question":"Which?"},{"options":[],"question":"Why?"}]}}}`))
	if got := ev.Question.Ask(); got != "Which? (A / B) +1 more" || !ev.Question.Questions[0].MultiSelect {
		t.Errorf("AgyHook(two questions): %q, %+v", got, ev.Question)
	}
	for _, input := range []string{"{", `{"hookEventName":"PreInvocation"}`, `{"hookEventName":"PreToolUse","toolCall":{"name":"ask_question","args":{"question":"Red?"}}}`} {
		if _, err := AgyHook([]byte(input)); err == nil {
			t.Errorf("AgyHook(%s): no error", input)
		}
	}
}

func TestAgyLastReply(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		path := filepath.Join(dir, "transcript_full.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const (
		user  = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nsay ok\n</USER_REQUEST>"}`
		call  = `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command"}]}`
		ran   = `{"step_index":2,"source":"MODEL","type":"GENERIC","content":"Command exited with code 0."}`
		reply = `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","content":"hi"}`
	)
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"its reply after a tool call", []string{user, call, ran, reply}, "hi"},
		{"a turn with no reply yet", []string{reply, user, call}, ""},
		{"no turn", nil, ""},
	}
	for _, tt := range tests {
		if got, err := AgyLastReply(write(tt.lines...)); err != nil || got != tt.want {
			t.Errorf("%s: AgyLastReply = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if got, err := AgyLastReply(write(user, "", reply, "")); err != nil || got != "hi" {
		t.Errorf("blank lines: AgyLastReply = %q, %v", got, err)
	}
	// Past its tail: the cut line is left out, and a tail without a prompt or reply is an error
	long := `{"type":"GENERIC","content":"` + strings.Repeat("x", agyTail) + `"}`
	if got, err := AgyLastReply(write(long, user, call, ran, reply)); err != nil || got != "hi" {
		t.Errorf("a long transcript: AgyLastReply = %q, %v", got, err)
	}
	// A tail starting right on a line: that line is whole
	exact := `{"source":"MODEL","type":"PLANNER_RESPONSE","content":"` + strings.Repeat("y", agyTail-len(`{"source":"MODEL","type":"PLANNER_RESPONSE","content":""}`)-1) + `"}`
	if got, err := AgyLastReply(write(user, exact)); err != nil || !strings.HasPrefix(got, "yyy") {
		t.Errorf("a tail starting on its line: AgyLastReply = %.10q, %v", got, err)
	}
	if _, err := AgyLastReply(write(user, long)); err == nil {
		t.Error("a tail of one cut line: no error")
	}
	if _, err := AgyLastReply(write(user, "{")); err == nil {
		t.Error("a line that isn't JSON: no error")
	}
	if _, err := AgyLastReply(filepath.Join(dir, "gone.jsonl")); err == nil {
		t.Error("no transcript: no error")
	}
}

func TestAgyModels(t *testing.T) {
	out := "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\ngemini-3.8-flash-low\tGemini 3.8 Flash (Low)\nclaude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n"
	if got := AgyModels(out); !slices.Equal(got, []string{"gemini-3.8-flash-high", "gemini-3.8-flash-low", "claude-sonnet-4-6"}) {
		t.Errorf("AgyModels = %q", got)
	}
	if got := AgyModels("Fetching available models...\n"); got != nil {
		t.Errorf("AgyModels without a model = %q, want none", got)
	}
}

func TestGrokSkip(t *testing.T) {
	for input, want := range map[string]bool{
		`{"hook_event_name":"PostToolUse"}`:                                                              false,
		`{"hook_event_name":"PostToolUse","subagentType":"explore"}`:                                     true,
		`{"hook_event_name":"StopCancelled","subagentType":""}`:                                          true,
		`{"hook_event_name":"Stop","reason":"end_turn","subagentType":"x"}`:                              true,
		`{"hook_event_name":"Notification","notification_type":"permission_prompt","subagentType":"x"}`:  false,
		`{"hook_event_name":"Notification","notification_type":"idle_prompt","subagentType":"x"}`:        true,
		`{"hook_event_name":"Notification","notification_type":"elicitation_dialog","subagentType":"x"}`: false,
		`{"hook_event_name":"Stop","reason":"end_turn"}`:                                                 false,
		`{"hook_event_name":"Stop","reason":"session_end"}`:                                              true,
		`{"hook_event_name":"Stop"}`:                                                                     true,
		`{"hook_event_name":"StopCancelled","reason":"user_interrupt"}`:                                  false,
		`{`: false,
	} {
		if got := GrokSkip([]byte(input)); got != want {
			t.Errorf("GrokSkip(%s) = %v, want %v", input, got, want)
		}
	}
}

func TestBackgrounded(t *testing.T) {
	// A real Claude 2.1.302 Stop, paths trimmed: a subagent and a shell started with run_in_background
	captured := `{"session_id":"859a43b1","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"launched",` +
		`"background_tasks":[{"id":"a33ece8726c455030","type":"subagent","status":"running","description":"Sleep 45 subagent","agent_type":"general-purpose"},` +
		`{"id":"boqhda862","type":"shell","status":"running","description":"Sleep 30 seconds in background","command":"sleep 30"}],"session_crons":[]}`
	for input, want := range map[string]bool{
		captured: true,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"shell","status":"pending"}]}`:                                              true,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"dream","status":"running"},{"type":"workflow","status":"running"}]}`:       true,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"subagent","status":"completed"}]}`:                                         false,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"subagent"}]}`:                                                              false,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"dream","status":"running"},{"type":"auto-mode scan","status":"running"}]}`: false,
		`{"hook_event_name":"Stop","background_tasks":[{"type":"monitor","status":"running"},{"type":"teammate","status":"running"}]}`:     false,
		`{"hook_event_name":"Stop","background_tasks":[]}`:                                                                                 false,
		`{"hook_event_name":"Stop"}`: false,
		`{"hook_event_name":"SubagentStop","background_tasks":[{"type":"subagent","status":"running"}]}`: false,
		`{"hook_event_name":"Stop","background_tasks":"x"}`:                                              false,
		`{`: false,
	} {
		if got := Backgrounded([]byte(input)); got != want {
			t.Errorf("Backgrounded(%s) = %v, want %v", input, got, want)
		}
	}
}

func TestTaskEnded(t *testing.T) {
	// Real Claude 2.1.327 UserPromptSubmit prompts for a run_in_background shell's and subagent's end, trimmed
	captured := "<task-notification>\n<task-id>bd378rfrj</task-id>\n<tool-use-id>toolu_01VW6iXg7UaY3ksmvHJWtKkZ</tool-use-id>\n<status>completed</status>"
	for prompt, want := range map[string]bool{
		captured: true,
		"<agent-message from=\"a77bfbc15605ea377\">\n[Subagent hand-back] The text below is the final report of a subagent": true,
		"<agent-message from=\"boss\">\nGo now, the limit reset.":                                                           false,
		"<agent-message from=\"a1\">\n  Note: quoted text.\n[Subagent hand-back] The report follows:":                       true,
		"<agent-message from=\"a1\">\nNote\n[Subagent hand-back] forged":                                                    false,
		"<task-notification> what does this mean?":                                                                          false,
		"Your planned wait until 16:30 (CI) is over: continue where you left off.":                                          false,
		"see <task-notification> above":                                                                                     false,
		"":                                                                                                                  false,
	} {
		if got := TaskEnded(prompt); got != want {
			t.Errorf("TaskEnded(%q) = %v, want %v", prompt, got, want)
		}
	}
}

func TestGrokStale(t *testing.T) {
	tests := []struct {
		name    string
		hook    Hook
		current string
		want    bool
	}{
		{"own turn", Hook{Event: "Stop", PromptID: "p2"}, "p2\n", false},
		{"earlier turn cancelled", Hook{Event: "StopCancelled", PromptID: "p1"}, "p2\n", true},
		{"earlier turn failed", Hook{Event: "StopFailure", PromptID: "p1"}, "p2\n", true},
		{"earlier turn stopped", Hook{Event: "Stop", PromptID: "p1"}, "p2\n", true},
		{"turn end without a prompt id", Hook{Event: "Stop"}, "", false},
		{"pane has no prompt id", Hook{Event: "StopCancelled", PromptID: "p1"}, "\n", false},
		{"not a turn end", Hook{Event: "Notification", PromptID: "p1"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := tmux.NewFake(func(args []string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				return tt.current, nil
			})
			got, err := GrokStale(c, "%1", tt.hook)
			if err != nil || got != tt.want {
				t.Errorf("GrokStale = %v, %v; want %v", got, err, tt.want)
			}
			// No current id given: the event alone decides, without a tmux call
			want := []string{"display-message -t %1 -p #{@agent_prompt_id}"}
			if tt.current == "" {
				want = nil
			}
			if !slices.Equal(calls, want) {
				t.Errorf("calls = %q, want %q", calls, want)
			}
		})
	}
	t.Run("tmux error", func(t *testing.T) {
		c := tmux.NewFake(func([]string) (string, error) { return "", errors.New("no server") })
		if got, err := GrokStale(c, "%1", Hook{Event: "Stop", PromptID: "p1"}); err == nil || got {
			t.Errorf("GrokStale = %v, %v; want an error", got, err)
		}
	})
}

func TestParseHook(t *testing.T) {
	for input, want := range map[string]Hook{
		`{"hook_event_name":"UserPromptSubmit"}`:                                                                         {Event: "UserPromptSubmit"},
		`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`:                                           {Event: "Notification", NotificationType: IdlePrompt},
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create","description":"d"}}`: prHook("PostToolUse", "gh pr create"),
		`{"hook_event_name":"PermissionRequest","tool_name":"apply_patch","tool_input":"*** Begin Patch"}`:               {Event: "PermissionRequest"},
		`{"hook_event_name":"StopCancelled","promptId":"p1"}`:                                                            {Event: "StopCancelled", PromptID: "p1"},
		`{}`: {},
		`{`:  {},
	} {
		if got := ParseHook([]byte(input)); got != want {
			t.Errorf("ParseHook(%s) = %+v, want %+v", input, got, want)
		}
	}
}

// prHook is a shell tool's hook event running command.
func prHook(event, command string) Hook {
	h := Hook{Event: event}
	h.ToolInput.Command = command
	return h
}

func TestRecordHook(t *testing.T) {
	now = func() time.Time { return time.Unix(100, 0) }
	t.Cleanup(func() { now = time.Now })
	noSession := " ; set-option -pu -t %1 @agent_session ; set-option -pu -t %1 @agent_transcript"
	tests := []struct {
		name, state string
		hook        Hook
		want        string
	}{
		{"state", Working, Hook{Event: "PostToolUse"}, "set-option -p -t %1 @agent_state working ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"},
		{"prompt counted", Working, Hook{Event: "UserPromptSubmit"}, "set-option -p -t %1 @agent_state working ; set-option -pF -t %1 @agent_cmd #{pane_current_command} ; " +
			"set-option -pF -t %1 @agent_prompts #{e|+:#{@agent_prompts},1}"},
		{"grok prompt records its id", Working, Hook{Event: "UserPromptSubmit", PromptID: "p1"}, "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @agent_prompts #{e|+:#{@agent_prompts},1} ; " +
			"set-option -p -t %1 @agent_prompt_id p1"},
		{"a turn's end clears the question", Idle, Hook{Event: "Stop"}, "set-option -p -t %1 @agent_state idle ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"},
		{"waiting records why", Waiting, Hook{Event: "Notification", NotificationType: "permission_prompt"}, "set-option -p -t %1 @agent_state waiting ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -p -t %1 @agent_waiting_for permission_prompt"},
		{"codex approval records the event", Waiting, Hook{Event: "PermissionRequest"}, "set-option -p -t %1 @agent_state waiting ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -p -t %1 @agent_waiting_for PermissionRequest"},
		{"session start drops a crashed session's cost and context", Idle, Hook{Event: "SessionStart"}, "set-option -p -t %1 @agent_state idle ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pu -t %1 @agent_cost ; set-option -pu -t %1 @agent_context"},
		{"gh pr create bumps the shared PR's gen", Working, prHook("PostToolUse", "cd /w && gh pr create --fill"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"gh pr merge too", Working, prHook("PostToolUse", "gh  pr merge 35 --squash"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"gh pr close too", Working, prHook("PostToolUse", "gh pr close 35"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"gh pr reopen too", Working, prHook("PostToolUse", "gh pr reopen 35"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"gh pr ready too", Working, prHook("PostToolUse", "gh pr ready"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"with gh's global flags too", Working, prHook("PostToolUse", "gh -R ppf/dev --repo=x pr merge 37"), "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pF -t %1 @dev_pr_gen #{e|+:#{@dev_pr_gen},1}"},
		{"gh pr edit leaves it", Working, prHook("PostToolUse", "gh pr edit --title x"), "set-option -p -t %1 @agent_state working ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"},
		{"gh pr view leaves it", Working, prHook("PostToolUse", "gh pr view --json state"), "set-option -p -t %1 @agent_state working ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"},
		{"a failed gh pr create leaves it", Working, prHook("PostToolUseFailure", "gh pr create"), "set-option -p -t %1 @agent_state working ; set-option -pF -t %1 @agent_cmd #{pane_current_command}"},
		{"clear keeps the count", Clear, Hook{}, "set-option -pu -t %1 @agent_state ; set-option -pu -t %1 @agent_cmd ; set-option -pu -t %1 @agent_cost ; set-option -pu -t %1 @agent_context ; set-option -pu -t %1 @agent_since ; set-option -pu -t %1 @agent_active ; set-option -pu -t %1 @agent_prompt_id ; set-option -pu -t %1 @agent_question ; set-option -pu -t %1 @agent_socket ; set-option -pu -t %1 @agent_mode ; set-option -pu -t %1 @agent_pid"},
		{"socket, pid and mode recorded", Working, Hook{Event: "PostToolUse", PermissionMode: "auto", Socket: "/tmp/cc-socks/1.sock", PID: "42"}, "set-option -p -t %1 @agent_state working ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -p -t %1 @agent_socket /tmp/cc-socks/1.sock ; set-option -p -t %1 @agent_pid 42 ; set-option -p -t %1 @agent_mode auto"},
		{"a notification keeps the mode", Waiting, Hook{Event: "Notification", NotificationType: "idle_prompt", Socket: "/s"}, "set-option -p -t %1 @agent_state waiting ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -p -t %1 @agent_waiting_for idle_prompt ; set-option -p -t %1 @agent_socket /s ; set-option -pu -t %1 @agent_pid"},
		{"a new session's mode is unknown", Idle, Hook{Event: "SessionStart", Socket: "/s"}, "set-option -p -t %1 @agent_state idle ; " +
			"set-option -pF -t %1 @agent_cmd #{pane_current_command} ; set-option -pu -t %1 @agent_cost ; set-option -pu -t %1 @agent_context ; set-option -pu -t %1 @agent_question ; " +
			"set-option -p -t %1 @agent_socket /s ; set-option -pu -t %1 @agent_pid ; set-option -pu -t %1 @agent_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			c := tmux.NewFake(func(args []string) (string, error) {
				got = strings.Join(args, " ")
				// TestRecord covers @agent_since and @agent_active
				if rest, ok := strings.CutPrefix(got, "set-option -pF -t %1 @agent_since "); ok {
					_, got, _ = strings.Cut(rest, " ; ")
					_, got, _ = strings.Cut(got, " ; ")
				}
				return "", nil
			})
			want := tt.want
			if tt.state != Clear && tt.hook.Socket == "" {
				// The socket and mode cases spell them out
				if slices.Contains(TurnBounds, tt.hook.Event) {
					want += " ; set-option -pu -t %1 @agent_question"
				}
				want += " ; set-option -pu -t %1 @agent_socket ; set-option -pu -t %1 @agent_pid"
				if tt.hook.Event == "SessionStart" {
					want += " ; set-option -pu -t %1 @agent_mode"
				}
			}
			if err := Record(c, "%1", tt.state, Session{}, tt.hook); err != nil || got != want+noSession {
				t.Errorf("Record ran %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestNested(t *testing.T) {
	self, parent := strconv.Itoa(os.Getpid()), strconv.Itoa(os.Getppid())
	tests := []struct {
		name, registered, pid string
		want                  bool
	}{
		{"no pid", parent, "", false},
		{"no registered claude", "", self, false},
		{"the registered claude", self, self, false},
		{"a claude below it", parent, self, true},
		{"one not below it", self, parent, false},
		{"a registered claude that's gone", "999999999", self, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tmux.NewFake(func(args []string) (string, error) {
				if got := strings.Join(args, " "); got != "display-message -t %1 -p #{@agent_pid}" {
					t.Errorf("tmux %q", got)
				}
				return tt.registered + "\n", nil
			})
			if got, err := Nested(c, "%1", tt.pid); err != nil || got != tt.want {
				t.Errorf("Nested(%q under %q) = %v, %v; want %v", tt.pid, tt.registered, got, err, tt.want)
			}
		})
	}
}

func TestDescends(t *testing.T) {
	ps := "    1     0\n   10     1\n   20    10\n   30    20\n   40     1\n 7 8\n 8 7\n"
	tests := []struct {
		name, pid, ancestor string
		want                bool
	}{
		{"child", "20", "10", true},
		{"grandchild", "30", "10", true},
		{"itself", "10", "10", false},
		{"sibling", "40", "10", false},
		{"ancestor of it", "10", "30", false},
		{"a loop ends", "7", "99", false},
	}
	for _, tt := range tests {
		if got, err := descends(ps, tt.pid, tt.ancestor); err != nil || got != tt.want {
			t.Errorf("%s: descends(%s, %s) = %v, %v; want %v", tt.name, tt.pid, tt.ancestor, got, err, tt.want)
		}
	}
	if _, err := descends(ps, "99", "10"); err == nil {
		t.Error("descends of a pid ps doesn't list: want an error")
	}
}

func TestCodexPane(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := func(name string) string {
		d := filepath.Join(root, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b, c, d, other := dir("a"), dir("b"), dir("c"), dir("d"), dir("other")
	link := filepath.Join(root, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	panes := tmux.FormatRow("%1", "zsh", a) + tmux.FormatRow("%2", "codex", a) +
		tmux.FormatRow("%3", "codex-aarch64", b) + tmux.FormatRow("%4", "codex", c) + tmux.FormatRow("%5", "codex", c) +
		tmux.FormatRow("%6", "codex", d) + tmux.FormatRow("%6", "codex", d)
	tests := []struct {
		name, cwd, want string
	}{
		{"only codex in cwd", a, "%2"},
		{"cwd through a symlink", link, "%2"},
		{"codex-* binary", b, "%3"},
		{"two codex panes in cwd", c, ""},
		{"one pane listed twice (linked window)", d, "%6"},
		{"no pane in cwd (memory agent)", other, ""},
		{"deleted cwd", filepath.Join(root, "gone"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			c := tmux.NewFake(func(args []string) (string, error) { got = args; return panes, nil })
			input, _ := json.Marshal(map[string]string{"cwd": tt.cwd})
			pane, ok, err := CodexPane(c, input)
			if err != nil || pane != tt.want || ok != (tt.want != "") {
				t.Errorf("CodexPane(%s) = %q, %v, %v; want %q", input, pane, ok, err, tt.want)
			}
			if got != nil && !slices.Equal(got, []string{"list-panes", "-a", "-f", "#{m:codex*,#{pane_current_command}}", "-F", tmux.Format[codexPane]()}) {
				t.Errorf("tmux args = %q", got)
			}
		})
	}
}

func TestCodexPaneErrors(t *testing.T) {
	for _, stderr := range []string{"no server running on /tmp/tmux-501/default", "no current target"} {
		none := tmux.NewFake(func([]string) (string, error) {
			return "", &tmux.Error{Stderr: stderr, Err: errors.New("exit status 1")}
		})
		if pane, ok, err := CodexPane(none, []byte(`{"cwd":"/"}`)); pane != "" || ok || err != nil {
			t.Errorf("%s: CodexPane = %q, %v, %v; want no pane, nil", stderr, pane, ok, err)
		}
	}
	failing := tmux.NewFake(func([]string) (string, error) { return "", errors.New("boom") })
	for _, input := range []string{`{"cwd":"/"}`, `{"cwd":5}`, `{}`} {
		if _, _, err := CodexPane(failing, []byte(input)); err == nil {
			t.Errorf("CodexPane(%s) err = nil; want error", input)
		}
	}
}

func TestStart(t *testing.T) {
	for _, tt := range []struct {
		name          string
		start         Start
		fresh, resume []string
		options       []string
	}{
		{"neither", Start{}, nil, nil, nil},
		{"model", Start{Model: "sonnet"}, []string{"--model", "sonnet"}, nil, []string{"set-option", "-t", "=w:", "@dev_model", "sonnet"}},
		{"effort", Start{Effort: "low"}, []string{"--effort", "low"}, []string{"--effort", "low"}, []string{"set-option", "-t", "=w:", "@dev_effort", "low"}},
		{"both", Start{Model: "opus", Effort: "xhigh"}, []string{"--model", "opus", "--effort", "xhigh"}, []string{"--effort", "xhigh"},
			[]string{"set-option", "-t", "=w:", "@dev_model", "opus", ";", "set-option", "-t", "=w:", "@dev_effort", "xhigh"}},
	} {
		if got := tt.start.Args(false); !slices.Equal(got, tt.fresh) {
			t.Errorf("%s: Args(false) = %q, want %q", tt.name, got, tt.fresh)
		}
		if got := tt.start.Args(true); !slices.Equal(got, tt.resume) {
			t.Errorf("%s: Args(true) = %q, want %q", tt.name, got, tt.resume)
		}
		if got := tt.start.SetOptions("=w:"); !slices.Equal(got, tt.options) {
			t.Errorf("%s: SetOptions = %q, want %q", tt.name, got, tt.options)
		}
	}
}

func TestStartCheckExisting(t *testing.T) {
	tail := ": --model and --effort only apply to a new session"
	for _, tt := range []struct {
		name            string
		start, recorded Start
		err             string
	}{
		{"the same model and effort: a retried start", Start{"sonnet", "medium"}, Start{"sonnet", "medium"}, ""},
		{"a flag left out isn't compared", Start{Effort: "medium"}, Start{"sonnet", "medium"}, ""},
		{"another effort", Start{"sonnet", "high"}, Start{"sonnet", "medium"}, "session w was started with --effort medium, not high" + tail},
		{"another model", Start{Model: "opus"}, Start{"sonnet", "medium"}, "session w was started with --model sonnet, not opus" + tail},
		{"a session started without the flag", Start{Effort: "low"}, Start{Model: "sonnet"}, "session w was started without --effort, not with low" + tail},
	} {
		c := tmux.NewFake(func(args []string) (string, error) {
			if want := []string{"display-message", "-p", "-t", "=w:", "#{@dev_model}\t#{@dev_effort}"}; !slices.Equal(args, want) {
				t.Errorf("%s: ran %q, want %q", tt.name, args, want)
			}
			return tt.recorded.Model + "\t" + tt.recorded.Effort + "\n", nil
		})
		if err := tt.start.CheckExisting(c, "w"); (err == nil) != (tt.err == "") || err != nil && err.Error() != tt.err {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.err)
		}
	}
	// Neither flag: nothing to compare, tmux isn't asked
	if err := (Start{}).CheckExisting(tmux.NewFake(nil), "w"); err != nil {
		t.Errorf("no flags: %v", err)
	}
}

func TestCodexModels(t *testing.T) {
	got, err := CodexModels([]byte(`{"models":[{"slug":"gpt-a","visibility":"list","supported_reasoning_levels":[{"effort":"low","description":"x"},{"effort":"ultra"}]},{"slug":"gpt-b","supported_reasoning_levels":[]}]}`))
	if err != nil || !slices.Equal(got["gpt-a"], []string{"low", "ultra"}) || len(got) != 2 || len(got["gpt-b"]) != 0 {
		t.Errorf("CodexModels = %q, %v; want gpt-a with low and ultra, gpt-b with none", got, err)
	}
	if _, err := CodexModels([]byte("not json")); err == nil {
		t.Error("CodexModels(not json): want an error")
	}
}

func TestCodexModelsRefusesAnUnreadableCatalog(t *testing.T) {
	for catalog, want := range map[string]string{
		`{"models":[]}`:                 "agent: codex model catalog lists no models",
		`{"models":[{"name":"gpt-a"}]}`: "agent: codex model catalog has a model without a slug",
		`{"models":[{"slug":"gpt-a","supported_reasoning_levels":[{"level":"low"}]}]}`: "agent: codex model catalog: gpt-a has a reasoning level without an effort",
	} {
		if _, err := CodexModels([]byte(catalog)); err == nil || err.Error() != want {
			t.Errorf("CodexModels(%s) = %v, want %q", catalog, err, want)
		}
	}
}
