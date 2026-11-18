package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// todoEnv runs dev todo as the desktop app session local_boss, whose worker is local_w (desktopHome).
func todoEnv(t *testing.T) {
	t.Helper()
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("TMUX_PANE", "")
	old := viewPR
	viewPR = func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }
	t.Cleanup(func() { viewPR = old })
}

func run(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, _, err := execute(t, stdin, args...)
	if err != nil {
		t.Fatalf("dev %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// set, add, done and clear say what's left; the list prints as rows and as the JSON the boards read.
func TestTodo(t *testing.T) {
	todoEnv(t)
	for _, tt := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"todo", "set", "Answer Vince", "Connect VPN"}, "to do 2\n"},
		{"", []string{"todo", "add", "Staging access", "--on", "Marek"}, "added 3 · to do 3, 1 on others\n"},
		{"", []string{"todo", "add", "Merge PR #87", "--link", "https://github.com/o/r/pull/87"}, "added 4 · to do 4, 1 on others\n"},
		{"", []string{"todo", "done", "2", "2"}, "done 2 2 · to do 3, 1 on others\n"},
		{"", []string{"todo"}, "1\t☐\tAnswer Vince\n2\t☑\tConnect VPN\n4\t☐\tMerge PR #87\thttps://github.com/o/r/pull/87\n3\t◌\tStaging access\ton Marek\n"},
	} {
		if got := run(t, tt.stdin, tt.args...); got != tt.want {
			t.Errorf("dev %s = %q, want %q", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	var l struct {
		Items []struct {
			ID   int    `json:"id"`
			Text string `json:"text"`
			On   string `json:"on"`
			Link string `json:"link"`
			Done bool   `json:"done"`
		} `json:"items"`
		Updated int64 `json:"updated"`
		Stale   bool  `json:"stale"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 4 || !l.Items[1].Done || l.Items[3].On != "Marek" || l.Items[2].Link == "" || l.Updated == 0 || l.Stale {
		t.Errorf("json = %+v", l)
	}
	if got := run(t, "", "todo", "clear"); got != "to do empty\n" {
		t.Errorf("clear = %q", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"items": []`) {
		t.Errorf("empty list's json = %q, want items []", got)
	}
}

// set without arguments reads stdin, one item per line, skipping blank lines.
func TestTodoSetStdin(t *testing.T) {
	todoEnv(t)
	run(t, "Answer Vince\n\n  Connect VPN  \n", "todo", "set")
	if got := run(t, "", "todo"); got != "1\t☐\tAnswer Vince\n2\t☐\tConnect VPN\n" {
		t.Errorf("list = %q", got)
	}
}

// A desktop worker's own session has no list, --boss local_… names its boss's; an unknown session fails.
func TestTodoBossFlag(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	if _, _, err := execute(t, "", "todo", "add", "x"); err == nil || !strings.Contains(err.Error(), "local_w is a worker") {
		t.Errorf("err = %v, want local_w refused as a worker", err)
	}
	run(t, "", "todo", "--boss", "local_boss", "set", "x")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	if got := run(t, "", "todo"); got != "1\t☐\tx\n" {
		t.Errorf("local_boss's list = %q", got)
	}
	if _, _, err := execute(t, "", "todo", "--boss", "local_typo"); err == nil || !strings.Contains(err.Error(), "no app session record for local_typo") {
		t.Errorf("err = %v, want no record", err)
	}
}

// Outside a boss session, without --boss, it fails rather than guess whose list it is.
func TestTodoNoBoss(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "")
	if _, _, err := execute(t, "", "todo", "add", "x"); err == nil || !strings.Contains(err.Error(), "no boss session") {
		t.Errorf("err = %v, want no boss session", err)
	}
}

// Bad numbers, empty items and an empty set are refused, leaving the list as it was.
func TestTodoErrors(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a")
	for _, args := range [][]string{{"todo", "done", "2"}, {"todo", "done", "x"}, {"todo", "add", " "}, {"todo", "set"}, {"todo", "set", "a", " "}, {"todo", "add", "\n\x1b"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n" {
		t.Errorf("list after refused calls = %q, want it unchanged", got)
	}
}

// add --until shows its time in the rows and the JSON; add --replaces takes an open item's number and place.
func TestTodoUntilReplaces(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a", "b")
	if got := run(t, "", "todo", "add", "Meeting", "--until", "2099-01-02 10:00"); got != "added 3 · to do 3\n" {
		t.Errorf("add --until = %q", got)
	}
	if got := run(t, "", "todo", "add", "b2", "--replaces", "2", "--on", "Marek"); got != "replaced 2 · to do 3, 1 on others\n" {
		t.Errorf("add --replaces = %q", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n3\t☐\tMeeting\tuntil 2099-01-02 10:00\n2\t◌\tb2\ton Marek\n" {
		t.Errorf("list = %q", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"until": `+jsonInt(time.Date(2099, 1, 2, 10, 0, 0, 0, time.Local).Unix())) {
		t.Errorf("json = %s, want until", got)
	}
	for _, args := range [][]string{{"todo", "add", "x", "--until", "yesterday"}, {"todo", "add", "x", "--until", "2000-01-01"}, {"todo", "add", "x", "--replaces", "9"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo", "add", "c"); got != "added 4 · to do 4, 1 on others\n" {
		t.Errorf("refused adds changed the list: %q", got)
	}
}

// In a desktop app boss, a prompt bringing its workers' notes gets its open list as context; a worker, another
// prompt or an empty list gets nothing.
func TestStateSideSessionsTodo(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	event := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"<system-reminder>…</system-reminder>\n<side-sessions-event>W finished</side-sessions-event>"}`
	if got := run(t, event, "state", "working"); got != "" {
		t.Errorf("empty list: %q, want nothing", got)
	}
	run(t, "", "todo", "set", "Merge PR #9")
	got := run(t, event, "state", "working")
	var out struct {
		Hook struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil || out.Hook.Event != "UserPromptSubmit" || !strings.Contains(out.Hook.Context, "1. Merge PR #9") {
		t.Errorf("event prompt: %q (%v), want the list as context", got, err)
	}
	if got := run(t, `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`, "state", "working"); got != "" {
		t.Errorf("plain prompt: %q, want nothing", got)
	}
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	if got, stderr, err := execute(t, event, "state", "working"); got != "" || stderr != "" || err != nil {
		t.Errorf("worker: %q, %q, %v; want nothing", got, stderr, err)
	}
	// A session without an app record reports on stderr; stdout, which Claude reads as JSON, stays empty
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_typo")
	if got, stderr, err := execute(t, event, "state", "working"); got != "" || !strings.HasPrefix(stderr, "❌ dev state: todo: no app session record") || err != nil {
		t.Errorf("no record: %q, %q, %v; want only a stderr report", got, stderr, err)
	}
}

// A desktop boss without a list takes over a stopped session's list on its first dev todo, not at start or on
// --json; a running session's list stays. Resume and /clear bring its list; startup and compaction nothing.
func TestTodoDesktopTakeOver(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	home := os.Getenv("HOME")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	org := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	live := filepath.Join(home, ".claude", "sessions")
	for i, id := range []string{"local_boss", "local_old", "local_running", "local_gone"} {
		pid := os.Getpid()
		if id == "local_old" || id == "local_gone" {
			pid = 1 << 30 // no running process
		}
		for path, v := range map[string]any{
			filepath.Join(org, id+".json"):               map[string]any{"sessionId": id, "originCwd": "/p", "createdAt": 1},
			filepath.Join(live, strconv.Itoa(i)+".json"): map[string]any{"pid": pid, "sessionId": "cli-" + id, "hostSessionId": id, "status": "idle"},
		} {
			data, _ := json.Marshal(v)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run(t, "", "todo", "--boss", "local_running", "set", "running's own")
	run(t, "", "todo", "--boss", "local_old", "set", "old open item")
	// A session whose own Claude isn't among the running ones takes over nothing, and says so
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_gone")
	if got, stderr, err := execute(t, "", "todo"); got != "" || err != nil || !strings.Contains(stderr, "no earlier list taken over") {
		t.Errorf("stopped session: %q, %q, %v; want nothing and a stderr note", got, stderr, err)
	}
	// set and clear replace the list: they take over nothing
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	run(t, "", "todo", "set", "plan")
	run(t, "", "todo", "clear")
	if got := run(t, "", "todo", "--boss", "local_old"); got != "1\t☐\told open item\n" {
		t.Errorf("local_old's list after set and clear elsewhere: %q, want it kept", got)
	}
	if err := os.Remove(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "desktop-local_boss.json")); err != nil {
		t.Fatal(err)
	}
	start := func(source string) string {
		return run(t, `{"hook_event_name":"SessionStart","source":"`+source+`","session_id":"s1"}`, "state", "idle")
	}
	if got := start("startup"); got != "" {
		t.Errorf("start without a list: %q, want nothing", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"items": []`) {
		t.Errorf("--json read: %q, want an empty list (no take-over)", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\told open item\n" {
		t.Errorf("first dev todo: %q, want local_old's list", got)
	}
	if got := run(t, "", "todo", "--boss", "local_old"); got != "" {
		t.Errorf("local_old after the take-over: %q, want empty", got)
	}
	if got := run(t, "", "todo", "--boss", "local_running"); got != "1\t☐\trunning's own\n" {
		t.Errorf("running session's list: %q, want it kept", got)
	}
	for source, want := range map[string]string{"startup": "", "resume": "(updated just now):\n1. old open item", "clear": "(updated just now):\n1. old open item", "compact": ""} {
		got := start(source)
		var out struct {
			Hook struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if want == "" {
			if got != "" {
				t.Errorf("%s: %q, want nothing", source, got)
			}
			continue
		}
		if err := json.Unmarshal([]byte(got), &out); err != nil || out.Hook.Event != "SessionStart" || !strings.Contains(out.Hook.Context, want) {
			t.Errorf("%s: %q (%v), want context with %q", source, got, err, want)
		}
	}
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_w")
	if got, stderr, err := execute(t, `{"hook_event_name":"SessionStart","source":"startup"}`, "state", "idle"); got != "" || stderr != "" || err != nil {
		t.Errorf("worker start: %q, %q, %v; want nothing", got, stderr, err)
	}
}

// --on a worker of the boss, by its local_… id or its name, links its session, shown under its name; --on
// anyone else is a person; --on me gives an item back to the user.
func TestTodoOnWorker(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "add", "By id", "--on", "local_w")
	run(t, "", "todo", "add", "By name", "--on", " w ")
	if _, stderr, err := execute(t, "", "todo", "add", "A person", "--on", "Marek"); err != nil || !strings.Contains(stderr, "on Marek: no single session this boss started (worker or boss) by that name or id, so a person\n") {
		t.Errorf("--on a person: stderr %q, %v; want a note", stderr, err)
	}
	run(t, "", "todo", "add", "Handed over", "--on", "Ola")
	run(t, "", "todo", "add", "Handed over", "--replaces", "4", "--on", "local_w")
	if got := run(t, "", "todo", "add", "Back to me", "--replaces", "3", "--on", "ME"); got != "replaced 3 · to do 4, 3 on others\n" {
		t.Errorf("--on me: %q", got)
	}
	got := run(t, "", "todo", "--json")
	for _, want := range []string{
		`"text": "By id",
      "on": "W",
      "session": {
        "id": "local_w",
        "name": "W"
      }`,
		`"text": "By name",
      "on": "W",
      "session": {`,
		`"text": "Back to me",
      "done": false`,
		`"text": "Handed over",
      "on": "W",
      "session": {
        "id": "local_w",`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--json = %s\nwant it to hold %s", got, want)
		}
	}
	// Its record gone (archived), the worker's items print as session gone
	home := os.Getenv("HOME")
	if err := os.Remove(filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org", "local_w.json")); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "", "todo"); !strings.Contains(got, "1\t◌\tBy id\ton W, session gone\n") {
		t.Errorf("dev todo = %q, want By id on W, session gone", got)
	}
}

// --replaces may leave out the text to only change who does it; an add without text is refused. A worker whose
// record can't be read is never called gone.
func TestTodoReplacesWithoutText(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "add", "Rework companion", "--on", "Marek")
	run(t, "", "todo", "add", "--replaces", "1", "--on", "local_w")
	run(t, "", "todo", "add", "Unreadable worker", "--on", "local_broken")
	if _, _, err := execute(t, "", "todo", "add", "--on", "Marek"); err == nil || err.Error() != "no item: only --replaces keeps the item's text" {
		t.Errorf("add without text: %v", err)
	}
	if got := run(t, "", "todo"); got != "1\t◌\tRework companion\ton W\n2\t◌\tUnreadable worker\ton local_broken\n" {
		t.Errorf("dev todo = %q", got)
	}
}

// ask puts a decision with its options on the list: the rows, the JSON and the open list name them.
func TestTodoAsk(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a")
	if got := run(t, "", "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", " Wait ", "--link", "https://github.com/o/r/pull/148"); got != "asked 2 · to do 2\n" {
		t.Errorf("ask = %q", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n2\t☐\tMerge #148?\thttps://github.com/o/r/pull/148\toptions: Merge (Recommended) · Wait\n" {
		t.Errorf("rows = %q", got)
	}
	var l struct {
		Items []struct {
			Options []string `json:"options"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil || len(l.Items) != 2 || l.Items[0].Options != nil || strings.Join(l.Items[1].Options, "|") != "Merge (Recommended)|Wait" {
		t.Errorf("json = %+v, %v", l, err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"todo", "ask", "Merge?"}, "no --option"},
		{[]string{"todo", "ask", " ", "--option", "a"}, "empty question"},
		{[]string{"todo", "ask", "Merge?", "--option", "a", "--option", " (Recommended)"}, "option 2 is empty"},
		{[]string{"todo", "ask", "Merge?", "--option", "Merge", "--option", "merge (Recommended)"}, `option 2 repeats "merge"`},
		{append([]string{"todo", "ask", "Pick?"}, strings.Split(strings.Repeat("--option|x|", 10), "|")[:20]...), "at most 9"},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

// answer refuses a Claude Code session, a desktop app boss and --boss before reaching tmux, and --via but from the
// manager's pane; --all refuses --boss.
func TestTodoAnswerRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		claude string
		args   []string
		want   string
	}{
		{"1", []string{"todo", "answer", "boss-dev", "1", "Merge"}, "refused in a Claude Code session"},
		{"", []string{"todo", "answer", "local_boss", "1", "Merge"}, "has no dev inbox"},
		{"", []string{"todo", "answer", "boss-dev", "1", "Merge", "--boss", "boss-x"}, "leave out --boss"},
		{"", []string{"todo", "answer", "boss-dev", "x", "Merge"}, `item number "x"`},
		{"1", []string{"todo", "answer", "boss-dev", "1", "Merge", "--from-board"}, "--from-board needs --question"},
		{"1", []string{"todo", "answer", "local_boss", "1", "Merge", "--from-board", "--question", "Merge?"}, "has no dev inbox"},
		{"", []string{"todo", "--all", "--boss", "boss-x"}, "leave out --boss"},
		{"1", []string{"todo", "answer", "boss-dev", "1", "Merge", "--via", "boss-x"}, "only --via manager"},
		{"1", []string{"todo", "answer", "boss-dev", "1", "Merge", "--via", "manager"}, "only in the manager's coordinator pane"},
	} {
		t.Setenv("CLAUDECODE", tt.claude)
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

// A pick relays only for the one question it answered, naming the boss, the item and its text, and not when the
// user left the item open.
func TestPickFor(t *testing.T) {
	pick := func(q, a string) *inbox.Prompt { return &inbox.Prompt{Pick: true, Question: q, Answer: a} }
	for _, tt := range []struct {
		name     string
		pick     *inbox.Prompt
		question string
		want     string
	}{
		{"its item", pick("dev #7: Merge #148? (CI green)", "Merge"), "Merge #148?", ""},
		{"boss name", pick("boss-dev #7: Merge #148?", "Merge"), "Merge #148?", ""},
		{"no --question", pick("dev #7: Merge #148?", "Merge"), "", "needs --question"},
		{"several questions", pick("", ""), "Merge #148?", "wasn't one question"},
		{"another item number", pick("dev #6: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"another boss", pick("terminal #7: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"another text", pick("dev #7: Ship?", "Ship"), "Merge #148?", "not boss-dev #7"},
		{"number prefix", pick("dev #70: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"project suffix", pick("api-dev #7: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"left open", pick("dev #7: Merge #148?", "leave it open"), "Merge #148?", "the item stays open"},
	} {
		err := pickFor(tt.pick, "boss-dev", 7, tt.question)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
}

// dev todo's line ends with why to re-check the item.
func TestTodoLineCheck(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	it := todo.Shown{ID: 2, Text: "PR #412 open", On: "reviewers", Link: "https://github.com/acme/orbit/pull/412",
		Check: &sidebar.Check{Kind: sidebar.CheckMerged, PR: 412, Since: now.Add(-40 * time.Minute).Unix()}}
	if got, want := todoLine(it, now), "2\t◌\tPR #412 open\ton reviewers\thttps://github.com/acme/orbit/pull/412\t⚠ PR #412 merged 40m ago"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// add --done-on needs a GitHub PR link; dev todo check ticks the item once its PR is so, and a desktop boss's next
// reminder tells it once.
func TestTodoDoneOn(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	url := "https://github.com/acme/orbit/pull/415"
	for _, args := range [][]string{{"todo", "add", "x", "--done-on", "merged"}, {"todo", "add", "x", "--link", url, "--done-on", "deployed"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	run(t, "", "todo", "add", "Second review of #415", "--on", "Ana", "--link", url, "--done-on", "approved")
	run(t, "", "todo", "add", "Keep")
	if got := run(t, "", "todo"); got != "2\t☐\tKeep\n1\t◌\tSecond review of #415\ton Ana\t"+url+"\tdone on approved\n" {
		t.Errorf("list = %q", got)
	}
	viewPR = func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN", Review: "APPROVED"}, nil }
	if got := run(t, "", "todo", "check"); !strings.Contains(got, "1\t☑\tSecond review of #415") {
		t.Errorf("check = %q, want #1 ticked", got)
	}
	event := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"<side-sessions-event>W finished</side-sessions-event>"}`
	want := `#1 \"Second review of #415\" ticked by itself: PR #415 approved (--done-on approved)`
	if got := run(t, event, "state", "working"); !strings.Contains(got, want) {
		t.Errorf("reminder = %s, want %s", got, want)
	}
	if got := run(t, event, "state", "working"); strings.Contains(got, "ticked by itself") {
		t.Errorf("second reminder = %s, want the tick told once", got)
	}
}
