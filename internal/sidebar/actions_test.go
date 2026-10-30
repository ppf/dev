package sidebar

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// actWorld is a fake tmux server: sessions in list-sessions order and pane rows (session_name, pane_id,
// @sidebar_pane, pane_current_path). Sidebars are hidden, so switchTo's ensure step does nothing.
type actWorld struct {
	sessions  []string
	panes     []map[string]string
	undying   bool // kill-session leaves the session in place
	exitEmpty string
	popupErr  error
	bosses    []string // sessions marked as a boss
	failCmd   string   // this command fails
	calls     [][]string
}

func (w *actWorld) client() *tmux.Client { return tmux.NewFake(w.run) }

func (w *actWorld) run(args []string) (string, error) {
	w.calls = append(w.calls, args)
	if args[0] == w.failCmd {
		return "", errors.New("boom")
	}
	target := ""
	if i := slices.Index(args, "-t"); i >= 0 {
		target = args[i+1]
	}
	switch args[0] {
	case "list-sessions":
		var rows []map[string]string
		for _, s := range w.sessions {
			rows = append(rows, map[string]string{"session_name": s})
		}
		return actRows(args, rows), nil
	case "list-panes":
		rows := slices.DeleteFunc(slices.Clone(w.panes), func(p map[string]string) bool {
			return target != "" && tmux.SessionTarget(p["session_name"]) != target
		})
		return actRows(args, rows), nil
	case "has-session":
		if slices.ContainsFunc(w.sessions, func(s string) bool { return tmux.SessionTarget(s) == target }) {
			return "", nil
		}
		return "", errors.New("can't find session")
	case "kill-session":
		if !w.undying {
			w.sessions = slices.DeleteFunc(w.sessions, func(s string) bool { return tmux.SessionTarget(s) == target })
		}
	case "display-message":
		for _, p := range w.panes {
			if p["pane_id"] == target && slices.Contains(args, "#{pane_current_path}") {
				return p["pane_current_path"] + "\n", nil
			}
		}
	case "show-option", "show-options":
		if slices.Contains(args, "exit-empty") {
			return w.exitEmpty + "\n", nil
		}
		if slices.Contains(args, optHidden) || slices.Contains(args, OptHerd) && slices.ContainsFunc(w.bosses,
			func(b string) bool { return tmux.SessionTarget(b) == target }) {
			return "1\n", nil
		}
	case "display-popup":
		return "", w.popupErr
	}
	return "", nil
}

// log is the calls so far, one line each, with -F formats elided; it resets the log.
func (w *actWorld) log() []string {
	var lines []string
	for _, args := range w.calls {
		args = slices.Clone(args)
		if i := slices.Index(args, "-F"); i >= 0 {
			args[i+1] = "…"
		}
		lines = append(lines, strings.Join(args, " "))
	}
	w.calls = nil
	return lines
}

// actRows answers a Query: one row per map, its fields taken from the map by the -F format's variables.
func actRows(args []string, rows []map[string]string) string {
	format := strings.TrimSuffix(args[slices.Index(args, "-F")+1], tmux.Sep)
	var b strings.Builder
	for _, r := range rows {
		var values []string
		for _, v := range strings.Split(format, tmux.Sep) {
			values = append(values, r[strings.TrimSuffix(strings.TrimPrefix(v, "#{"), "}")])
		}
		b.WriteString(tmux.FormatRow(values...))
	}
	return b.String()
}

// actPane is a pane row of session in dir; sidebar is the session's @sidebar_pane.
func actPane(session, id, sidebar, dir string) map[string]string {
	return map[string]string{"session_name": session, "pane_id": id, "@sidebar_pane": sidebar, "pane_current_path": dir}
}

// actWorktree creates a repo with the worktree .claude/worktrees/x, away from the user's git config.
func actWorktree(t *testing.T) (root, wt string) {
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
	root, wt = filepath.Join(dir, "repo"), filepath.Join(dir, "repo/.claude/worktrees/x")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", root},
		{"-C", root, "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", root, "worktree", "add", "-q", "-b", "x", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root, wt
}

func TestActionFocusContent(t *testing.T) {
	w := &actWorld{panes: []map[string]string{actPane("a", "%0", "%0", "/"), actPane("a", "%1", "%0", "/")}}
	if err := focusContent(w.client(), "/dev/tty1", "a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"list-panes -t =a: -F …", "select-pane -t %1", "switch-client -c /dev/tty1 -T root"}
	if got := w.log(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}

	w.panes = w.panes[:1] // only the sidebar: nothing to select
	if err := focusContent(w.client(), "", "a"); err != nil {
		t.Fatal(err)
	}
	want = []string{"list-panes -t =a: -F …", "switch-client -T root"}
	if got := w.log(); !slices.Equal(got, want) {
		t.Errorf("only the sidebar: calls = %q, want %q", got, want)
	}
}

func TestActionOpenPicker(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		client   string
		popupErr error
		want     []string
		wantErr  bool
	}{
		{"client", "/dev/tty1", nil, []string{
			"switch-client -c /dev/tty1 -T root",
			"display-popup -c /dev/tty1 -E -w 80% -h 60% -b rounded -e SIDEBAR_SELECT_IN_POPUP=1 -- '" + exe + "'",
		}, false},
		{"current client", "", nil, []string{
			"switch-client -T root",
			"display-popup -E -w 80% -h 60% -b rounded -e SIDEBAR_SELECT_IN_POPUP=1 -- '" + exe + "'",
		}, false},
		// the picker's own exit status: it already showed its error
		{"picker failed", "", &exec.ExitError{}, nil, false},
		{"client gone", "/dev/ttys999", &tmux.Error{Stderr: "can't find client: /dev/ttys999", Err: &exec.ExitError{}}, nil, true},
		{"tmux failed", "", errors.New("no client"), nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{popupErr: tt.popupErr}
			if err := OpenPicker(w.client(), tt.client); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %t", err, tt.wantErr)
			}
			if got := w.log(); tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("calls = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActionKill(t *testing.T) {
	root, wt := actWorktree(t)
	if err := os.MkdirAll(wt+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	killB := killCalls(t, "=b:")
	plainKill := slices.Concat([]string{
		"list-sessions -F …",
		"list-panes -t =b: -F …", "display-message -t %2 -p #{pane_current_path}",
		"list-sessions -F …"}, killB, []string{"has-session -t =b:", "has-session -t =a:",
		"set-option -gq @sidebar_selected_session a",
	})
	for _, tt := range []struct {
		name     string
		sessions []string
		target   string
		bDir     string // b's content pane
		cDir     string // c's pane
		want     []string
	}{
		{"no target", []string{"a", "b"}, "", "/", "/", nil},
		{"last session", []string{"b"}, "b", "/", "/", append([]string{
			"list-sessions -F …",
			"list-panes -t =b: -F …", "display-message -t %2 -p #{pane_current_path}",
			"list-sessions -F …", "show-option -gv exit-empty"}, killB...)},
		{"gone: select current", []string{"a", "c"}, "b", "/", "/", []string{
			"list-sessions -F …", "set-option -gq @sidebar_selected_session a",
		}},
		{"not a worktree", []string{"a", "b", "c"}, "b", "/", "/", plainKill},
		{"unshared worktree", []string{"a", "b", "c"}, "b", wt + "/sub", wt + "2", []string{
			"list-sessions -F …",
			"list-panes -t =b: -F …", "display-message -t %2 -p #{pane_current_path}", "list-panes -a -F …",
			"display-popup -c /dev/tty1 -E -w 70 -h 8 -b rounded -- " +
				tmux.ShellJoin(exe, "sidebar", "close-worktree", "--client", "/dev/tty1", "--", "a", "b", root, wt),
		}},
		{"shared worktree", []string{"a", "b", "c"}, "b", wt, wt + "/sub", append(append(plainKill[:3:3], "list-panes -a -F …"), plainKill[3:]...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{sessions: tt.sessions, panes: []map[string]string{
				actPane("a", "%0", "%0", "/"), actPane("b", "%1", "%1", tt.bDir), actPane("b", "%2", "%1", tt.bDir),
				actPane("c", "%3", "", tt.cDir),
			}}
			if err := kill(w.client(), "/dev/tty1", "a", tt.target); err != nil {
				t.Fatal(err)
			}
			if got := w.log(); !slices.Equal(got, tt.want) {
				t.Errorf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestActionWorktreeShared(t *testing.T) {
	const wt = "/r/.claude/worktrees/wt"
	for _, tt := range []struct {
		session, path string
		want          bool
	}{
		{"other", wt, true},
		{"other", wt + "/sub/dir", true},
		{"other", wt + "2", false}, // compared literally, not as a prefix
		{"other", "/r", false},
		{"self", wt, false},
	} {
		w := &actWorld{panes: []map[string]string{actPane("self", "%1", "", wt), actPane(tt.session, "%2", "", tt.path)}}
		got, err := WorktreeShared(w.client(), "self", wt)
		if err != nil || got != tt.want {
			t.Errorf("%s in %s: shared = %t, %v; want %t", tt.session, tt.path, got, err, tt.want)
		}
	}
}

func TestActionSessionWorktree(t *testing.T) {
	root, wt := actWorktree(t)
	w := &actWorld{panes: []map[string]string{actPane("s", "%1", "%1", "/"), actPane("s", "%2", "%1", wt+"/sub")}}
	if err := os.MkdirAll(wt+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	gotRoot, gotWt, ok, err := sessionWorktree(w.client(), "s")
	if err != nil || !ok || gotRoot != root || gotWt != wt {
		t.Errorf("sessionWorktree = %q, %q, %t, %v; want %q, %q", gotRoot, gotWt, ok, err, root, wt)
	}

	w.panes = w.panes[:1]
	if _, _, ok, err := sessionWorktree(w.client(), "s"); ok || err != nil {
		t.Errorf("no content pane: ok %t, err %v; want neither", ok, err)
	}
}

func TestActionKillNow(t *testing.T) {
	for _, tt := range []struct {
		name            string
		sessions        []string
		current, target string
		undying         bool
		killed          bool
		want            []string
	}{
		{"current: switch first, select before the kill", []string{"a", "b", "c"}, "b", "b", false, true, []string{
			"list-sessions -F …",
			"switch-client -c /dev/tty1 -t =a:", "switch-client -c /dev/tty1 -T root",
			"set-option -gq @sidebar_selected_session a",
			"kill-session -t =b:", "has-session -t =b:", "has-session -t =a:",
			"set-option -gq @sidebar_selected_session a",
		}},
		{"last session: nothing to switch to or select", []string{"a"}, "a", "a", false, true, []string{
			"list-sessions -F …", "show-option -gv exit-empty", "kill-session -t =a:",
		}},
		{"other: current stays selected", []string{"a", "b"}, "a", "b", false, true, []string{
			"list-sessions -F …", "kill-session -t =b:", "has-session -t =b:", "has-session -t =a:",
			"set-option -gq @sidebar_selected_session a",
		}},
		{"still there", []string{"a", "b"}, "a", "b", true, false, []string{
			"list-sessions -F …", "kill-session -t =b:", "has-session -t =b:", "has-session -t =a:",
			"set-option -gq @sidebar_selected_session a",
		}},
		{"current gone: select the first", []string{"b", "c"}, "a", "b", false, true, []string{
			"list-sessions -F …", "kill-session -t =b:", "has-session -t =b:", "has-session -t =a:",
			"list-sessions -F …", "set-option -gq @sidebar_selected_session c",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{sessions: tt.sessions, undying: tt.undying}
			killed, err := killNow(w.client(), "/dev/tty1", tt.current, tt.target)
			if err != nil || killed != tt.killed {
				t.Errorf("killNow = %t, %v; want %t", killed, err, tt.killed)
			}
			var want []string
			for _, call := range tt.want {
				if target, ok := strings.CutPrefix(call, "kill-session -t "); ok {
					want = append(want, killCalls(t, target)...)
					continue
				}
				want = append(want, call)
			}
			if got := w.log(); !slices.Equal(got, want) {
				t.Errorf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A kill that fails leaving the session is reported; one gone anyway is not (bash's `|| true`).
func TestActionKillNowFails(t *testing.T) {
	for _, tt := range []struct {
		name    string
		undying bool
		wantErr bool
	}{
		{"session stays", true, true},
		{"session gone anyway", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{sessions: []string{"a", "b"}, failCmd: "kill-session", undying: tt.undying}
			if !tt.undying {
				w.sessions = []string{"a"}
			}
			killed, err := killNow(w.client(), "/dev/tty1", "a", "b")
			if killed == tt.wantErr || (err != nil) != tt.wantErr {
				t.Errorf("killNow = %t, %v; want killed %t, error %t", killed, err, !tt.wantErr, tt.wantErr)
			}
		})
	}
}

// killCalls is killSession's call when the server outlives target (another session left, or exit-empty off).
func killCalls(t *testing.T, target string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"kill-session -t " + target + " ; run-shell -b " + tmux.ShellJoin(exe, "resurrect", "snapshot")}
}

func TestKillSession(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := tmux.ShellJoin(exe, "resurrect", "snapshot")
	for _, tt := range []struct {
		name      string
		sessions  []string
		exitEmpty string
		want      []string
	}{
		{"another session left: snapshot once it's gone", []string{"a", "b"}, "on", []string{
			"kill-session -t =b: ; run-shell -b " + snapshot}},
		{"last session: the server waits for the snapshot", []string{"b"}, "on", []string{"show-option -gv exit-empty",
			"set-option -g exit-empty off ; run-shell -b 'tmux' 'kill-session' '-t' '=b:'; " + snapshot + "; tmux set-option -g exit-empty on"}},
		{"a # in the name stays literal in run-shell", []string{"b#1"}, "on", []string{"show-option -gv exit-empty",
			"set-option -g exit-empty off ; run-shell -b 'tmux' 'kill-session' '-t' '=b##1:'; " + snapshot + "; tmux set-option -g exit-empty on"}},
		{"last session, exit-empty off: the server stays anyway", []string{"b"}, "off", []string{"show-option -gv exit-empty",
			"kill-session -t =b: ; run-shell -b " + snapshot}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{sessions: tt.sessions, exitEmpty: tt.exitEmpty}
			// The last listed session is the one closed
			if err := KillSession(w.client(), tt.sessions[len(tt.sessions)-1]); err != nil {
				t.Fatal(err)
			}
			want := append([]string{"list-sessions -F …"}, tt.want...)
			if got := w.log(); !slices.Equal(got, want) {
				t.Errorf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func TestActionRename(t *testing.T) {
	for _, tt := range []struct {
		client, target string
		want           []string
	}{
		{"/dev/tty1", "app", []string{"command-prompt", "-t", "/dev/tty1", "-I", "app", "-p", "Rename session:",
			"rename-session -t '=app:' -- '%%'"}},
		{"", "it's", []string{"command-prompt", "-I", "it's", "-p", "Rename session:",
			`rename-session -t '=it'"'"'s:' -- '%%'`}},
	} {
		w := &actWorld{}
		if err := rename(w.client(), tt.client, tt.target); err != nil {
			t.Fatal(err)
		}
		if len(w.calls) != 2 || !slices.Equal(w.calls[1], tt.want) {
			t.Errorf("calls = %q, want %q", w.calls, tt.want)
		}
	}
	boss := &actWorld{bosses: []string{"boss-app"}}
	if err := rename(boss.client(), "/dev/tty1", "boss-app"); err != nil || len(boss.calls) != 2 ||
		!slices.Equal(boss.calls[1][:3], []string{"display-message", "-c", "/dev/tty1"}) {
		t.Errorf("renaming a boss: err %v, calls %q", err, boss.calls)
	}
	w := &actWorld{}
	if err := rename(w.client(), "", ""); err != nil || len(w.calls) != 0 {
		t.Errorf("no target: err %v, calls %q", err, w.calls)
	}
}
