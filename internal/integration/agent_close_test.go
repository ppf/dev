//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dev agent close, run in a coordinator's pane, refuses a worker that isn't its own, is busy or awaiting
// a decision (until its screen shows the dialog gone), or whose worktree has uncommitted or unmerged work,
// leaving it untouched. A finished worker is closed: its untracked files land in
// $XDG_STATE_HOME/dev/results/<session>/, the attached client moves to the coordinator, and the session,
// worktree and branch are gone.
func TestAgentClose(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	root := newRepo(t, "", "")
	d.T("new-session", "-d", "-s", "coord", "sleep 600")
	coord := d.T("display", "-t", "=coord:", "-p", "#{pane_id}")
	coordID := d.T("display", "-t", "=coord:", "-p", "#{session_id}")
	devCmd := func(pane, stdin string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = d.scriptEnv(append([]string{"TMUX_PANE=" + pane}, gitEnv...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// worker opens session name in its own worktree (root for "main"), started by the coordinator if mine,
	// with hook state from event unless state is ""
	worker := func(name string, mine bool, state, event string) string {
		t.Helper()
		wt := root
		if name != "main" {
			wt = filepath.Join(root, ".claude", "worktrees", name)
			git(t, root, "worktree", "add", "-q", "-b", name, wt, "origin/main")
		}
		d.T("new-session", "-d", "-s", name, "-c", wt, "sleep 600")
		if mine {
			d.T("set-option", "-t", "="+name+":", "@dev_started_by", coordID)
		}
		pane := d.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		if state == "" {
			return wt
		}
		if out, err := devCmd(pane, event, "state", state); err != nil {
			t.Fatalf("❌ dev state %s: %v\n%s", state, err, out)
		}
		return wt
	}
	idle := `{"hook_event_name":"Stop"}`
	if out, err := devCmd(coord, idle, "state", "idle"); err != nil {
		t.Fatalf("❌ dev state idle: %v\n%s", err, out)
	}
	worker("other", false, "idle", idle)
	writeFile(t, filepath.Join(worker("dirty", true, "idle", idle), ".gitignore"), "changed")
	ahead := worker("ahead", true, "idle", idle)
	writeFile(t, filepath.Join(ahead, "f.txt"), "f")
	git(t, ahead, "add", "f.txt")
	git(t, ahead, "commit", "-qm", "f")
	worker("busy", true, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	worker("deciding", true, "waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`)
	worker("main", true, "idle", idle)
	// A shell of the user's own in the worktree
	d.T("new-session", "-d", "-s", "shell", "-c", worker("shared", true, "idle", idle), "sleep 600")
	// Its agent pane runs claude, but no hook registered it: dev agent finds no agent
	worker("nostate", true, "", "")
	// A Codex pane without its hooks: dev agent finds it, but can't tell whether it works
	worker("codex", true, "", "")
	d.T("respawn-pane", "-k", "-t", "=codex:", sleeper(t, "codex"), "600")
	waitFor(t, "codex running", func() bool { return d.T("display", "-t", "=codex:", "-p", "#{pane_current_command}") == "codex" })

	for name, want := range map[string]string{
		"other":    "other wasn't started by this session (coord)",
		"dirty":    "has uncommitted changes to tracked files",
		"ahead":    "has commits that aren't in origin/main",
		"busy":     "busy is working",
		"deciding": "waiting for a decision (permission_prompt)",
		"coord":    "coord is this agent's own pane",
		"main":     "main isn't in a .claude/worktrees worktree",
		"shared":   "another session has a pane in",
		"nostate":  `session "nostate" has no agent pane`,
		"codex":    "codex has no hook state",
	} {
		out, err := devCmd(coord, "", "agent", "close", name)
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("❌ close %s = %v, %q; want an error with %q", name, err, out, want)
		}
		if _, err := d.Try("has-session", "-t", "="+name+":"); err != nil {
			t.Errorf("❌ refused close of %s killed it", name)
		}
		if wt := filepath.Join(root, ".claude", "worktrees", name); name != "coord" && name != "main" && !strings.Contains(git(t, root, "worktree", "list"), wt) {
			t.Errorf("❌ refused close of %s removed its worktree", name)
		}
	}

	// Its dialog answered No: no hook, but Claude's input box is back in place of the dialog
	rule := strings.Repeat("─", 40)
	writeFile(t, d.T("display", "-t", "=deciding:", "-p", "#{pane_tty}"), "\x1b[2J\x1b[H"+rule+"\n❯ \n"+rule+"\n  ? for shortcuts\n")
	waitFor(t, "deciding's input box drawn", func() bool { return strings.Contains(d.T("capture-pane", "-p", "-t", "=deciding:"), "? for shortcuts") })
	// Recorded seconds ago, as Claude records a dialog seconds after drawing it
	since, _ := strconv.ParseInt(d.T("show-option", "-pqv", "-t", "=deciding:", "@agent_since"), 10, 64)
	d.T("set-option", "-p", "-t", "=deciding:", "@agent_since", strconv.FormatInt(since-3, 10))
	if out, err := devCmd(coord, "", "agent", "close", "deciding"); err != nil {
		t.Errorf("❌ close deciding after No: %v\n%s", err, out)
	}

	// Squash-merged PRs: none of the worker's commits are in origin's main. gh answers with the PR in
	// <worktree name>.json, else fails
	prs := t.TempDir()
	writeStubs(t, filepath.Join(d.tmp, "stubs"), `cat "`+prs+`/$(basename "$PWD").json" 2>/dev/null || { echo "no pull requests found" >&2; exit 1; }`, "gh")
	squash := func(name, state, base string) (wt, head string) {
		t.Helper()
		wt = worker(name, true, "idle", idle)
		writeFile(t, filepath.Join(wt, name+".txt"), name)
		git(t, wt, "add", name+".txt")
		git(t, wt, "commit", "-qm", name)
		head = strings.TrimSpace(git(t, wt, "rev-parse", "HEAD"))
		git(t, root, "merge", "-q", "--squash", name)
		git(t, root, "commit", "-qm", "squash "+name)
		git(t, root, "push", "-q", "origin", "main")
		writeFile(t, filepath.Join(prs, name+".json"), fmt.Sprintf(`{"state":%q,"headRefOid":%q,"baseRefName":%q}`, state, head, base))
		return wt, head
	}
	squash("squashed", "MERGED", "main")
	if out, err := devCmd(coord, "", "agent", "close", "squashed"); err != nil {
		t.Errorf("❌ close squashed: %v\n%s", err, out)
	}
	later, merged := squash("later", "MERGED", "main")
	writeFile(t, filepath.Join(later, "more.txt"), "more")
	git(t, later, "add", "more.txt")
	git(t, later, "commit", "-qm", "more")
	squash("open", "OPEN", "main")
	squash("elsewhere", "MERGED", "release")
	squash("nopr", "MERGED", "main")
	if err := os.Remove(filepath.Join(prs, "nopr.json")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"later":     "its PR was merged at " + merged + ", not at its HEAD " + strings.TrimSpace(git(t, later, "rev-parse", "HEAD")),
		"open":      "its PR is open",
		"elsewhere": "its PR was merged into release",
		"nopr":      "gh pr view --json state,headRefOid,baseRefName: no pull requests found",
	} {
		out, err := devCmd(coord, "", "agent", "close", name)
		if want = "has commits that aren't in origin/main, and no PR merged at its HEAD: " + want; err == nil || !strings.Contains(out, want) {
			t.Errorf("❌ close %s = %v, %q; want an error with %q", name, err, out, want)
		}
		if _, err := d.Try("has-session", "-t", "="+name+":"); err != nil {
			t.Errorf("❌ refused close of %s killed it", name)
		}
	}

	// Pushed to origin's main, then the local origin/main rewound: stale until close fetches
	done := worker("done", true, "waiting", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`)
	writeFile(t, filepath.Join(done, "g.txt"), "g")
	git(t, done, "add", "g.txt")
	git(t, done, "commit", "-qm", "g")
	git(t, done, "push", "-q", "origin", "done:main")
	git(t, root, "update-ref", "refs/remotes/origin/main", "origin/main~1")
	writeFile(t, filepath.Join(done, "RESULT.md"), "# result")
	writeFile(t, filepath.Join(done, "notes", "a.txt"), "a")
	actSwitch(d, "done")

	out, err := devCmd(coord, "", "agent", "close", "done", "--json")
	if err != nil {
		t.Fatalf("❌ close done: %v\n%s", err, out)
	}
	var got struct {
		Session, Worktree, Branch, Results string
		Files                              []string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("❌ %v: %s", err, out)
	}
	results := filepath.Join(d.tmp, "state", "dev", "results", "done")
	check(t, "--json", strings.Join([]string{got.Session, got.Worktree, got.Branch, got.Results, strings.Join(got.Files, ",")}, " "),
		strings.Join([]string{"done", done, "done", results, "RESULT.md,notes/a.txt"}, " "))
	for name, want := range map[string]string{"RESULT.md": "# result", "notes/a.txt": "a"} {
		if b, err := os.ReadFile(filepath.Join(results, name)); err != nil || string(b) != want {
			t.Errorf("❌ archived %s = %q, %v; want %q", name, b, err, want)
		}
	}
	if _, err := d.Try("has-session", "-t", "=done:"); err == nil {
		t.Error("❌ session done still there")
	}
	check(t, "client moved to the coordinator", actClientSession(d), "coord")
	check(t, "worktree removed", boolStr(strings.Contains(git(t, root, "worktree", "list"), done)), "0")
	if _, err := gitTry(root, "show-ref", "--verify", "refs/heads/done"); err == nil {
		t.Error("❌ branch done still there")
	}

	// A second close of that name keeps the first one's results; text output, no client attached
	again := worker("done", true, "idle", idle)
	writeFile(t, filepath.Join(again, "RESULT.md"), "# again")
	out, err = devCmd(coord, "", "agent", "close", "done")
	if err != nil {
		t.Fatalf("❌ close done again: %v\n%s", err, out)
	}
	check(t, "text output", out, "Saved done's untracked files to "+results+".2: RESULT.md\n"+
		"Closed done, removed worktree "+again+" and branch done\n")
	if b, err := os.ReadFile(filepath.Join(results, "RESULT.md")); err != nil || string(b) != "# result" {
		t.Errorf("❌ first results = %q, %v; want them kept", b, err)
	}

	// Outside tmux, --boss names the coordinator, once it's a boss session
	worker("remote", true, "idle", idle)
	if out, err := devCmd("", "", "agent", "close", "remote", "--boss", "coord"); err == nil || !strings.Contains(out, "coord is not a boss session") {
		t.Errorf("❌ close --boss of a plain session = %v, %q; want it refused", err, out)
	}
	d.T("set-option", "-t", "=coord:", "@dev_herd", "1")
	if out, err := devCmd("", "", "agent", "close", "remote", "--boss", "coord"); err != nil {
		t.Errorf("❌ close --boss coord: %v\n%s", err, out)
	}
	if _, err := d.Try("has-session", "-t", "=remote:"); err == nil {
		t.Error("❌ session remote still there")
	}
}
