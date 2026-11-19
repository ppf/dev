//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev todo check looks up a linked item's PR with gh (stubbed) and marks the item once its PR moved since its first
// lookup; once the boss session is closed, dev todo --all still lists its list, marked boss closed.
func TestTodoCheck(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	url := "https://github.com/acme/orbit/pull/412"
	view := filepath.Join(tmp, "view.json")
	stubs := writeStubs(t, filepath.Join(tmp, "stubs"), `case "$*" in
"pr view `+url+` --json state,mergedAt,closedAt,reviewDecision,statusCheckRollup") cat "`+view+`" ;;
*) exit 1 ;;
esac`, "gh")
	pr := func(review string) {
		t.Helper()
		writeFile(t, view, `{"state":"OPEN","mergedAt":null,"closedAt":null,"reviewDecision":"`+review+`","statusCheckRollup":[]}`)
	}
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "other", "exec sleep 600")
	s.T("new-session", "-d", "-s", "boss-orbit", "bash", script, "", filepath.Join(tmp, "boss.log"))
	s.T("set-option", "-t", "=boss-orbit:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-orbit:", "-p", "#{pane_id}")
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(append(env, "PATH="+stubs+":"+os.Getenv("PATH"))...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		return string(out)
	}
	boss := []string{"TMUX_PANE=" + pane}
	dev(boss, "todo", "add", "PR #412 open, CI pending", "--on", "reviewers", "--link", url)
	dev(boss, "todo", "add", "Ask Mara for the API key")

	pr("REVIEW_REQUIRED")
	if out := dev(boss, "todo", "check"); strings.Contains(out, "⚠") {
		t.Errorf("❌ first check marks:\n%s", out)
	}
	pr("APPROVED")
	out := dev(boss, "todo", "check")
	check(t, "check after approval", out, "2\t☐\tAsk Mara for the API key\n1\t◌\tPR #412 open, CI pending\ton reviewers\t"+url+"\t⚠ PR #412 approved 0m ago\n")

	s.T("kill-session", "-t", "=boss-orbit")
	out = dev([]string{"TMUX_PANE="}, "todo", "--all")
	check(t, "--all after close", out, "boss-orbit\t2\t☐\tAsk Mara for the API key\t⚠ boss closed\n"+
		"boss-orbit\t1\t◌\tPR #412 open, CI pending\ton reviewers\t"+url+"\t⚠ PR #412 approved 0m ago\n")
	out = strings.Join(strings.Fields(dev([]string{"TMUX_PANE="}, "todo", "--all", "--json")), "")
	if !strings.Contains(out, `"boss":"boss-orbit","closed":true`) {
		t.Errorf("❌ --all --json lacks the closed list:\n%s", out)
	}
	for _, want := range []string{`"text":"AskMarafortheAPIkey"`, `"status":"bossclosed"}`, `"status":"check","status_on":"reviewers"}`} {
		if !strings.Contains(out, want) {
			t.Errorf("❌ --all --json lacks %s:\n%s", want, out)
		}
	}

	// A closed boss's list is checked and ticked by --boss; nothing else takes it, nor a live session that is no boss
	writeFile(t, view, `{"state":"MERGED","mergedAt":"2026-01-01T00:00:00Z","closedAt":null,"reviewDecision":"APPROVED","statusCheckRollup":[]}`)
	out = dev([]string{"TMUX_PANE="}, "todo", "check", "--boss", "boss-orbit")
	if !strings.Contains(out, "1\t◌\tPR #412 open, CI pending\ton reviewers\t"+url+"\t⚠ PR #412 merged") {
		t.Errorf("❌ check of the closed list:\n%s", out)
	}
	out = dev([]string{"TMUX_PANE="}, "todo", "done", "--boss", "boss-orbit", "--text", "Ask Mara for the API key", "2")
	check(t, "done on the closed list", out, "done 2 · to do 1, 1 on others\n")
	fails := func(argv ...string) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=", "PATH="+stubs+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("❌ dev %s took it:\n%s", strings.Join(argv, " "), out)
		}
	}
	fails("todo", "add", "--boss", "boss-orbit", "new")
	fails("todo", "done", "--boss", "other", "1")
	fails("todo", "done", "--boss", "boss-nope", "1")
}
