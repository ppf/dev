//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A T3 Code thread takes over the list of dev boss boss-dev once it is closed: dev todo there shows its items, the one
// on the old boss's worker as session gone; a boss-dev opened again afterwards starts with an empty list of its own.
func TestTodoTakeOver(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "dev")
	config := filepath.Join(tmp, "config")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(config, "dev", "projects.toml"), "[[project]]\nname = \"dev\"\npath = \""+project+"\"\n")
	s := newServer(t, "set -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"), "XDG_CONFIG_HOME="+config, "HOME="+tmp)
	dev := func(env []string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		// Outside git: the folder is a T3 Code boss's main checkout
		cmd.Dir = project
		cmd.Env = s.scriptEnv(append([]string{"CLAUDECODE=1", "CLAUDE_CODE_HOST_SESSION_ID="}, env...)...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	openBoss := func() []string {
		s.T("new-session", "-d", "-s", "boss-dev", "-x", "120", "-y", "40", "sleep", "600")
		s.T("set-option", "-t", "=boss-dev:", "@dev_herd", "1")
		return []string{"TMUX_PANE=" + s.T("display", "-t", "=boss-dev:", "-p", "#{pane_id}"), "T3CODE_THREAD_ID="}
	}
	boss := openBoss()
	s.T("new-session", "-d", "-s", "w-takeover", "sleep", "600")
	s.T("set-option", "-t", "=w-takeover:", "@dev_started_by", "boss-dev")
	for _, args := range [][]string{
		{"todo", "add", "Review the contract"},
		{"todo", "add", "Build take-over", "--on", "w-takeover"},
		{"todo", "note", "risk", "flaky CI"},
	} {
		if _, stderr, err := dev(boss, args...); err != nil {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(args, " "), err, stderr)
		}
	}
	thread := []string{"TMUX_PANE=", "T3CODE_THREAD_ID=5f1c2d90-aaaa-4000-8000-000000000001"}
	if _, _, err := dev(thread, "todo", "take-over", "boss-dev"); err == nil {
		t.Error("❌ a running boss's list taken")
	}
	s.T("kill-session", "-t", "=boss-dev")

	out, stderr, err := dev(thread, "todo", "take-over")
	if err != nil || !strings.Contains(out, "Lists this boss (t3:5f1c2d90, dev) can take over") || !strings.Contains(out, "  boss-dev        tmux     closed    2 open") {
		t.Fatalf("❌ listing: %v %s\n%s", err, stderr, out)
	}
	out, stderr, err = dev(thread, "todo", "take-over", "boss-dev")
	if err != nil || !strings.HasPrefix(out, "✅ Took over boss-dev (tmux, dev): 2 items, 1 note\n") || !strings.Contains(out, "⚠ 1 on the old boss's workers (w-takeover): re-check them (dev todo)") {
		t.Fatalf("❌ take-over: %v %s\n%s", err, stderr, out)
	}
	out, _, err = dev(thread, "todo")
	if err != nil || out != "1\t☐\tReview the contract\n2\t◌\tBuild take-over\ton w-takeover, session gone\n" {
		t.Errorf("❌ the thread's list: %v %q", err, out)
	}
	if out, _, _ := dev(thread, "todo", "note"); !strings.Contains(out, "flaky CI") {
		t.Errorf("❌ the thread's notes: %q", out)
	}

	boss = openBoss()
	out, stderr, err = dev(boss, "todo")
	if err != nil || out != "" || stderr != "" {
		t.Errorf("❌ reopened boss's list: %v %q %q, want empty", err, out, stderr)
	}
	if out, stderr, err := dev(boss, "todo", "add", "Fresh start"); err != nil || out != "added 1 · to do 1\n" {
		t.Errorf("❌ reopened boss's add: %v %q %s", err, out, stderr)
	}
}
