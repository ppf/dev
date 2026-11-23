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
	oldThread = "49f62c8e-1111-4000-8000-000000000001"
	newThread = "5f1c2d90-2222-4000-8000-000000000002"
)

// takeOverEnv runs dev as T3 Code thread newThread in project dev (/p/dev), on a tmux server where boss-dev runs,
// with app session local_run running in /p/dev; it returns the folder of the lists.
func takeOverEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("T3CODE_THREAD_ID", newThread)
	withConfig(t, "[[project]]\nname = \"dev\"\npath = \"/p/dev\"\n\n[[project]]\nname = \"vitay\"\npath = \"/p/vitay\"\n")
	oldCheckout, oldTmux, oldView := mainCheckout, takeOverTmux, viewPR
	t.Cleanup(func() { mainCheckout, takeOverTmux, viewPR = oldCheckout, oldTmux, oldView })
	mainCheckout = func() (string, error) { return "/p/dev", nil }
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
			"local_run.json":  `{"sessionId":"local_run","cliSessionId":"cli-run","originCwd":"/p/dev"}`,
			"local_stop.json": `{"sessionId":"local_stop","originCwd":"/p/dev"}`,
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

// as runs dev as T3 Code thread id.
func as(t *testing.T, id string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("T3CODE_THREAD_ID", id)
	defer t.Setenv("T3CODE_THREAD_ID", newThread)
	return execute(t, "", args...)
}

// The listing shows this project's lists newest change first, why one can't be taken, and counts the others; a take
// moves the list, numbers kept, and the old thread's writes are refused, saying where it went and how to take it back.
func TestTodoTakeOver(t *testing.T) {
	lists := takeOverEnv(t)
	for _, args := range [][]string{
		{"todo", "add", "Build dev agent start --from", "--link", "https://github.com/ppf/dev/pull/282"},
		{"todo", "ask", "Merge #282?", "--option", "Merge (Recommended)", "--option", "Wait"},
		{"todo", "note", "decided", "branch takeover: picks A-G approved"},
	} {
		if _, _, err := as(t, oldThread, args...); err != nil {
			t.Fatal(err)
		}
	}
	writeList(t, lists, "tmp_tmux-1_default-boss-dev", "boss-dev", "running's", 31*time.Minute)
	writeList(t, lists, "tmp_tmux-1_default-boss-vitay", "boss-vitay", "vitay's", time.Hour)
	writeList(t, lists, "desktop-local_stop", "", "stopped's", 11*24*time.Hour)
	writeList(t, lists, "desktop-local_run", "", "running app's", 2*time.Hour)

	out := run(t, "", "todo", "take-over")
	for _, want := range []string{
		"Lists this boss (t3:5f1c2d90, dev) can take over, newest change first:\n  t3:49f62c8e     T3 Code  unknown",
		"  boss-dev        tmux     running   1 open           0 notes   changed 31m ago   ✗ running: close it first\n",
		"  local_run       desktop  running",
		"  local_stop      desktop  stopped   1 open           0 notes   changed 11d ago\n",
		"1 list of another project (boss-vitay): --any-project\nTake one: dev todo take-over <list>\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "unknown   2 open, 1 ask    1 note    changed just now\n") {
		t.Errorf("old thread's row lacks its counts:\n%s", out)
	}
	if strings.Index(out, "t3:49f62c8e") > strings.Index(out, "boss-dev") || strings.Index(out, "boss-dev") > strings.Index(out, "local_run") {
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
	if l := listing.Lists; len(l) != 4 || listing.OtherProjects != 1 || l[0].List != "t3:49f62c8e" || l[0].Kind != "t3" || l[0].Project != "dev" ||
		l[0].State != "unknown" || l[0].Open != 2 || l[0].Asks != 1 || l[0].Notes != 1 || l[0].Updated == 0 || !l[0].Takeable ||
		l[1].List != "boss-dev" || l[1].Takeable || l[1].WhyNot != "running: close it first" {
		t.Errorf("--json = %+v", listing)
	}
	if out := run(t, "", "todo", "take-over", "--any-project"); !strings.Contains(out, "  boss-vitay      tmux     closed") || strings.Contains(out, "--any-project\n") {
		t.Errorf("--any-project listing:\n%s", out)
	}

	out = run(t, "", "todo", "take-over", "t3:49f62c8e")
	want := "✅ Took over t3:49f62c8e (T3 Code, dev): 2 items, 1 note\n" +
		"  #1 → #1   Build dev agent start --from\n" +
		"  #2 → #2   Merge #282?\n" +
		"  n1 → n1   decided: branch takeover: picks A-G approved\n" +
		"⚠ 1 open ask among them: the boards can't answer a T3 Code boss's asks: name them in your next reply\n" +
		"⚠ dev can't see T3 Code's threads: archive t3:49f62c8e in T3 Code; its dev todo writes are refused from now\n" +
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
	_, _, err := as(t, oldThread, "todo", "add", "Merge #283")
	if want := "list moved to t3:5f1c2d90 at " + at + " (dev todo take-over): this boss's to do list is there now; take it back there with dev todo take-over t3:5f1c2d90"; err == nil || err.Error() != want {
		t.Errorf("old thread's add: %v, want %q", err, want)
	}
	if stdout, stderr, err := as(t, oldThread, "todo"); err != nil || stdout != "" || stderr != "⚠ dev todo: list moved to t3:5f1c2d90 at "+at+" (dev todo take-over)\n" {
		t.Errorf("old thread's read: %q %q %v", stdout, stderr, err)
	}
	if _, _, err := execute(t, "", "todo", "take-over", "t3:49f62c8e"); err == nil || err.Error() != "t3:49f62c8e moved to t3:5f1c2d90 at "+at+": nothing left to take" {
		t.Errorf("take of a moved list: %v", err)
	}
	// What the refusal says works: the old thread takes it back
	if out, _, err := as(t, oldThread, "todo", "take-over", "t3:5f1c2d90"); err != nil || !strings.HasPrefix(out, "✅ Took over t3:5f1c2d90 (T3 Code, dev): 2 items, 1 note\n") {
		t.Errorf("take back: %q %v", out, err)
	}
	if _, _, err := execute(t, "", "todo", "add", "x"); err == nil || !strings.Contains(err.Error(), "list moved to t3:49f62c8e") {
		t.Errorf("new thread's add after the take back: %v", err)
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
	writeList(t, lists, "t3-0a8cec22-3333", "", "older dev's", time.Minute)
	if err := os.WriteFile(filepath.Join(lists, "t3-eeeeeeee-4444.json"), []byte(`{"items":[],"next":1,"dir":"/p/dev"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := as(t, oldThread, "todo", "add", "theirs"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"todo", "take-over", "--boss", "boss-dev", "t3:49f62c8e"}, "take-over takes into the list of the boss that runs it: leave out --boss"},
		{[]string{"todo", "take-over", "t3:5f1c2d90"}, "t3:5f1c2d90 is this boss's own list"},
		{[]string{"todo", "take-over", "boss-dev"}, "boss-dev is running: close it first (its manager's ×, or /exit in it), then take it over"},
		{[]string{"todo", "take-over", "local_run"}, "local_run is running: close it first (/exit in it), then take it over"},
		{[]string{"todo", "take-over", "boss-vitay"}, "boss-vitay is project vitay, not dev: --any-project takes it"},
		{[]string{"todo", "take-over", "manager"}, "manager is the manager's list, not dev's: --any-project takes it"},
		{[]string{"todo", "take-over", "t3:0a8cec22"}, "t3:0a8cec22 is of no known project, not dev: --any-project takes it"},
		{[]string{"todo", "take-over", "t3:eeeeeeee"}, "t3:eeeeeeee is empty (no open items, no notes): nothing to take"},
		{[]string{"todo", "take-over", "boss-nobody"}, "no list boss-nobody: dev todo take-over lists those this boss can take"},
		{[]string{"todo", "take-over", "t3:49f6"}, "t3:49f6: give 8 or more characters of its id"},
	} {
		if _, _, err := execute(t, "", tt.args...); err == nil || err.Error() != tt.want {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
	if out, _, err := as(t, oldThread, "todo"); err != nil || !strings.Contains(out, "theirs") {
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
		"Take over another boss's to do list for this project: its items (who does each, links, --until, --done-on,\ncontext, doing, open asks), its notes, PR checks and finished items move to this boss's list, after its own\nitems, renumbered.",
		"Usage:\n  dev todo take-over [<list>] [flags]",
		"--any-project   lists of other projects, the manager's, and lists of no known project too",
		"--json          print a JSON object: without <list>, lists (each with list, kind: tmux, desktop or t3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

// A shared prefix is refused, naming each; a longer one takes; --json says what moved.
func TestTodoTakeOverAmbiguousJSON(t *testing.T) {
	lists := takeOverEnv(t)
	for _, id := range []string{"49f62c8e-1111-a", "49f62c8e-2222-b"} {
		writeList(t, lists, "t3-"+id, "", "from "+id, time.Minute)
		f := filepath.Join(lists, "t3-"+id+".json")
		data, _ := os.ReadFile(f)
		if err := os.WriteFile(f, []byte(strings.Replace(string(data), `"boss":""`, `"dir":"/p/dev"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := execute(t, "", "todo", "take-over", "t3:49f62c8e"); err == nil || err.Error() != "t3:49f62c8e matches 2 lists (49f62c8e-1111-a, 49f62c8e-2222-b): give more of its id" {
		t.Errorf("shared prefix: %v", err)
	}
	out := run(t, "", "todo", "take-over", "--json", "t3:49f62c8e-2222")
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
	if v.From.List != "t3:49f62c8e" || v.To.List != "t3:5f1c2d90" || v.To.Kind != "t3" || v.To.Project != "dev" || v.Mode != "move" ||
		len(v.Items) != 1 || v.Items[0].From != 1 || v.Items[0].To != 1 || len(v.Notes) != 0 || v.OnWorkers == nil || len(v.Warnings) != 1 {
		t.Errorf("--json = %s", out)
	}
	if got := run(t, "", "todo"); got != "1\t☐\tfrom 49f62c8e-2222-b\n" {
		t.Errorf("taken = %q", got)
	}
}

// A T3 Code boss's dev todo and dev todo note say on stderr that an earlier boss's list waits, only while its own list
// is empty and one can be taken; the old thread, its list moved, is told nothing.
func TestTodoTakeOverHint(t *testing.T) {
	takeOverEnv(t)
	if _, stderr, err := execute(t, "", "todo"); err != nil || stderr != "" {
		t.Errorf("nothing to take: %q %v, want no hint", stderr, err)
	}
	if _, _, err := as(t, oldThread, "todo", "add", "theirs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := as(t, oldThread, "todo", "note", "risk", "flaky CI"); err != nil {
		t.Fatal(err)
	}
	want := "[dev todo] An earlier boss's list for dev waits: t3:49f62c8e (1 open, 1 note, changed just now): dev todo take-over t3:49f62c8e. dev todo take-over lists all.\n"
	for _, args := range [][]string{{"todo"}, {"todo", "note"}} {
		if _, stderr, err := execute(t, "", args...); err != nil || stderr != want {
			t.Errorf("dev %s: stderr %q %v, want the hint", strings.Join(args, " "), stderr, err)
		}
	}
	if _, stderr, _ := execute(t, "", "todo", "--json"); stderr != "" {
		t.Errorf("--json: stderr %q, want no hint", stderr)
	}
	run(t, "", "todo", "add", "mine")
	if _, stderr, err := execute(t, "", "todo"); err != nil || stderr != "" {
		t.Errorf("own list not empty: %q %v, want no hint", stderr, err)
	}
	run(t, "", "todo", "take-over", "t3:49f62c8e")
	if _, stderr, _ := as(t, oldThread, "todo"); strings.Contains(stderr, "waits") {
		t.Errorf("moved list's boss: %q, want no hint", stderr)
	}
}

// A take into a list with items merges: --json says mode merge, each item's and note's numbers, the dropped state note.
func TestTodoTakeOverMergeJSON(t *testing.T) {
	takeOverEnv(t)
	for _, args := range [][]string{{"todo", "add", "theirs"}, {"todo", "note", "state", "their state"}} {
		if _, _, err := as(t, oldThread, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(t, "", "todo", "add", "mine")
	run(t, "", "todo", "note", "state", "my state")
	out := run(t, "", "todo", "take-over", "--json", "t3:49f62c8e")
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
