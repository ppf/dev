package cmd

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/serve"
)

// The user's doing refuses a Claude Code session (not a board's button), no --question, --boss, a bad number, a
// doing line with --stop and two lines before reaching tmux; the boss's refuses two lines too.
func TestTodoDoingRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		claude string
		args   []string
		want   string
	}{
		{"1", []string{"todo", "doing", "boss-dev", "1", "--question", "a"}, "refused in a Claude Code session"},
		{"", []string{"todo", "doing", "boss-dev", "1"}, "--question <the item's text> is needed"},
		{"", []string{"todo", "doing", "boss-dev", "1", "--question", "a", "--boss", "boss-x"}, "leave out --boss"},
		{"", []string{"todo", "doing", "boss-dev", "--question", "a"}, "no item number"},
		{"", []string{"todo", "doing", "boss-dev", "x", "--question", "a"}, `item number "x"`},
		{"", []string{"todo", "doing", "boss-dev", "1", "on it", "--stop", "--question", "a"}, "--stop takes no doing line"},
		{"1", []string{"todo", "doing", "1", "on", "it"}, "one doing line: quote it"},
		{"1", []string{"todo", "doing", "1", "on it", "--stop"}, "--stop takes no doing line"},
		{"", []string{"todo", "doing", "1", "--question", "a"}, "--question is the user's form"},
	} {
		t.Setenv("CLAUDECODE", tt.claude)
		if _, _, err := execute(t, "", tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(tt.args, " "), err, tt.want)
		}
	}
}

// A boss marks its own item doing in its desktop app session, refused on the user's; the user's form on a desktop app
// boss's list marks it and tells no one.
func TestTodoDoingDesktop(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "local_boss")
	t.Setenv("CLAUDECODE", "1")
	for _, args := range [][]string{{"todo", "add", "mine"}, {"todo", "add", "parked", "--on", "boss"}} {
		if _, _, err := execute(t, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := execute(t, "", "todo", "doing", "1", "x"); err == nil || !strings.Contains(err.Error(), "isn't your own (--on boss)") {
		t.Errorf("boss on the user's item: %v", err)
	}
	if out, _, err := execute(t, "", "todo", "doing", "2", "drafting it"); err != nil || out != "#2 in progress\n" {
		t.Errorf("boss doing = %q, %v", out, err)
	}
	if out, _, err := execute(t, "", "todo", "--json"); err != nil || !strings.Contains(out, `"doing": {`) || !strings.Contains(out, `"text": "drafting it"`) {
		t.Errorf("--json after doing: %v\n%s", err, out)
	}
	t.Setenv("CLAUDECODE", "")
	out, stderr, err := execute(t, "", "todo", "doing", "local_boss", "1", "sending it", "--question", "mine")
	if err != nil || out != "#1 in progress, no one told\n" || !strings.Contains(stderr, "no dev inbox") {
		t.Errorf("user doing on a desktop boss = %q %q, %v", out, stderr, err)
	}
	if out, _, err := execute(t, "", "todo", "doing", "local_boss", "1", "--stop", "--question", "mine"); err != nil || out != "#1 back in to do, no one told\n" {
		t.Errorf("user stop = %q, %v", out, err)
	}
}

// add --part-of links the new item to an open one, both shown in --json; one linked already is refused.
func TestTodoAddPartOf(t *testing.T) {
	todoEnv(t)
	run(t, "", "todo", "add", "send the cert")
	if out := run(t, "", "todo", "add", "install the cert", "--on", "boss", "--part-of", "1"); out != "added 2 · to do 2\n" {
		t.Errorf("add --part-of = %q", out)
	}
	out := strings.Join(strings.Fields(run(t, "", "todo", "--json")), "")
	if !strings.Contains(out, `"id":1,"text":"sendthecert"`) || strings.Count(out, `"part_of":`) != 2 || !strings.Contains(out, `"part_of":2`) || !strings.Contains(out, `"part_of":1`) {
		t.Errorf("--json: %s", out)
	}
	if _, _, err := execute(t, "", "todo", "add", "x", "--part-of", "1"); err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Errorf("second link: %v", err)
	}
}

// The page's doing: no list and no session is 404, an item not the user's 409; no tmux server reached.
func TestPageDoing(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("CLAUDECODE", "1")
	run(t, "", "todo", "add", "theirs", "--waiting-for", "Carl")
	var ref serve.Refused
	if err := pageDoing(io.Discard)("boss-nope", 1, "x", "y", false); !errors.As(err, &ref) || ref.Code != http.StatusNotFound {
		t.Errorf("no list: %v, want 404", err)
	}
	if err := pageDoing(io.Discard)("local_boss", 1, "theirs", "y", false); !errors.As(err, &ref) || ref.Code != http.StatusConflict || !strings.Contains(err.Error(), "not the user's") {
		t.Errorf("not the user's: %v, want 409", err)
	}
}
