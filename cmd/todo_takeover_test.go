package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

const (
	oldBoss = "local_49f62c8e"
	newBoss = "local_5f1c2d90"
)

// takeOverEnv runs dev as desktop app session newBoss in project dev (/p/dev), on a tmux server where boss-dev runs,
// with app sessions oldBoss and local_stop stopped and local_run running in /p/dev; it returns the folder of the lists.
func takeOverEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", newBoss)
	t.Setenv("TMUX_PANE", "")
	withConfig(t, "[[project]]\nname = \"dev\"\npath = \"/p/dev\"\n\n[[project]]\nname = \"vitay\"\npath = \"/p/vitay\"\n")
	oldTmux, oldView := takeOverTmux, viewPR
	t.Cleanup(func() { takeOverTmux, viewPR = oldTmux, oldView })
	viewPR = func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }
	takeOverTmux = func() *tmux.Client {
		return tmux.NewFake(func(args []string) (string, error) {
			if args[0] == "display-message" {
				return "/tmp/tmux-1/default\n", nil
			}
			return tmux.FormatRow("boss-dev"), nil
		})
	}
	org := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org")
	live := filepath.Join(home, ".claude", "sessions")
	for dir, files := range map[string]map[string]string{
		org: {
			"local_run.json":           `{"sessionId":"local_run","cliSessionId":"cli-run","originCwd":"/p/dev"}`,
			"local_stop.json":          `{"sessionId":"local_stop","originCwd":"/p/dev"}`,
			newBoss + ".json":          `{"sessionId":"` + newBoss + `","originCwd":"/p/dev"}`,
			oldBoss + ".json":          `{"sessionId":"` + oldBoss + `","originCwd":"/p/dev"}`,
			"local_49f62c8e-1111.json": `{"sessionId":"local_49f62c8e-1111","originCwd":"/p/dev"}`,
			"local_49f62c8e-2222.json": `{"sessionId":"local_49f62c8e-2222","originCwd":"/p/dev"}`,
			"local_eeeeeeee.json":      `{"sessionId":"local_eeeeeeee","originCwd":"/p/dev"}`,
		},
		live: {"1.json": `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"cli-run","kind":"interactive","status":"idle"}`},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	lists := filepath.Join(home, "state", "dev", "todo")
	if err := os.MkdirAll(lists, 0o700); err != nil {
		t.Fatal(err)
	}
	return lists
}

// writeList writes a list file name under lists with one open item text, changed ago.
func writeList(t *testing.T, lists, name, boss, text string, ago time.Duration) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"items": []map[string]any{{"id": 1, "text": text}}, "next": 2, "boss": boss, "updated": time.Now().Add(-ago).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lists, name+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// as runs dev as desktop app session id.
func as(t *testing.T, id string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", id)
	defer t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", newBoss)
	return execute(t, "", args...)
}

// The listing shows this project's lists newest change first, why one can't be taken, and counts the others; a take
// moves the list, numbers kept, and the old session's writes are refused, saying where it went and how to take it back.
func TestTodoTakeOver(t *testing.T) {
	lists := takeOverEnv(t)
	for _, args := range [][]string{
		{"todo", "add", "--no-details", "Build dev agent start --from", "--link", "https://github.com/ppf/dev/pull/282"},
		{"todo", "ask", "Merge #282?", "--option", "Merge (Recommended)", "--option", "Wait"},
		{"todo", "note", "decided", "branch takeover: picks A-G approved"},
	} {
		if _, _, err := as(t, oldBoss, args...); err != nil {
			t.Fatal(err)
		}
	}
	writeList(t, lists, "tmp_tmux-1_default-boss-dev", "boss-dev", "running's", 31*time.Minute)
	writeList(t, lists, "tmp_tmux-1_default-boss-vitay", "boss-vitay", "vitay's", time.Hour)
	writeList(t, lists, "desktop-local_stop", "", "stopped's", 11*24*time.Hour)
	writeList(t, lists, "desktop-local_run", "", "running app's", 2*time.Hour)

	out := run(t, "", "todo", "take-over")
	for _, want := range []string{
		"Lists this boss (local_5f1c2d90, dev) can take over, newest change first:\n  local_49f62c8e  desktop  stopped",
		"  boss-dev        tmux     running   1 open           0 notes   changed 31m ago   ✗ running: close it first\n",
		"  local_run       desktop  running",
		"  local_stop      desktop  stopped   1 open           0 notes   changed 11d ago\n",
		"1 list of another project (boss-vitay): --any-project\nTake one: dev todo take-over <list>\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "stopped   2 open, 1 ask    1 note    changed just now\n") {
		t.Errorf("old session's row lacks its counts:\n%s", out)
	}
	if strings.Index(out, "local_49f62c8e") > strings.Index(out, "boss-dev") || strings.Index(out, "boss-dev") > strings.Index(out, "local_run") {
		t.Errorf("rows not newest first:\n%s", out)
	}
	var listing struct {
		Lists []struct {
			List, Kind, Project, State string
			Open, Asks, Notes          int
			Updated                    int64
			Takeable                   bool
			WhyNot                     string `json:"why_not"`
		}
		OtherProjects int `json:"other_projects"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "take-over", "--json")), &listing); err != nil {
		t.Fatal(err)
	}
	if l := listing.Lists; len(l) != 4 || listing.OtherProjects != 1 || l[0].List != "local_49f62c8e" || l[0].Kind != "desktop" || l[0].Project != "dev" ||
		l[0].State != "stopped" || l[0].Open != 2 || l[0].Asks != 1 || l[0].Notes != 1 || l[0].Updated == 0 || !l[0].Takeable ||
		l[1].List != "boss-dev" || l[1].Takeable || l[1].WhyNot != "running: close it first" {
		t.Errorf("--json = %+v", listing)
	}
	if out := run(t, "", "todo", "take-over", "--any-project"); !strings.Contains(out, "  boss-vitay      tmux     closed") || strings.Contains(out, "--any-project\n") {
		t.Errorf("--any-project listing:\n%s", out)
	}

	out = run(t, "", "todo", "take-over", "local_49f62c8e")
	want := "✅ Took over local_49f62c8e (desktop, dev): 2 items, 1 note\n" +
		"  #1 → #1   Build dev agent start --from\n" +
		"  #2 → #2   Merge #282?\n" +
		"  n1 → n1   decided: branch takeover: picks A-G approved\n" +
		"Next: dev todo check\n"
	if out != want {
		t.Errorf("take-over =\n%s\nwant\n%s", out, want)
	}
	if got := run(t, "", "todo"); !strings.HasPrefix(got, "1\t☐\tBuild dev agent start --from\thttps://github.com/ppf/dev/pull/282\n2\t☐\tMerge #282?") {
		t.Errorf("dev todo after = %q", got)
	}
	if got := run(t, "", "todo", "note"); !strings.Contains(got, "n1") || !strings.Contains(got, "picks A-G approved") {
		t.Errorf("notes after = %q", got)
	}

	at := time.Now().Format("15:04")
	_, _, err := as(t, oldBoss, "todo", "add", "--no-details", "Merge #283")
	if want := "list moved to local_5f1c2d90 at " + at + " (dev todo take-over): this boss's to do list is there now; take it back there with dev todo take-over local_5f1c2d90"; err == nil || err.Error() != want {
		t.Errorf("old session's add: %v, want %q", err, want)
	}
	if stdout, stderr, err := as(t, oldBoss, "todo"); err != nil || stdout != "" || stderr != "⚠ dev todo: list moved to local_5f1c2d90 at "+at+" (dev todo take-over)\n" {
		t.Errorf("old session's read: %q %q %v", stdout, stderr, err)
	}
	if _, _, err := execute(t, "", "todo", "take-over", "local_49f62c8e"); err == nil || err.Error() != "local_49f62c8e moved to local_5f1c2d90 at "+at+": nothing left to take" {
		t.Errorf("take of a moved list: %v", err)
	}
	// What the refusal says works: the old session takes it back
	if out, _, err := as(t, oldBoss, "todo", "take-over", "local_5f1c2d90"); err != nil || !strings.HasPrefix(out, "✅ Took over local_5f1c2d90 (desktop, dev): 2 items, 1 note\n") {
		t.Errorf("take back: %q %v", out, err)
	}
	if _, _, err := execute(t, "", "todo", "add", "--no-details", "x"); err == nil || !strings.Contains(err.Error(), "list moved to local_49f62c8e") {
		t.Errorf("new session's add after the take back: %v", err)
	}
}

// Each refusal: --boss, its own list, a running tmux or desktop boss, another project's, the manager's, an empty or
// unknown one, an id too short, and taking into a list with items or notes; nothing moves.
func TestTodoTakeOverRefusals(t *testing.T) {
	lists := takeOverEnv(t)
	writeList(t, lists, "tmp_tmux-1_default-boss-dev", "boss-dev", "running's", time.Minute)
	writeList(t, lists, "tmp_tmux-1_default-boss-vitay", "boss-vitay", "vitay's", time.Minute)
	writeList(t, lists, "tmp_tmux-1_default-manager", "manager", "manager's", time.Minute)
	writeList(t, lists, "desktop-local_run", "", "running app's", time.Minute)
	writeList(t, lists, "desktop-local_0a8cec22", "", "older dev's", time.Minute)
	if err := os.WriteFile(filepath.Join(lists, "desktop-local_eeeeeeee.json"), []byte(`{"items":[],"next":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := as(t, oldBoss, "todo", "add", "--no-details", "theirs"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"todo", "take-over", "--boss", "boss-dev", "local_49f62c8e"}, "take-over takes into the list of the boss that runs it: leave out --boss"},
		{[]string{"todo", "take-over", "local_5f1c2d90"}, "local_5f1c2d90 is this boss's own list"},
		{[]string{"todo", "take-over", "boss-dev"}, "boss-dev is running: close it first (its manager's ×, or /exit in it), then take it over"},
		{[]string{"todo", "take-over", "local_run"}, "local_run is running: close it first (/exit in it), then take it over"},
		{[]string{"todo", "take-over", "boss-vitay"}, "boss-vitay is project vitay, not dev: --any-project takes it"},
		{[]string{"todo", "take-over", "manager"}, "manager is the manager's list, not dev's: --any-project takes it"},
		{[]string{"todo", "take-over", "local_0a8cec22"}, "local_0a8cec22 is of no known project, not dev: --any-project takes it"},
		{[]string{"todo", "take-over", "local_eeeeeeee"}, "local_eeeeeeee is empty (no open items, no notes): nothing to take"},
		{[]string{"todo", "take-over", "boss-nobody"}, "no list boss-nobody: dev todo take-over lists those this boss can take"},
		{[]string{"todo", "take-over", "local_49f6"}, "local_49f6: give 8 or more characters of its id"},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || err.Error() != tt.want {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
	if out, _, err := as(t, oldBoss, "todo"); err != nil || !strings.Contains(out, "theirs") {
		t.Errorf("old list after the refusals: %q %v", out, err)
	}
	// With --any-project another project's list is taken
	if out, _, err := execute(t, "", "todo", "take-over", "--any-project", "boss-vitay"); err != nil || !strings.HasPrefix(out, "✅ Took over boss-vitay (tmux, vitay): 1 item, 0 notes\n") {
		t.Errorf("--any-project take: %q %v", out, err)
	}
}

func TestTodoTakeOverHelp(t *testing.T) {
	takeOverEnv(t)
	out := run(t, "", "todo", "take-over", "--help")
	for _, want := range []string{
		"Take over another boss's to do list for this project: its items (who does each, links, --until, --done-on,\ncontext, doing, open asks), its notes, PR checks and finished items move to this boss's list: into an empty one\nnumbers kept, else after its own items, renumbered.",
		"Usage:\n  dev todo take-over [<list>] [flags]",
		"--any-project   lists of other projects, the manager's, and lists of no known project too",
		"--json          print a JSON object: without <list>, lists (each with list, kind: tmux or desktop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

// A shared prefix is refused, naming each; a longer one takes; --json says what moved.
func TestTodoTakeOverAmbiguousJSON(t *testing.T) {
	lists := takeOverEnv(t)
	for _, id := range []string{"local_49f62c8e-1111", "local_49f62c8e-2222"} {
		writeList(t, lists, "desktop-"+id, "", "from "+id, time.Minute)
	}
	if _, _, err := execute(t, "", "todo", "take-over", "local_49f62c8e"); err == nil || err.Error() != "local_49f62c8e matches 2 lists (local_49f62c8e-1111, local_49f62c8e-2222): give more of its id" {
		t.Errorf("shared prefix: %v", err)
	}
	out := run(t, "", "todo", "take-over", "--json", "local_49f62c8e-2222")
	var v struct {
		From, To  struct{ List, Kind, Project string }
		Mode      string
		Items     []struct{ From, To int }
		Notes     []struct{ From, To int }
		OnWorkers []string `json:"on_workers"`
		Warnings  []string
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.From.List != "local_49f62c8e-2222" || v.To.List != "local_5f1c2d90" || v.To.Kind != "desktop" || v.To.Project != "dev" || v.Mode != "move" ||
		len(v.Items) != 1 || v.Items[0].From != 1 || v.Items[0].To != 1 || len(v.Notes) != 0 || v.OnWorkers == nil || len(v.Warnings) != 0 {
		t.Errorf("--json = %s", out)
	}
	if got := run(t, "", "todo"); got != "1\t☐\tfrom local_49f62c8e-2222\n" {
		t.Errorf("taken = %q", got)
	}
}

// A take into a list with items merges: --json says mode merge, each item's and note's numbers, the dropped state note.
func TestTodoTakeOverMergeJSON(t *testing.T) {
	takeOverEnv(t)
	for _, args := range [][]string{{"todo", "add", "--no-details", "theirs"}, {"todo", "note", "state", "their state"}} {
		if _, _, err := as(t, oldBoss, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(t, "", "todo", "add", "--no-details", "mine")
	run(t, "", "todo", "note", "state", "my state")
	out := run(t, "", "todo", "take-over", "--json", "local_49f62c8e")
	var v struct {
		Mode    string
		Items   []struct{ From, To int }
		Notes   []struct{ From, To int }
		Dropped *struct {
			ID   int
			Kind string
			Text string
		} `json:"dropped_state_note"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	// Both state notes were added in the same second: the destination's stays
	if v.Mode != "merge" || len(v.Items) != 1 || v.Items[0].From != 1 || v.Items[0].To != 2 || len(v.Notes) != 0 ||
		v.Dropped == nil || v.Dropped.Kind != "state" || v.Dropped.Text != "their state" {
		t.Errorf("--json = %s", out)
	}
	if got := run(t, "", "todo"); got != "1\t☐\tmine\n2\t☐\ttheirs\n" {
		t.Errorf("merged = %q", got)
	}
}

// A desktop app boss with an empty list hears on resume that a stopped session's list for its project waits; a session
// that never wrote a list (a worker too) doesn't, nor the boss once it has items.
func TestTodoTakeOverDesktopHint(t *testing.T) {
	todoEnv(t)
	desktopSessions(t)
	withConfig(t, "[[project]]\nname = \"proj\"\npath = \"/p\"\n")
	// The worker runs in the project too: only being a worker keeps the hint from it
	worker := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org", "local_w.json")
	if err := os.WriteFile(worker, []byte(`{"sessionId":"local_w","originCwd":"/p","spawnedFrom":{"sessionId":"local_boss"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "", "todo", "--boss", "local_old", "set", "old open item")
	start := func(id string) string {
		t.Helper()
		t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", id)
		return run(t, `{"hook_event_name":"SessionStart","source":"resume"}`, "state", "idle")
	}
	// A session that never wrote a list is no boss
	if got := start("local_boss"); got != "" {
		t.Errorf("session with no list: %q, want nothing", got)
	}
	run(t, "", "todo", "--boss", "local_boss", "set", "plan")
	run(t, "", "todo", "--boss", "local_boss", "clear")
	if got := start("local_boss"); !strings.Contains(got, "An earlier boss's list for proj waits: local_old (1 open, 0 notes, changed just now): dev todo take-over local_old.") {
		t.Errorf("boss resume: %q, want the hint", got)
	}
	if got := start("local_w"); got != "" {
		t.Errorf("worker resume: %q, want nothing", got)
	}
	run(t, "", "todo", "--boss", "local_boss", "set", "mine")
	if got := start("local_boss"); strings.Contains(got, "waits") {
		t.Errorf("boss with a list: %q, want no hint", got)
	}
}
