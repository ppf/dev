package sidebar

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// closeWorktree creates a repo with worktree feat, keeping the user's git config (hooks, signing) out.
func closeWorktree(t *testing.T) (root, wt string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "dev-test", "GIT_AUTHOR_EMAIL": "dev-test@example.com",
		"GIT_COMMITTER_NAME": "dev-test", "GIT_COMMITTER_EMAIL": "dev-test@example.com",
	} {
		t.Setenv(k, v)
	}
	// git reports realpaths (/private/var on macOS)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(dir, "repo")
	for _, args := range [][]string{{"init", "-q", "-b", "main", root}, {"-C", root, "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wt, err = worktree.Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return root, wt
}

func closeKey(b byte) func() (byte, error) {
	return func() (byte, error) { return b, nil }
}

func TestClosePrompt(t *testing.T) {
	for _, key := range []byte{0x1b, '\r', 'q'} {
		c, log := ensTmux(nil)
		var out bytes.Buffer
		a := CloseArgs{Client: "/dev/a", Current: "app", Target: "app-x", MainRoot: "/r", Worktree: "/r/.claude/worktrees/x/y"}
		if err := CloseWorktree(c, a, &out, closeKey(key)); err != nil {
			t.Fatal(err)
		}
		want := "\n  Close session app-x?\n\n" +
			"  [y] close + remove worktree x/y (discards uncommitted changes)\n" +
			"  [n] close, keep worktree\n" +
			"  [Esc] cancel\n"
		if out.String() != want {
			t.Errorf("prompt = %q, want %q", out.String(), want)
		}
		if len(log()) != 0 {
			t.Errorf("key %q: calls %q, want none", key, log())
		}
	}

	c, _ := ensTmux(nil)
	keyErr := errors.New("no tty")
	err := CloseWorktree(c, CloseArgs{Target: "app-x"}, &bytes.Buffer{}, func() (byte, error) { return 0, keyErr })
	if !errors.Is(err, keyErr) {
		t.Errorf("readKey error: got %v", err)
	}
}

func TestCloseKill(t *testing.T) {
	sessions := "list-sessions -F #{session_name}\x1f#{@project}\x1f#{session_id}\x1f#{@dev_started_by}\x1f#{@dev_herd}\x1f"
	row := func(name string) string { return tmux.FormatRow(name, "", "", "", "") }
	twoSessions := map[string]string{sessions: row("app") + row("app-x")}
	gone := "has-session -t =app-x:"

	tests := []struct {
		name        string
		key         byte
		current     string
		out         map[string]string
		fail        []string
		badWorktree bool
		kept        bool // not killed
		removed     bool
		message     string // last command, "" = no message
	}{
		{name: "y closes and removes", key: 'y', current: "app", out: twoSessions, fail: []string{gone},
			removed: true, message: "display-message -c /dev/a Removed worktree feat"},
		{name: "Y too", key: 'Y', current: "app", out: twoSessions, fail: []string{gone},
			removed: true, message: "display-message -c /dev/a Removed worktree feat"},
		{name: "y on the last session removes the worktree before the kill", key: 'y', current: "app-x",
			out: map[string]string{sessions: row("app-x")}, removed: true},
		{name: "y on the last session with a failing remove keeps it", key: 'y', current: "app-x",
			out: map[string]string{sessions: row("app-x")}, badWorktree: true, kept: true,
			message: "display-message -c /dev/a ❌ worktree remove failed: "},
		{name: "y with the session surviving kill keeps the worktree", key: 'y', current: "app", out: twoSessions},
		{name: "y with a failing remove", key: 'y', current: "app", out: twoSessions, fail: []string{gone},
			badWorktree: true, message: "display-message -c /dev/a ❌ worktree remove failed: "},
		{name: "n closes and keeps", key: 'n', current: "app", out: twoSessions, fail: []string{gone}},
		{name: "N too", key: 'N', current: "app", out: twoSessions, fail: []string{gone}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, wt := closeWorktree(t)
			a := CloseArgs{Client: "/dev/a", Current: tt.current, Target: "app-x", MainRoot: root, Worktree: wt}
			if tt.badWorktree {
				a.Worktree = filepath.Join(root, ".claude/worktrees/feat-missing")
			}
			c, log := ensTmux(tt.out, tt.fail...)
			if err := CloseWorktree(c, a, &bytes.Buffer{}, closeKey(tt.key)); err != nil {
				t.Fatal(err)
			}
			calls := log()
			if killed := slices.ContainsFunc(calls, func(call string) bool {
				return strings.HasPrefix(call, "kill-session -t =app-x: ")
			}); killed == tt.kept {
				t.Errorf("app-x killed = %t, want %t: %q", killed, !tt.kept, calls)
			}
			if _, err := os.Stat(wt); (err != nil) != tt.removed {
				t.Errorf("worktree removed = %v, want %v", err != nil, tt.removed)
			}
			last := calls[len(calls)-1]
			isMessage := strings.HasPrefix(last, "display-message -c /dev/a ")
			if tt.message == "" && isMessage || tt.message != "" && !strings.HasPrefix(last, tt.message) {
				t.Errorf("last call = %q, want message %q", last, tt.message)
			}
		})
	}
}
