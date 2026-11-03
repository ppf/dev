//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestExitCloses: a clean agent exit closes its session for good like the sidebar's ×: an archive record,
// a snapshot without it, and for a worktree the close-worktree popup on an attached client (none attached:
// the worktree is kept, and the record says so). A crash leaves the session.
func TestExitCloses(t *testing.T) {
	requireFiles(t, filepath.Join(bashHome, ".tmux/plugins/tmux-resurrect/scripts/save.sh"))
	d := newDevServer(t, "set -g @dev_notify off\n")
	d.T("set-option", "-g", "@resurrect-hook-post-save-layout", devBin+" resurrect save")
	stubs := filepath.SplitList(d.path)[0]
	// Claude's hooks around one line of input: "crash" exits 3, anything else (or EOF) exits cleanly
	writeStubs(t, stubs, `hook() { printf '{"hook_event_name":"%s","session_id":"c1","transcript_path":"/t/c1.jsonl"}' "$2" | `+shellQuote(devBin)+` state "$1"; }
hook idle SessionStart
read -r line
hook clear SessionEnd
[ "$line" = crash ] && exit 3
exit 0`, "claude")
	repo := newRepo(t, "", "")
	dir := tempDir(t)
	env := d.scriptEnv(append(gitEnv, writeProjects(t, [2]string{"app", dir}, [2]string{"repo", repo}))...)
	start := func(args ...string) string {
		t.Helper()
		out, errOut, status := devAgent(env, "", append(append([]string{"start"}, args...), "--json", "--timeout", "30s")...)
		var a map[string]string
		if err := json.Unmarshal([]byte(out), &a); err != nil || status != "0" {
			t.Fatalf("start %v: exit %s, %v: %q %q", args, status, err, out, errOut)
		}
		return a["pane"]
	}
	gone := func(session string) {
		t.Helper()
		waitFor(t, session+" closed", func() bool { _, err := d.Try("has-session", "-t", "="+session+":"); return err != nil })
	}
	record := func(session string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(d.tmp, "state/dev/archive", session, "session.json"))
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Keys of r, all strings but worktree_removed
	fields := func(r map[string]any, keys ...string) string {
		var out []string
		for _, k := range keys {
			v := r[k]
			if b, ok := v.(bool); ok || v == nil {
				v = strconv.FormatBool(b)
			}
			out = append(out, k+"="+v.(string))
		}
		return strings.Join(out, " ")
	}

	pane := start("app")
	d.T("run-shell", devBin+" resurrect snapshot")
	check(t, "saved with app", d.saved(), "app boot")
	d.T("send-keys", "-t", pane, "C-d")
	gone("app")
	check(t, "record", fields(record("app"), "session", "path", "conversation", "transcript", "worktree_removed"),
		"session=app path="+dir+" conversation=c1 transcript=/t/c1.jsonl worktree_removed=false")
	actWait(d, "saved without app", d.saved, "boot")

	pane = start("app")
	d.T("send-keys", "-t", pane, "crash", "Enter")
	waitFor(t, "crash: back at the shell", func() bool {
		return d.T("display", "-t", pane, "-p", "#{pane_current_command}") != filepath.Base(devBin)
	})
	check(t, "crash: app stays", d.T("list-sessions", "-F", "#{session_name}"), "app\nboot")
	if _, err := os.Stat(filepath.Join(d.tmp, "state/dev/archive/app.2")); err == nil {
		t.Error("crash: archived")
	}
	d.T("kill-session", "-t", "=app:")

	// No client attached: closed, worktree kept
	pane = start("repo", "lone")
	lone := filepath.Join(repo, ".claude/worktrees/lone")
	d.T("send-keys", "-t", pane, "C-d")
	gone("repo-lone")
	check(t, "no client: worktree kept", exists(lone), "present")
	check(t, "no client: record", fields(record("repo-lone"), "worktree", "worktree_removed", "note"),
		"worktree="+lone+" worktree_removed=false note=worktree kept: no client attached to ask about it")

	// The client on it is asked, as by ×
	for _, tt := range []struct{ key, worktree, removed string }{{"n", "present", "false"}, {"y", "absent", "true"}} {
		name := "asked-" + tt.key
		pane = start("repo", name)
		d.T("switch-client", "-t", "=repo-"+name+":")
		d.T("send-keys", "-t", pane, "C-d")
		waitScreen(t, d.outer, "out", "Close session repo-"+name+"?")
		d.outer.T("send-keys", "-t", "out", tt.key)
		gone("repo-" + name)
		check(t, tt.key+": worktree", exists(filepath.Join(repo, ".claude/worktrees", name)), tt.worktree)
		check(t, tt.key+": record", fields(record("repo-"+name), "worktree_removed"), "worktree_removed="+tt.removed)
		check(t, tt.key+": client moved", d.T("list-clients", "-F", "#{client_session}"), "boot")
	}
}
