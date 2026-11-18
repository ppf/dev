//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
)

// A boss's previews (Watch) tell it in its inbox, once, of a PR that moved under an item, a worker gone, and an item
// unchanged two days, and tick an item added with --done-on once its PR is so (gh stubbed).
func TestTodoNudges(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	url := func(n string) string { return "https://github.com/acme/orbit/pull/" + n }
	views := filepath.Join(tmp, "views")
	stubs := writeStubs(t, filepath.Join(tmp, "stubs"), `case "$*" in
"pr view https://github.com/acme/orbit/pull/"*" --json state,mergedAt,closedAt,reviewDecision,statusCheckRollup") n=${3##*/}; cat "`+views+`/$n.json" ;;
*) exit 1 ;;
esac`, "gh")
	t.Setenv("PATH", stubs+":"+os.Getenv("PATH"))
	pr := func(n, state, review, merged string) {
		t.Helper()
		writeFile(t, filepath.Join(views, n+".json"), `{"state":"`+state+`","mergedAt":`+merged+`,"closedAt":null,"reviewDecision":"`+review+`","statusCheckRollup":[]}`)
	}
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	// A draft in the coordinator's box keeps the notes in the inbox
	s.T("new-session", "-d", "-s", "boss-orbit", "bash", script, "a draft", filepath.Join(tmp, "boss.log"))
	s.T("set-option", "-t", "=boss-orbit:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss-orbit:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "fix", "sleep 600")
	s.T("set-option", "-t", "=fix:", "@dev_started_by", "boss-orbit")
	dev := func(argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE="+boss, "PATH="+stubs+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		return string(out)
	}
	state := exec.Command(devBin, "state", "idle")
	state.Env = s.scriptEnv("TMUX_PANE=" + boss)
	state.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := state.CombinedOutput(); err != nil {
		t.Fatalf("❌ dev state idle: %v\n%s", err, out)
	}
	pr("415", "OPEN", "REVIEW_REQUIRED", "null")
	pr("418", "OPEN", "APPROVED", "null")
	dev("todo", "add", "Second review of #415", "--on", "Ana", "--link", url("415"), "--done-on", "approved")
	dev("todo", "add", "Deploy the cache fix after #418 merges", "--on", "Tomas", "--link", url("418"))
	dev("todo", "add", "Rework export rounding", "--on", "fix")
	dev("todo", "add", "Attach the signed DPA to the vendor ticket")

	start := time.Now()
	watch := func(after time.Duration) string {
		t.Helper()
		clk := control.Clock{Now: func() time.Time { return start.Add(after) }, Sleep: time.Sleep}
		if err := inbox.Watch(s.tmux, clk, boss); err != nil {
			t.Fatal(err)
		}
		b, ok, err := inbox.Here(s.tmux, boss)
		if !ok || err != nil {
			t.Fatalf("❌ no boss inbox: %v", err)
		}
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}
	check(t, "nothing moved", watch(0), "")

	pr("415", "OPEN", "APPROVED", "null")
	pr("418", "MERGED", "APPROVED", `"`+start.Add(-30*time.Minute).UTC().Format(time.RFC3339)+`"`)
	s.T("kill-session", "-t", "=fix")
	moved := watch(11 * time.Minute) // past the lookups' 10 minutes
	hint := strings.SplitN(moved, "\n(a \"to do\"", 2)
	check(t, "moved", hint[0], "[dev boss inbox] 3 worker update(s):\n"+
		`- to do #1 "Second review of #415" ticked by itself: PR #415 approved (--done-on approved)`+"\n"+
		`- to do #2 "Deploy the cache fix after #418 merges" (on Tomas): PR #418 merged 41m ago`+"\n"+
		`- to do #3 "Rework export rounding" (on fix): its session is gone`)
	check(t, "told once", watch(12*time.Minute), "")
	aged := watch(49 * time.Hour)
	check(t, "unchanged 2d", strings.SplitN(aged, "\n(a \"to do\"", 2)[0], "[dev boss inbox] 3 worker update(s):\n"+
		`- to do #2 "Deploy the cache fix after #418 merges" (on Tomas): unchanged 2d: check it at its source`+"\n"+
		`- to do #3 "Rework export rounding" (on fix): unchanged 2d: check it at its source`+"\n"+
		`- to do #4 "Attach the signed DPA to the vendor ticket": unchanged 2d: check it at its source`)
	check(t, "not again at 3d", watch(72*time.Hour), "")
	if os.Getenv("ZZ_CAPTURE") != "" {
		writeFile(t, os.Getenv("ZZ_CAPTURE"), "## watch after the PRs moved and the worker closed\n"+moved+"\n## watch at 49h\n"+aged+"\n## dev todo\n"+dev("todo"))
	}
}
