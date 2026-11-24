//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A desktop app session takes over the list of dev boss boss-dev once it is closed: dev todo there shows its items, the
// one on the old boss's worker as session gone; a boss-dev opened again afterwards starts with an empty list of its own.
// The reopened boss's SessionStart hints at the session's list, until it has a list of its own.
func TestTodoTakeOver(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "dev")
	config := filepath.Join(tmp, "config")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(config, "dev", "projects.toml"), "[[project]]\nname = \"dev\"\npath = \""+project+"\"\n")
	record := filepath.Join(tmp, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "org", "local_5f1c2d90.json")
	writeFile(t, record, `{"sessionId":"local_5f1c2d90","originCwd":"`+project+`"}`)
	if err := os.MkdirAll(filepath.Join(tmp, ".claude", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newServer(t, "set -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"), "XDG_CONFIG_HOME="+config, "HOME="+tmp)
	devIn := func(stdin string, env []string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Dir = project
		cmd.Env = s.scriptEnv(append([]string{"CLAUDECODE=1", "CLAUDE_CODE_HOST_SESSION_ID=", "CLAUDE_CONFIG_DIR="}, env...)...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	dev := func(env []string, argv ...string) (string, string, error) {
		t.Helper()
		return devIn("", env, argv...)
	}
	openBoss := func() []string {
		s.T("new-session", "-d", "-s", "boss-dev", "-x", "120", "-y", "40", "sleep", "600")
		s.T("set-option", "-t", "=boss-dev:", "@dev_herd", "1")
		pane := s.T("display", "-t", "=boss-dev:", "-p", "#{pane_id}")
		s.T("set-option", "-p", "-t", pane, "@dev_boss_pane", "coordinator")
		return []string{"TMUX_PANE=" + pane}
	}
	boss := openBoss()
	s.T("new-session", "-d", "-s", "w-takeover", "sleep", "600")
	s.T("set-option", "-t", "=w-takeover:", "@dev_started_by", "boss-dev")
	for _, args := range [][]string{
		{"todo", "add", "--no-details", "Review the contract"},
		{"todo", "add", "Build take-over", "--on", "w-takeover"},
		{"todo", "note", "risk", "flaky CI"},
	} {
		if _, stderr, err := dev(boss, args...); err != nil {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(args, " "), err, stderr)
		}
	}
	thread := []string{"TMUX_PANE=", "CLAUDE_CODE_HOST_SESSION_ID=local_5f1c2d90"}
	if _, _, err := dev(thread, "todo", "take-over", "boss-dev"); err == nil {
		t.Error("❌ a running boss's list taken")
	}
	s.T("kill-session", "-t", "=boss-dev")

	out, stderr, err := dev(thread, "todo", "take-over")
	if err != nil || !strings.Contains(out, "Lists this boss (local_5f1c2d90, dev) can take over") || !strings.Contains(out, "  boss-dev        tmux     closed    2 open") {
		t.Fatalf("❌ listing: %v %s\n%s", err, stderr, out)
	}
	out, stderr, err = dev(thread, "todo", "take-over", "boss-dev")
	if err != nil || !strings.HasPrefix(out, "✅ Took over boss-dev (tmux, dev): 2 items, 1 note\n") || !strings.Contains(out, "⚠ 1 on the old boss's workers (w-takeover): re-check them (dev todo)") {
		t.Fatalf("❌ take-over: %v %s\n%s", err, stderr, out)
	}
	out, _, err = dev(thread, "todo")
	if err != nil || out != "1\t☐\tReview the contract\n2\t◌\tBuild take-over\ton w-takeover, session gone\n" {
		t.Errorf("❌ the session's list: %v %q", err, out)
	}
	if out, _, _ := dev(thread, "todo", "note"); !strings.Contains(out, "flaky CI") {
		t.Errorf("❌ the session's notes: %q", out)
	}

	boss = openBoss()
	start := `{"hook_event_name":"SessionStart","source":"startup"}`
	if out, stderr, err := devIn(start, boss, "state", "idle"); err != nil || !strings.Contains(out, "An earlier boss's list for dev waits: local_5f1c2d90 (2 open, 1 note, ") {
		t.Errorf("❌ reopened boss's SessionStart hint: %v %q %s", err, out, stderr)
	}
	out, stderr, err = dev(boss, "todo")
	if err != nil || out != "" || stderr != "" {
		t.Errorf("❌ reopened boss's list: %v %q %q, want empty", err, out, stderr)
	}
	if out, stderr, err := dev(boss, "todo", "add", "--no-details", "Fresh start"); err != nil || out != "added 1 · to do 1\n" {
		t.Errorf("❌ reopened boss's add: %v %q %s", err, out, stderr)
	}
	if out, _, _ := devIn(start, boss, "state", "idle"); strings.Contains(out, "waits") {
		t.Errorf("❌ hint with a list of its own: %q", out)
	}
}
