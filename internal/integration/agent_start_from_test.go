//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dev agent start --from, run in a boss's pane, closes its idle worker and starts a fresh Claude of the same name
// in the worker's worktree, on the new model and effort: commits, changes to tracked files and untracked files
// stay, and so does the to do item on the worker. A worker that is busy, deciding, someone else's, a boss, not in a
// .claude/worktrees worktree or in a worktree another session uses is refused and left as it was.
func TestAgentStartFrom(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	root := newRepo(t, "", "")
	d.T("new-session", "-d", "-s", "coord", "sleep 600")
	d.T("set-option", "-t", "=coord:", "@dev_herd", "1")
	coord := d.T("display", "-t", "=coord:", "-p", "#{pane_id}")
	coordID := d.T("display", "-t", "=coord:", "-p", "#{session_id}")
	env := d.scriptEnv(append(gitEnv, writeProjects(t, [2]string{"repo", root}), "TMUX_PANE="+coord)...)

	out, errOut, status := devAgent(env, "", "start", "repo", "fix", "--model", "sonnet", "--effort", "medium", "--json", "--timeout", "30s")
	if status != "0" {
		t.Fatalf("❌ start repo fix: exit %s: %s", status, errOut)
	}
	var old map[string]string
	if err := json.Unmarshal([]byte(out), &old); err != nil {
		t.Fatalf("❌ %v: %s", err, out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "fix")
	transcript := filepath.Join(d.tmp, "s0.jsonl")
	d.T("set-option", "-p", "-t", old["pane"], "@agent_transcript", transcript)
	oldID := d.T("display", "-t", "=repo-fix:", "-p", "#{session_id}")
	writeFile(t, filepath.Join(wt, "f.txt"), "f")
	git(t, wt, "add", "f.txt")
	git(t, wt, "commit", "-qm", "f")
	head := git(t, wt, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(wt, ".gitignore"), "changed")
	writeFile(t, filepath.Join(wt, "notes.txt"), "notes")
	todo := func(args ...string) string {
		t.Helper()
		o, e, s := runDev(env, "", append([]string{"todo"}, args...)...)
		if s != "0" {
			t.Fatalf("❌ dev todo %q: exit %s: %s", args, s, e)
		}
		return o
	}
	todo("add", "Fix login", "--on", "repo-fix")

	// Refusals: each leaves its worker as it was
	idle := `{"hook_event_name":"Stop"}`
	state := func(name, st, event string) {
		t.Helper()
		pane := d.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		if _, e, s := runDev(append(env, "TMUX_PANE="+pane), event, "state", st); s != "0" {
			t.Fatalf("❌ dev state %s: %s", st, e)
		}
	}
	// worker opens session name in its own worktree (root for "main"), started by the coordinator if mine
	worker := func(name string, mine bool, st, event string) string {
		t.Helper()
		dir := root
		if name != "main" {
			dir = filepath.Join(root, ".claude", "worktrees", name)
			git(t, root, "worktree", "add", "-q", "-b", name, dir, "origin/main")
		}
		d.T("new-session", "-d", "-s", name, "-c", dir, "sleep 600")
		d.T("set-option", "-t", "="+name+":", "@project", "repo")
		if mine {
			d.T("set-option", "-t", "="+name+":", "@dev_started_by", coordID)
		}
		state(name, st, event)
		return dir
	}
	worker("busy", true, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	worker("deciding", true, "waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`)
	worker("other", false, "idle", idle)
	worker("sub", true, "idle", idle)
	d.T("set-option", "-t", "=sub:", "@dev_herd", "1")
	worker("main", true, "idle", idle)
	d.T("new-session", "-d", "-s", "shell", "-c", worker("shared", true, "idle", idle), "sleep 600")
	worker("parent", true, "idle", idle)
	d.T("new-session", "-d", "-s", "child", "sleep 600")
	d.T("set-option", "-t", "=child:", "@dev_started_by", d.T("display", "-t", "=parent:", "-p", "#{session_id}"))
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"busy", nil, "❌ busy is working: wait for it first\n"},
		{"deciding", nil, "❌ deciding is waiting for a decision (permission_prompt): answer it first\n"},
		{"other", nil, `❌ other wasn't started by this session (coord), but by "": take over only your own workers` + "\n"},
		{"nope", nil, `❌ no session "nope"` + "\n"},
		{"sub", nil, "❌ sub is a boss: --from takes over workers only\n"},
		{"main", nil, "❌ main isn't in a .claude/worktrees worktree (" + root + ")\n"},
		{"shared", nil, "❌ another session has a pane in " + filepath.Join(root, ".claude", "worktrees", "shared") + "\n"},
		{"parent", nil, "❌ parent still has workers (child): it closes them first\n"},
		{"repo-fix", []string{"repo", "fix-2"}, "❌ --from takes no <project> or [worktree]: the new session takes repo-fix's\n"},
		{"repo-fix", []string{"--agent", "codex"}, "❌ --from starts a fresh Claude, not --agent codex\n"},
	} {
		out, errOut, status := devAgent(env, "", append([]string{"start", "--from", tc.name, "--timeout", "30s"}, tc.args...)...)
		check(t, "start --from "+tc.name+" "+strings.Join(tc.args, " "), status+" "+out+errOut, "1 "+tc.want)
		if _, err := d.Try("has-session", "-t", "="+tc.name+":"); err != nil && tc.name != "nope" {
			t.Errorf("❌ refused takeover of %s killed it", tc.name)
		}
	}
	check(t, "refusals kept the worker", d.T("display", "-t", "=repo-fix:", "-p", "#{session_id}"), oldID)

	out, errOut, status = devAgent(env, "", "start", "--from", "repo-fix", "--model", "opus", "--effort", "high", "--json", "--timeout", "30s")
	if status != "0" {
		t.Fatalf("❌ start --from repo-fix: exit %s: %s", status, errOut)
	}
	var got struct {
		Session, Pane, Path, Model, Effort string
		StartedBy                          string `json:"started_by"`
		TookOver                           struct {
			Session, Worktree, Branch, Base, Transcript string
			Ahead                                       int
			Dirty                                       []string
		} `json:"took_over"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("❌ %v: %s", err, out)
	}
	check(t, "new agent", strings.Join([]string{got.Session, got.Path, got.StartedBy, got.Model, got.Effort}, " "),
		strings.Join([]string{"repo-fix", wt, "coord", "opus", "high"}, " "))
	check(t, "took_over", strings.Join([]string{got.TookOver.Session, got.TookOver.Worktree, got.TookOver.Branch, got.TookOver.Base, got.TookOver.Transcript,
		strings.Join(got.TookOver.Dirty, ","), strconv.Itoa(got.TookOver.Ahead)}, " "),
		strings.Join([]string{"repo-fix", wt, "fix", "origin/main", transcript, ".gitignore,notes.txt", "1"}, " "))
	for _, want := range []string{
		"ℹ  Taking over repo-fix (sonnet, medium): branch fix, 1 commit ahead of origin/main, 2 uncommitted files (kept)\n",
		"Closed repo-fix, kept worktree " + wt + "\n",
		"Handover: its transcript " + transcript + "\n",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("❌ stderr %q, want %q in it", errOut, want)
		}
	}
	if newID := d.T("display", "-t", "=repo-fix:", "-p", "#{session_id}"); newID == oldID {
		t.Errorf("❌ session id %s unchanged: not a new session", newID)
	}
	check(t, "recorded on the session", d.T("show-option", "-qv", "-t", "=repo-fix:", "@dev_model")+" "+d.T("show-option", "-qv", "-t", "=repo-fix:", "@dev_effort"), "opus high")
	if args := agentArgs(t, d, got.Pane); !strings.Contains(args, "claude --model opus --effort high") {
		t.Errorf("❌ claude not started with the new model and effort: %q", args)
	}
	check(t, "HEAD", git(t, wt, "rev-parse", "HEAD"), head)
	check(t, "branch", git(t, wt, "branch", "--show-current"), "fix\n")
	check(t, "changes", git(t, wt, "status", "--porcelain"), " M .gitignore\n?? notes.txt\n")
	record, err := os.ReadFile(filepath.Join(d.tmp, "state/dev/archive/repo-fix/session.json"))
	if err != nil || strings.Contains(string(record), "worktree_removed") || !strings.Contains(string(record), `"worktree": "`+wt+`"`) ||
		!strings.Contains(string(record), `"note": "taken over by a fresh worker"`) || !strings.Contains(string(record), `"transcript": "`+transcript+`"`) {
		t.Errorf("❌ archive record: %v %s", err, record)
	}
	if list := todo(); !strings.Contains(list, "Fix login") || !strings.Contains(list, "on repo-fix") || strings.Contains(list, "session gone") {
		t.Errorf("❌ to do list %q, want Fix login still on the live repo-fix", list)
	}
	out, _, _ = devAgent(env, "", "list", "--mine")
	if !slices.Contains(strings.Split(out, "\n"), strings.Join([]string{"repo-fix", got.Pane, "Claude", "idle", "fix", wt, "-"}, "\t")) {
		t.Errorf("❌ list --mine = %q, want the new repo-fix in it", out)
	}

	// The new session not registered in time: exit 2, the worktree kept and the plain retry named
	if _, errOut, status := devAgent(env, "", "start", "repo", "late", "--json", "--timeout", "30s"); status != "0" {
		t.Fatalf("❌ start repo late: exit %s: %s", status, errOut)
	}
	// A claude that never registers
	if err := os.Rename(sleeper(t, "claude"), filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	_, errOut, status = devAgent(env, "", "start", "--from", "repo-late", "--model", "opus", "--timeout", "2s")
	if want := "❌ closed repo-late and kept its worktree and branch late, but the new session's agent didn't register: answer its screen (dev agent read repo-late), " +
		"or have the user close it (× → keep worktree) and retry with\n   dev agent start repo late --model opus: timed out"; status != "2" || !strings.Contains(errOut, want) {
		t.Errorf("❌ takeover timing out = exit %s, %q; want 2 and %q", status, errOut, want)
	}
	if _, err := gitTry(root, "show-ref", "--verify", "refs/heads/late"); err != nil {
		t.Errorf("❌ branch late gone: %v", err)
	}
	// The retry: a plain start reusing the worktree, its session still this coordinator's worker
	d.T("kill-session", "-t", "=repo-late:")
	// Unlinked first: Linux refuses to write over a running executable
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	out, errOut, status = devAgent(env, "", "start", "repo", "late", "--model", "opus", "--json", "--timeout", "30s")
	var late map[string]string
	if err := json.Unmarshal([]byte(out), &late); err != nil || status != "0" {
		t.Fatalf("❌ retry: exit %s, %v: %q %q", status, err, out, errOut)
	}
	check(t, "retry", late["path"]+" "+late["branch"]+" "+late["started_by"], filepath.Join(root, ".claude", "worktrees", "late")+" late coord")
}

// runDev runs dev args with env and stdin and returns stdout, stderr and the exit status.
func runDev(env []string, stdin string, args ...string) (string, string, string) {
	cmd := exec.Command(devBin, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), errStatus(err)
}
