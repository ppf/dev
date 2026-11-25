package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// todoEnv runs dev todo as the desktop app session local_boss, whose worker is local_w (desktopHome).
func todoEnv(t *testing.T) {
	t.Helper()
	desktopHome(t)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("TMUX_PANE", "")
	old, oldTmux := viewPR, takeOverTmux
	viewPR = func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }
	// A desktop app boss's empty list looks for one to take over (desktopHint): never on the live tmux server
	takeOverTmux = func() *tmux.Client {
		return tmux.NewFake(func(args []string) (string, error) {
			if args[0] == "display-message" {
				return "/tmp/tmux-1/default\n", nil
			}
			return "", nil
		})
	}
	t.Cleanup(func() { viewPR, takeOverTmux = old, oldTmux })
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
		{"", []string{"todo", "add", "--no-details", "Merge PR #87", "--link", "https://github.com/o/r/pull/87"}, "added 4 · to do 4, 1 on others\n"},
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
			// Marek is no worker: a person
			Status   string `json:"status"`
			StatusOn string `json:"status_on"`
		} `json:"items"`
		Updated int64 `json:"updated"`
		Stale   bool  `json:"stale"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 4 || !l.Items[1].Done || l.Items[3].On != "Marek" || l.Items[2].Link == "" || l.Updated == 0 || l.Stale ||
		l.Items[0].Status != "yours" || l.Items[1].Status != "" || l.Items[2].Status != "yours" || l.Items[3].Status != "waiting" || l.Items[3].StatusOn != "Marek" {
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
	if _, _, err := execute(t, "", "todo", "add", "--no-details", "x"); err == nil || !strings.Contains(err.Error(), "local_w is a worker") {
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
	if _, _, err := execute(t, "", "todo", "add", "--no-details", "x"); err == nil || !strings.Contains(err.Error(), "no boss session") {
		t.Errorf("err = %v, want no boss session", err)
	}
}

// Bad numbers, empty items and an empty set are refused, leaving the list as it was.
func TestTodoErrors(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "a")
	for _, args := range [][]string{{"todo", "done", "2"}, {"todo", "done", "x"}, {"todo", "add", "--no-details", " "}, {"todo", "set"}, {"todo", "set", "a", " "}, {"todo", "add", "\n\x1b"}} {
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
	if got := run(t, "", "todo", "add", "--no-details", "Meeting", "--until", "2099-01-02 10:00"); got != "added 3 · to do 3\n" {
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
	for _, args := range [][]string{{"todo", "add", "--no-details", "x", "--until", "yesterday"}, {"todo", "add", "x", "--until", "2000-01-01"}, {"todo", "add", "x", "--replaces", "9"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo", "add", "--no-details", "c"); got != "added 4 · to do 4, 1 on others\n" {
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

// desktopSessions writes app records and live session files: local_boss and local_running run, local_old and local_gone
// don't, all in project /p.
func desktopSessions(t *testing.T) {
	t.Helper()
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
}

// A desktop boss without a list takes over a stopped session's list on its first dev todo, not at start or on
// --json; a running session's list stays. Resume and /clear bring its list; startup and compaction nothing.
func TestTodoDesktopTakeOver(t *testing.T) {
	todoEnv(t)
	desktopSessions(t)
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
	if _, _, err := execute(t, "", "todo", "add", "x", "--waiting-for", "me"); err == nil {
		t.Error("add --waiting-for me: not refused")
	}
	// Refused by the list it would take over: nothing taken over
	if _, _, err := execute(t, "", "todo", "add", "--no-details", "x", "--part-of", "9"); err == nil || !strings.Contains(err.Error(), "no open item 9") {
		t.Errorf("add --part-of 9: %v, want refused by local_old's list", err)
	}
	if _, _, err := execute(t, "", "todo", "add", "--replaces", "9", "--on", "me"); err == nil || !strings.Contains(err.Error(), "no open item 9") {
		t.Errorf("add --replaces 9: %v, want refused by local_old's list", err)
	}
	if _, _, err := execute(t, "", "todo", "add", "bare"); err == nil || !strings.Contains(err.Error(), "needs --details") {
		t.Errorf("add without details: %v, want refused", err)
	}
	if got := run(t, "", "todo", "--boss", "local_old"); got != "1\t☐\told open item\n" {
		t.Errorf("local_old after a refused add: %q, want it not taken over", got)
	}
	// One its trial accepts is written once, on the list taken over
	run(t, "", "todo", "add", "--no-details", "new")
	if got := run(t, "", "todo"); got != "1\t☐\told open item\n2\t☐\tnew\n" {
		t.Errorf("first dev todo: %q, want local_old's list and the new item once", got)
	}
	if got := run(t, "", "todo", "--boss", "local_old"); got != "" {
		t.Errorf("local_old after the take-over: %q, want empty", got)
	}
	if got := run(t, "", "todo", "--boss", "local_running"); got != "1\t☐\trunning's own\n" {
		t.Errorf("running session's list: %q, want it kept", got)
	}
	for source, want := range map[string]string{"startup": "", "resume": "(updated just now):\n1. old open item", "clear": "(updated just now):\n1. old open item", "compact": "(updated just now):\n1. old open item"} {
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

// On a desktop app boss's list: --on boss, case aside, is the boss's own task, not on others; --waiting-for a person
// is a person's item, refused beside --on or for me, boss or a worker; note state replaces the list's earlier one.
func TestTodoOnBossWaitingFor(t *testing.T) {
	todoEnv(t)
	out, stderr, err := execute(t, "", "todo", "add", "Parked idea", "--on", "Boss")
	if err != nil || out != "added 1 · to do 1\n" || stderr != "on boss: your own task, to do until you work on it\n" {
		t.Errorf("--on Boss: %q, %q, %v", out, stderr, err)
	}
	out, stderr, err = execute(t, "", "todo", "add", "Review", "--waiting-for", " Ola ")
	if err != nil || out != "added 2 · to do 2, 1 on others\n" || strings.Contains(stderr, "Ola") {
		t.Errorf("--waiting-for Ola: %q, %q, %v", out, stderr, err)
	}
	for args, want := range map[string]string{
		"--on|Marek|--waiting-for|Ola": "--on or --waiting-for, not both",
		"--waiting-for|me":             "--waiting-for me: not a person",
		"--waiting-for|BOSS":           "--waiting-for BOSS: not a person",
		"--waiting-for|w":              "--waiting-for w: a session you started: --on W",
		"--waiting-for| ":              "--waiting-for: no person",
	} {
		if _, _, err := execute(t, "", append([]string{"todo", "add", "x"}, strings.Split(args, "|")...)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	// A worker --on would read otherwise, or that shares its name, is named by its id
	org := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	for id, title := range map[string]string{"local_b": "Boss", "local_t1": "Twin", "local_t2": "Twin"} {
		record := `{"sessionId":"` + id + `","title":"` + title + `","createdAt":` + jsonInt(time.Now().UnixMilli()) + `,"spawnedFrom":{"sessionId":"local_boss"}}`
		if err := os.WriteFile(filepath.Join(org, id+".json"), []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for person, want := range map[string]string{"local_b": "--on local_b", "local_t1": "--on local_t1"} {
		if _, _, err := execute(t, "", "todo", "add", "x", "--waiting-for", person); err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("--waiting-for %s: %v, want it to end %q", person, err, want)
		}
	}
	if got := run(t, "", "todo", "add", "--replaces", "2", "--on", "boss"); got != "replaced 2 · to do 2\n" {
		t.Errorf("--replaces --on boss: %q", got)
	}
	if got := run(t, "", "todo", "add", "--replaces", "2", "--waiting-for", "Ola"); got != "replaced 2 · to do 2, 1 on others\n" {
		t.Errorf("--replaces --waiting-for: %q", got)
	}
	// Its own session id, also named by --boss, is the boss's own
	out, stderr, err = execute(t, "", "todo", "add", "--replaces", "2", "--on", "local_boss")
	if err != nil || out != "replaced 2 · to do 2\n" || !strings.Contains(stderr, "on local_boss: this list's own boss, so your own task, as --on boss\n") {
		t.Errorf("--on its own id: %q, %q, %v", out, stderr, err)
	}
	if _, _, err := execute(t, "", "todo", "add", "x", "--boss", "local_boss", "--waiting-for", "local_boss"); err == nil || !strings.Contains(err.Error(), "not a person") {
		t.Errorf("--boss local_boss --waiting-for its own id: %v", err)
	}
	if got := run(t, "", "todo"); got != "1\t☐\tParked idea\tboss's own\n2\t☐\tReview\tboss's own\n" {
		t.Errorf("dev todo = %q", got)
	}
	var l struct {
		Items []struct {
			On       string `json:"on"`
			Column   string `json:"column"`
			Assignee struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"assignee"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil || len(l.Items) != 2 {
		t.Fatalf("--json: %v %+v", err, l)
	}
	if it := l.Items[0]; it.On != "boss" || it.Column != "todo" || it.Assignee.Kind != "boss" || it.Assignee.Name != "boss" {
		t.Errorf("--json item = %+v, want on boss, to do, the boss's", it)
	}
	run(t, "", "todo", "note", "state", "6 fixes parked")
	if got := run(t, "", "todo", "note", "state", "7 fixes parked"); got != "noted n2\n" {
		t.Errorf("second state: %q", got)
	}
	if got := run(t, "", "todo", "note"); strings.Contains(got, "6 fixes") || !strings.Contains(got, "n2 ") || !strings.Contains(got, "state: 7 fixes parked") {
		t.Errorf("notes = %q, want the second state only", got)
	}
}

// --on a worker of the boss, by its local_… id or its name, links its session, shown under its name; --on
// anyone else is a person; --on me gives an item back to the user.
func TestTodoOnWorker(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "add", "By id", "--on", "local_w")
	run(t, "", "todo", "add", "By name", "--on", " w ")
	if _, stderr, err := execute(t, "", "todo", "add", "A person", "--on", "Marek"); err != nil || !strings.Contains(stderr, "no session Marek: a person is --waiting-for Marek, your own task --on boss\n") {
		t.Errorf("--on a person: stderr %q, %v; want a note", stderr, err)
	}
	run(t, "", "todo", "add", "Handed over", "--on", "Ola")
	run(t, "", "todo", "add", "Handed over", "--replaces", "4", "--on", "local_w")
	if got := run(t, "", "todo", "add", "Back to me", "--replaces", "3", "--on", "ME", "--details", "Ola handed it back"); got != "replaced 3 · to do 4, 3 on others\n" {
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
	// --replaces corrects it in place, keeping its number and what it doesn't give
	if got := run(t, "", "todo", "ask", "--replaces", "2", "--option", "Merge | after CI (Recommended)", "--context", "CI running"); got != "replaced 2 · to do 2\n" {
		t.Errorf("ask --replaces = %q", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\ta\n2\t☐\tMerge #148?\thttps://github.com/o/r/pull/148\toptions: Merge | after CI (Recommended)\n    - CI running\n" {
		t.Errorf("rows after --replaces = %q", got)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"todo", "ask", "Merge?"}, "no --option"},
		{[]string{"todo", "ask"}, "no question: only --replaces keeps"},
		{[]string{"todo", "ask", "--replaces", "2"}, "nothing to correct"},
		{[]string{"todo", "ask", "--replaces", "1", "--context", "x"}, "item 1 is no question"},
		{[]string{"todo", "ask", "--replaces", "2", "--option", "Merge", "--option", "merge"}, `option 2 repeats "merge"`},
		{[]string{"todo", "ask", " ", "--option", "a"}, "empty question"},
		{[]string{"todo", "ask", "Merge?", "--option", "a", "--option", " (Recommended)"}, "option 2 is empty"},
		{[]string{"todo", "ask", "Merge?", "--option", "Merge", "--option", "merge (Recommended)"}, `option 2 repeats "merge"`},
		{[]string{"todo", "ask", "Merge?", "--option", "Merge | now", "--option", "merge | later (Recommended)"}, `option 2 repeats "merge"`},
		{append([]string{"todo", "ask", "Pick?"}, strings.Split(strings.Repeat("--option|x|", 10), "|")[:20]...), "at most 9"},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

// An ask's --context lines print indented under it and its options with their details; --replaces keeps the context
// unless given; the caps refuse with the fix.
func TestTodoAskContext(t *testing.T) {
	todoEnv(t)
	if got := run(t, "", "todo", "ask", "Merge #148?", "--context", " CI   green ", "--context", "~/memo.md", "--option", "Merge (Recommended) | now", "--option", "Wait | ~1 day"); got != "asked 1 · to do 1\n" {
		t.Errorf("ask = %q", got)
	}
	want := "1\t☐\tMerge #148?\toptions: Merge | now (Recommended) · Wait | ~1 day\n    - CI green\n    - ~/memo.md\n"
	if got := run(t, "", "todo"); got != want {
		t.Errorf("rows = %q, want %q", got, want)
	}
	run(t, "", "todo", "add", "--no-details", "--replaces", "1", "Merge #148 now?")
	if got := run(t, "", "todo"); !strings.HasSuffix(got, "    - CI green\n    - ~/memo.md\n") {
		t.Errorf("after --replaces without --context: %q", got)
	}
	run(t, "", "todo", "add", "--no-details", "--replaces", "1", "--context", "review in")
	run(t, "", "todo", "add", "--no-details", "Ship", "--context", "CI green")
	if got := run(t, "", "todo"); !strings.HasSuffix(got, "2\t☐\tShip\n    - CI green\n") {
		t.Errorf("a plain item's context: %q", got)
	}
	var l struct {
		Items []struct {
			Context []string `json:"context"`
			Choices []struct {
				Label       string `json:"label"`
				Detail      string `json:"detail"`
				Recommended bool   `json:"recommended"`
			} `json:"choices"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil || len(l.Items) != 2 || strings.Join(l.Items[0].Context, "|") != "review in" ||
		len(l.Items[0].Choices) != 2 || l.Items[0].Choices[0].Label != "Merge" || l.Items[0].Choices[0].Detail != "now" || !l.Items[0].Choices[0].Recommended || l.Items[0].Choices[1].Detail != "~1 day" {
		t.Errorf("json = %+v, %v", l, err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"todo", "ask", strings.Repeat("x", 101), "--option", "a"}, "put the findings in --context lines or a file"},
		{[]string{"todo", "ask", "Ok?", "--option", strings.Repeat("x", 31)}, "put the rest after ' | '"},
		{[]string{"todo", "ask", "Ok?", "--option", "a", "--context", " "}, "--context line 1 is empty"},
		{[]string{"todo", "ask", "Ok?", "--option", " | detail"}, "option 1 is empty"},
		{[]string{"todo", "add", "--no-details", "--replaces", "1", strings.Repeat("x", 101)}, "question is 101 characters"},
		{[]string{"todo", "add", "--no-details", "x", "--context", " "}, "--context line 1 is empty"},
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
	typed := &inbox.Prompt{Text: "dev #7: merge it"}
	several := func(handOn bool, asks ...inbox.Asked) *inbox.Prompt {
		return &inbox.Prompt{Pick: true, Text: "words", At: 1, Asks: asks, HandOnMixed: handOn}
	}
	ask := func(q, a string) inbox.Asked { return inbox.Asked{Question: q, Answer: a} }
	// The index: of the question among several, -1 for a pick of one
	if i, err := pickFor(several(false, ask("dev #6: Tag?", "Tag"), ask("dev #7: Merge #148?", "Merge")), "boss-dev", 7, "Merge #148?", true); i != 1 || err != nil {
		t.Errorf("its question of several: %d, %v; want 1", i, err)
	}
	if i, err := pickFor(pick("dev #7: Merge #148?", "Merge"), "boss-dev", 7, "Merge #148?", true); i != -1 || err != nil {
		t.Errorf("a pick of one: %d, %v; want -1", i, err)
	}
	// Projects app and boss-app (session boss-boss-app): boss-app names only the second, not app's boss
	both := several(false, ask("boss-app #1: Merge?", "Wait"), ask("app #1: Merge?", "Merge"))
	for boss, want := range map[string]int{"boss-app": 1, "boss-boss-app": 0} {
		if i, err := pickFor(both, boss, 1, "Merge?", false); i != want || err != nil {
			t.Errorf("%s from a dialog asking app and boss-app: %d, %v; want %d", boss, i, err, want)
		}
	}
	if _, err := pickFor(pick("boss-app #1: Merge?", "Merge"), "boss-app", 1, "Merge?", false); err == nil || !strings.Contains(err.Error(), "not boss-app #1") {
		t.Errorf("project boss-app's question relayed to app's boss: %v, want refused", err)
	}
	// Projects app and boss-app with boss-boss-app closed (the session's name an alias again): both name app's boss
	if _, err := pickFor(both, "boss-app", 1, "Merge?", true); err == nil || !strings.Contains(err.Error(), "asked boss-app #1 twice") {
		t.Errorf("boss-app with the alias on, from a dialog asking app and boss-app: %v, want refused", err)
	}
	// Projects app and App: each its own, by the name as written
	cased := several(false, ask("App #1: Merge?", "Wait"), ask("app #1: Merge?", "Merge"))
	for boss, want := range map[string]int{"boss-app": 1, "boss-App": 0} {
		if i, err := pickFor(cased, boss, 1, "Merge?", true); i != want || err != nil {
			t.Errorf("%s from a dialog asking app and App: %d, %v; want %d", boss, i, err, want)
		}
	}
	// In a dialog of several only the project's or the session's name as written names it, the session's only when alias
	for _, tt := range []struct {
		q     string
		alias bool
		ok    bool
	}{{"App #1: Merge?", true, false}, {"boss-app #1: Merge?", true, true}, {"boss-app #1: Merge?", false, false}, {"Boss-app #1: Merge?", true, false}} {
		_, err := pickFor(several(false, ask(tt.q, "Merge"), ask("b #1: Tag?", "Tag")), "boss-app", 1, "Merge?", tt.alias)
		if tt.ok != (err == nil) || err != nil && !strings.Contains(err.Error(), "first line is \"<project> #<n>: <item text>\" as written") {
			t.Errorf("boss-app from a dialog asking %q, alias %v: %v, want accepted %v", tt.q, tt.alias, err, tt.ok)
		}
	}
	// One item asked twice the same way: refused, the picks may differ
	if _, err := pickFor(several(false, ask("dev #7: Merge #148?", "Merge"), ask("dev #7: Merge #148?\n\n- CI green", "Wait")), "boss-dev", 7, "Merge #148?", true); err == nil || !strings.Contains(err.Error(), "asked boss-dev #7 twice") {
		t.Errorf("an item asked twice: %v, want refused", err)
	}
	for _, tt := range []struct {
		name     string
		pick     *inbox.Prompt
		question string
		want     string
	}{
		{"typed message", typed, "Merge #148?", "a typed message, not a pick"},
		{"its item", pick("dev #7: Merge #148? (CI green)", "Merge"), "Merge #148?", ""},
		{"boss name", pick("boss-dev #7: Merge #148?", "Merge"), "Merge #148?", ""},
		{"no --question", pick("dev #7: Merge #148?", "Merge"), "", "needs --question"},
		{"several questions, none kept", pick("", ""), "Merge #148?", "held no question"},
		// A dialog of several: the question naming the item, each relayed on its own
		{"its question of several", several(false, ask("dev #6: Tag?", "Tag"), ask("dev #7: Merge #148?", "Merge")), "Merge #148?", ""},
		{"not in the dialog", several(false, ask("dev #6: Tag?", "Tag"), ask("dev #8: Ship?", "Ship")), "Merge #148?", `asked "dev #6: Tag?", "dev #8: Ship?", not "dev #7: Merge #148?"`},
		{"left unanswered", several(false, ask("dev #6: Tag?", "Tag"), ask("dev #7: Merge #148?", "")), "Merge #148?", "left \"dev #7: Merge #148?\" unanswered: the item stays open"},
		// A boss's own option of that label, recommended, is an answer
		{"a boss's Leave it open (Recommended)", pick("dev #7: Merge #148?", "Leave it open (Recommended)"), "Merge #148?", ""},
		{"left open in several", several(false, ask("dev #6: Tag?", "Tag"), ask("dev #7: Merge #148?", "Leave it open")), "Merge #148?", "the item stays open"},
		{"asked twice", several(false, ask("dev #7: Merge #148?", "Merge"), ask("dev #7: Merge #148?\n\n- CI green", "Wait")), "Merge #148?", "asked boss-dev #7 twice"},
		// Only the first line names the item: not the item's text within a longer one, nor in another's context
		{"text within a longer question", several(false, ask("dev #7: Merge #148? Or wait for #150?", "Merge"), ask("dev #6: Tag?", "Tag")), "Merge #148?", "not \"dev #7: Merge #148?\""},
		{"text in another question's context", several(false, ask("dev #7: Ship internally?\n\n- then Merge #148?", "Ship"), ask("dev #6: Tag?", "Tag")), "Merge #148?", "not \"dev #7: Merge #148?\""},
		{"beside a hand-on question", several(true, ask("Hand on to boss-dev?", "Hand it on"), ask("dev #7: Merge #148?", "Merge")), "Merge #148?", "a hand-on question stays alone in its dialog"},
		{"another item number", pick("dev #6: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"another boss", pick("terminal #7: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"another text", pick("dev #7: Ship?", "Ship"), "Merge #148?", "not boss-dev #7"},
		{"number prefix", pick("dev #70: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"project suffix", pick("api-dev #7: Merge #148?", "Merge"), "Merge #148?", "not boss-dev #7"},
		{"left open", pick("dev #7: Merge #148?", "leave it open"), "Merge #148?", "the item stays open"},
		// The manager's own assessment question names no item: never an item's answer, typed under Other neither
		{"assessment", pick("Hand on the user's idea to cache Merge #148? builds?", "Hand it on"), "Merge #148?", "not boss-dev #7"},
		{"assessment, Other", pick("Merge #148? cache idea: hand it on?", "merge it and cache the builds"), "Merge #148?", "not boss-dev #7"},
	} {
		_, err := pickFor(tt.pick, "boss-dev", 7, tt.question, true)
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

// dev todo's line says who is on the item, with what they do when they said it; without a doing mark it's as before.
func TestTodoLineDoing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tt := range []struct {
		it   todo.Shown
		want string
	}{
		{todo.Shown{ID: 117, Text: "Teams to Jan"}, "117\t☐\tTeams to Jan"},
		{todo.Shown{ID: 117, Text: "Teams to Jan", Doing: &todo.Doing{}}, "117\t☐\tTeams to Jan\tthe user is on it"},
		{todo.Shown{ID: 117, Text: "Teams to Jan", Doing: &todo.Doing{Text: "writing it"}}, "117\t☐\tTeams to Jan\tthe user is on it: writing it"},
		{todo.Shown{ID: 5, Text: "Merge #1", On: todo.Boss, Doing: &todo.Doing{}}, "5\t☐\tMerge #1\tboss's own\tthe boss is on it"},
		{todo.Shown{ID: 6, Text: "PR #412 open", Doing: &todo.Doing{}, Check: &sidebar.Check{Kind: sidebar.CheckMerged, PR: 412, Since: now.Add(-40 * time.Minute).Unix()}},
			"6\t☐\tPR #412 open\tthe user is on it\t⚠ PR #412 merged 40m ago"},
	} {
		if got := todoLine(tt.it, now); got != tt.want {
			t.Errorf("line = %q, want %q", got, tt.want)
		}
	}
}

// add --done-on needs a GitHub PR link; dev todo check ticks the item once its PR is so, and a desktop boss's next
// reminder tells it once.
func TestTodoDoneOn(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	url := "https://github.com/acme/orbit/pull/415"
	for _, args := range [][]string{{"todo", "add", "--no-details", "x", "--done-on", "merged"}, {"todo", "add", "x", "--link", url, "--done-on", "deployed"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	run(t, "", "todo", "add", "Second review of #415", "--on", "Ana", "--link", url, "--done-on", "approved")
	run(t, "", "todo", "add", "--no-details", "Keep")
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

// add --ends takes restart:<version> or test:<version>, never on an ask; dev todo check ticks an older version test
// once a newer one is on the list, by dev, the result saying why.
func TestTodoEnds(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	for _, args := range [][]string{{"todo", "add", "--no-details", "x", "--ends", "reboot:v1.0.0"}, {"todo", "add", "--no-details", "x", "--ends", "test:1.0"},
		{"todo", "ask", "Restart?", "--option", "Yes", "--option", "No", "--ends", "restart:v1.0.0"}} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("❌ dev %s: want an error", strings.Join(args, " "))
		}
	}
	run(t, "", "todo", "add", "--no-details", "Check v0.83.0 on the iPad", "--ends", "test:v0.83.0")
	run(t, "", "todo", "add", "--no-details", "Keep")
	if got := run(t, "", "todo", "check"); got != "1\t☐\tCheck v0.83.0 on the iPad\tends test:v0.83.0\n2\t☐\tKeep\n" {
		t.Errorf("❌ no newer test: %q", got)
	}
	run(t, "", "todo", "add", "--no-details", "Test v0.93.0 by hand", "--ends", "test:v0.93.0")
	if got := run(t, "", "todo", "check"); !strings.Contains(got, "1\t☑\tCheck v0.83.0 on the iPad\tends test:v0.83.0\tclosed by dev: v0.93.0 replaced v0.83.0 (#3)\n") {
		t.Errorf("❌ check = %q, want #1 closed by dev", got)
	}
}

// note adds, prints and drops; a refusal leaves the notes as they were.
func TestTodoNote(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "set", "Merge #4")
	if got := run(t, "", "todo", "note"); got != "no notes\n" {
		t.Errorf("no notes = %q", got)
	}
	if got := run(t, "", "todo", "note", "decided", "CSV only", "--why", "parser reads CSV", "--item", "1"); got != "noted n1\n" {
		t.Errorf("decided = %q", got)
	}
	run(t, "", "todo", "note", "said", "no merges to acme", "--until", "2099-01-01")
	got := run(t, "", "todo", "note")
	for _, want := range []string{"Your notes (2 of 12", " n1 ", "decided: CSV only (why: parser reads CSV) #1", " n2 ", "said: no merges to acme (until 2099-01-02 00:00)", "dev todo note drop <n>"} {
		if !strings.Contains(got, want) {
			t.Errorf("notes = %q, want %q", got, want)
		}
	}
	for _, args := range [][]string{
		{"todo", "note", "said", "no end"},
		{"todo", "note", "risk", strings.Repeat("x", 201)},
		{"todo", "note", "risk", "x", "--item", "9"},
		{"todo", "note", "drop", "7"},
		{"todo", "note", "drop", "x"},
		{"todo", "note", "said", "x", "--until", "2000-01-01"},
		{"todo", "note", "said", "x", "--until", "soon"},
	} {
		if _, _, err := execute(t, "", args...); err == nil {
			t.Errorf("dev %s: want an error", strings.Join(args, " "))
		}
	}
	if got := run(t, "", "todo", "note"); !strings.Contains(got, "(2 of 12") {
		t.Errorf("refused notes were stored: %q", got)
	}
	if got := run(t, "", "todo", "note", "drop", "n1"); got != "dropped n1\n" {
		t.Errorf("drop = %q", got)
	}
	if got := run(t, "", "todo", "note"); strings.Contains(got, "n1 ") || !strings.Contains(got, "n2 ") {
		t.Errorf("after drop = %q", got)
	}
	if got := run(t, "", "todo"); got != "1\t☐\tMerge #4\n" {
		t.Errorf("notes changed the list: %q", got)
	}
}

// Printing notes takes no list over; the first add of a note does, as a first dev todo add does.
func TestTodoNotePrintAdoptsNothing(t *testing.T) {
	todoEnv(t)
	desktopSessions(t)
	run(t, "", "todo", "--boss", "local_old", "set", "old open item")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	if got := run(t, "", "todo", "note"); got != "no notes\n" {
		t.Errorf("note = %q", got)
	}
	if _, _, err := execute(t, "", "todo", "note", "drop", "1"); err == nil {
		t.Error("drop of a missing note: want an error")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "desktop-local_boss.json")); err == nil {
		t.Error("print or a refused drop made local_boss a list")
	}
	if got := run(t, "", "todo", "--boss", "local_old"); got != "1\t☐\told open item\n" {
		t.Errorf("local_old's list = %q, want it kept", got)
	}
}

// Note ids are not reused after a drop, and drop takes n1 or N1.
func TestTodoNoteIDs(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "note", "risk", "a")
	run(t, "", "todo", "note", "risk", "b")
	run(t, "", "todo", "note", "drop", "N2")
	if got := run(t, "", "todo", "note", "risk", "c"); got != "noted n3\n" {
		t.Errorf("after dropping the highest = %q, want n3", got)
	}
}

// --quiet on add and ask stores a test item, quiet in the JSON, kept by --replaces.
func TestTodoQuiet(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "add", "t", "--quiet")
	run(t, "", "todo", "ask", "t?", "--option", "a", "--option", "b", "--quiet")
	run(t, "", "todo", "add", "--no-details", "real")
	run(t, "", "todo", "add", "--no-details", "--replaces", "1", "t2")
	var l struct {
		Items []struct {
			Quiet bool `json:"quiet"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil || len(l.Items) != 3 || !l.Items[0].Quiet || !l.Items[1].Quiet || l.Items[2].Quiet {
		t.Errorf("json = %+v, %v; want quiet, quiet, not", l, err)
	}
}

// Over 120 characters add warns on stderr and still adds; at 120, or with --replaces alone, it stays quiet.
func TestTodoAddLongHint(t *testing.T) {
	todoEnv(t)
	_, errOut, err := execute(t, "", "todo", "add", "--no-details", strings.Repeat("x", 121))
	if err != nil || !strings.Contains(errOut, "long item (121 characters)") {
		t.Errorf("121: err %v, stderr %q", err, errOut)
	}
	if _, errOut, err = execute(t, "", "todo", "add", "--no-details", strings.Repeat("é", 120)); err != nil || strings.Contains(errOut, "long item") {
		t.Errorf("120: err %v, stderr %q", err, errOut)
	}
	if _, errOut, err = execute(t, "", "todo", "add", "--replaces", "1", "--on", "Marek"); err != nil || strings.Contains(errOut, "long item") {
		t.Errorf("replaces alone: err %v, stderr %q", err, errOut)
	}
	if got := run(t, "", "todo", "--json"); strings.Count(got, `"text"`) != 2 {
		t.Errorf("items not added: %s", got)
	}
}

// add of an item the user does without --details is refused, unless --no-details, writing nothing; --replaces keeps
// the old item's details, refused only when it had none. A waiting item and ask without --details warn on stderr and
// still add; a worker's item, --quiet, and a replace of an item that has details stay quiet; an ask whose last
// --context is a file still warns.
func TestTodoNoDetailsWarning(t *testing.T) {
	todoEnv(t)
	const warn = "no details: add them with --details"
	const (
		quiet = iota
		warns
		refused
	)
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"add", []string{"add", "Mail Vince"}, refused},
		{"add --on me", []string{"add", "Mine", "--on", "me"}, refused},
		{"add --no-details", []string{"add", "Water the plants", "--no-details"}, quiet},
		{"add waiting", []string{"add", "Reply from Ola", "--waiting-for", "Ola"}, warns},
		{"add with details", []string{"add", "Documented", "--details", "what and why"}, quiet},
		{"add on worker", []string{"add", "W's", "--on", "w"}, quiet},
		{"add on boss", []string{"add", "Own", "--on", "boss"}, quiet},
		{"add quiet", []string{"add", "Test", "--quiet"}, quiet},
		{"ask", []string{"ask", "Merge?", "--option", "a", "--option", "b"}, warns},
		{"ask with file context", []string{"ask", "Ship?", "--option", "a", "--option", "b", "--context", "/tmp/draft.md"}, warns},
		{"ask with details", []string{"ask", "Pick?", "--option", "a", "--option", "b", "--details", "why"}, quiet},
		{"ask quiet", []string{"ask", "T?", "--option", "a", "--option", "b", "--quiet"}, quiet},
	} {
		_, errOut, err := execute(t, "", append([]string{"todo"}, tt.args...)...)
		switch {
		case tt.want == refused && (err == nil || !strings.Contains(err.Error(), "needs --details") || !strings.Contains(err.Error(), "--no-details")):
			t.Errorf("%s: err %v, want refused", tt.name, err)
		case tt.want != refused && (err != nil || strings.Contains(errOut, warn) != (tt.want == warns)):
			t.Errorf("%s: err %v, stderr %q, want warning %v", tt.name, err, errOut, tt.want == warns)
		}
	}
	if got := strings.Count(run(t, "", "todo", "--json"), `"text"`); got != 10 {
		t.Errorf("%d items added, want 10", got)
	}
	if _, _, err := execute(t, "", "todo", "add", "Both", "--details", "x", "--no-details"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("--details and --no-details: %v, want refused", err)
	}
	// --no-details is for an item the user does: a waiting one still warns
	if _, errOut, err := execute(t, "", "todo", "add", "Reply from Ola", "--waiting-for", "Ola", "--no-details"); err != nil || !strings.Contains(errOut, warn) {
		t.Errorf("--no-details --waiting-for: err %v, stderr %q, want the warning", err, errOut)
	}
	// An ask without details only warns, also when add --replaces changes it
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "7", "--link", "https://example.com/pr/1"); err != nil || !strings.Contains(errOut, warn) {
		t.Errorf("add --replaces of a bare ask: err %v, stderr %q, want the warning", err, errOut)
	}

	todoEnv(t)
	run(t, "", "todo", "add", "Documented", "--details", "what and why")
	run(t, "", "todo", "add", "Bare", "--no-details")
	run(t, "", "todo", "add", "Rework", "--on", "boss")
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "1", "Documented, retitled"); err != nil || strings.Contains(errOut, warn) {
		t.Errorf("replace of a user item with details: err %v, stderr %q", err, errOut)
	}
	// Bare keeps its --no-details (stored); the boss's own item given back to the user without details is refused
	if _, _, err := execute(t, "", "todo", "add", "--replaces", "3", "--on", "me"); err == nil || !strings.Contains(err.Error(), "needs --details") {
		t.Errorf("the boss's item given back without details: %v, want refused", err)
	}
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "2", "Bare, renamed"); err != nil || strings.Contains(errOut, warn) {
		t.Errorf("a plain replace of a --no-details item: err %v, stderr %q", err, errOut)
	}
	if out := run(t, "", "todo", "--json"); !strings.Contains(out, `"Documented, retitled"`) || !strings.Contains(out, `"what and why"`) ||
		strings.Count(out, `"on": "boss"`) != 1 {
		t.Errorf("after the refused replaces: %s, want details kept and the refused ones unchanged", out)
	}
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "2", "Bare, retitled", "--no-details"); err != nil || strings.Contains(errOut, warn) {
		t.Errorf("replace with --no-details: err %v, stderr %q", err, errOut)
	}
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "1", "--on", "Marek"); err != nil || strings.Contains(errOut, warn) {
		t.Errorf("replace of an item with details: err %v, stderr %q", err, errOut)
	}
	if _, errOut, err := execute(t, "", "todo", "add", "--replaces", "2", "--on", "Marek"); err != nil || !strings.Contains(errOut, warn) {
		t.Errorf("replace of a bare item: err %v, stderr %q, want the warning", err, errOut)
	}

	todoEnv(t)
	run(t, "", "todo", "ask", "Documented?", "--option", "a", "--option", "b", "--details", "why")
	run(t, "", "todo", "ask", "Bare?", "--option", "a", "--option", "b")
	if _, errOut, err := execute(t, "", "todo", "ask", "--replaces", "1", "--context", "x"); err != nil || strings.Contains(errOut, warn) {
		t.Errorf("ask --replaces of an ask with details: err %v, stderr %q", err, errOut)
	}
	if _, errOut, err := execute(t, "", "todo", "ask", "--replaces", "2", "--context", "x"); err != nil || !strings.Contains(errOut, warn) {
		t.Errorf("ask --replaces of a bare ask: err %v, stderr %q, want the warning", err, errOut)
	}
}

// A malformed projects.toml blocks only a pick that needs the alias lookup: the project's own question still relays.
func TestPickIndexConfigError(t *testing.T) {
	withConfig(t, "[[project\n")
	cmd := newRootCmd()
	own := &inbox.Prompt{Pick: true, Question: "manager #1: Merge?", Answer: "Merge"}
	if i, err := pickIndex(cmd, nil, own, "manager", 1, "Merge?"); i != -1 || err != nil {
		t.Errorf("a pick naming the manager: %d, %v; want relayed", i, err)
	}
	alias := &inbox.Prompt{Pick: true, Question: "boss-app #1: Merge?", Answer: "Merge"}
	if _, err := pickIndex(cmd, nil, alias, "boss-app", 1, "Merge?"); err == nil {
		t.Error("a pick naming the session over a malformed projects.toml: want the config error")
	}
	// Both names in one dialog: the alias may make them one item asked twice, which a lost lookup can't rule out
	both := &inbox.Prompt{Pick: true, Asks: []inbox.Asked{{Question: "boss-app #1: Merge?", Answer: "Wait"}, {Question: "app #1: Merge?", Answer: "Merge"}}}
	if _, err := pickIndex(cmd, nil, both, "boss-app", 1, "Merge?"); err == nil {
		t.Error("an app and boss-app question over a malformed projects.toml: want refused")
	}
}

// An ask answered and reopened with the same id and text is a new ask: the mark set before is not its.
func TestMarkAskedReopened(t *testing.T) {
	asked := inbox.Asking{Boss: "boss-a", ID: 1, Text: "Ship?", Added: 100, At: 100}
	for added, want := range map[int64]bool{100: true, 101: false, 99: false} {
		lists := []bossList{{Boss: "boss-a", List: todo.List{Items: []todo.Shown{{ID: 1, Text: "Ship?", Options: []string{"Ship"}, Added: added}}}}}
		markAsked(lists, []inbox.Asking{asked})
		if got := lists[0].Items[0].Asking; got != want {
			t.Errorf("added at %d, marked reading 100: asking %v, want %v", added, got, want)
		}
	}
}

func TestTodoNoteCapDrops(t *testing.T) {
	todoEnv(t)
	for i := 1; i <= todo.MaxNotes; i++ {
		run(t, "", "todo", "note", "risk", fmt.Sprintf("risk %d", i))
	}
	want := fmt.Sprintf("noted n%d\ndropped n1, the oldest at the cap: risk risk 1\n", todo.MaxNotes+1)
	if got := run(t, "", "todo", "note", "risk", "one more"); got != want {
		t.Errorf("note at the cap = %q, want %q", got, want)
	}
}
