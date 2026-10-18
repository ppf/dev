//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A session whose branch has a PR shows its tag on the meta line; o on it and a click on the tag open it
// through gh (stubbed: JSON in a checkout of feature/login, a logged --web call). A session without one
// shows none.
func TestSidebarPR(t *testing.T) {
	d := actServer(t, "a")
	url := "https://github.com/o/r/pull/12"
	writeStubs(t, filepath.Join(d.tmp, "stubs"), `case "$*" in
"pr view --json "*) [ "$(git branch --show-current)" = feature/login ] || { echo 'no pull requests found' >&2; exit 1; }
	echo '{"number":12,"state":"OPEN","isDraft":false,"url":"`+url+`"}' ;;
"pr view `+url+` --web") echo "gh $*" >> "$DEV_STUB_LOG" ;;
*) exit 1 ;;
esac`, "gh")

	for _, s := range []struct{ name, branch string }{{"pr", "feature/login"}, {"nopr", "main"}} {
		pane := d.T("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", s.name, "-c", viewGitRepo(t, s.branch), "exec sleep 600")
		d.T("set-option", "-p", "-t", pane, "@agent_cmd", "sleep")
		d.T("set-option", "-p", "-t", pane, "@agent_state", "idle")
		d.T("select-pane", "-t", pane, "-T", s.name+" task")
		waitFor(t, s.name+" runs sleep", func() bool {
			cmd, _ := d.Try("display", "-t", pane, "-p", "#{pane_current_command}")
			return cmd == "sleep"
		})
	}
	actSwitch(d, "a")
	sidebar := actEnsure(d, "a")
	waitScreen(t, d.server, sidebar, "#12 open · Claude · idle")

	row, ok := actRow(d, sidebar, "pr")
	if !ok {
		t.Fatalf("no row for pr:\n%s", strings.Join(actScreen(d, sidebar), "\n"))
	}
	screen := actScreen(d, sidebar)
	check(t, "pr meta line", strings.TrimSpace(screen[row+1]), "#12 open · Claude · idle · pr task")
	nopr, _ := actRow(d, sidebar, "nopr")
	check(t, "nopr meta line", strings.TrimSpace(screen[nopr+1]), "Claude · idle · nopr task")

	webCalls := func() string {
		b, err := os.ReadFile(d.stubLog)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	call := "gh pr view " + url + " --web"
	actSelect(d, sidebar, "pr")
	d.T("send-keys", "-t", sidebar, "o")
	actWait(d, "o on pr: gh --web", webCalls, call)

	actClick(d, sidebar, 5, row+1)
	actWait(d, "click on the tag: gh --web", webCalls, call+"\n"+call)
	check(t, "click on the tag: client stays", actClientSession(d), "a")
	check(t, "click on the tag: sessions", d.T("list-sessions", "-F", "#{session_name}"), "a\nboot\nnopr\npr")
}

// A PR opened while a sidebar looks on shows within seconds: the worker's PostToolUse hook for gh pr create
// bumps @dev_pr_gen, the viewed sidebar looks it up again, and a sidebar viewed next shows the
// stored result without its own gh call.
func TestSidebarPRShared(t *testing.T) {
	d := actServer(t, "a", "b")
	opened := filepath.Join(d.tmp, "pr-opened")
	writeStubs(t, filepath.Join(d.tmp, "stubs"), `case "$*" in
"pr view --json "*) echo "gh $*" >> "$DEV_STUB_LOG"
	[ -e `+opened+` ] || { echo 'no pull requests found' >&2; exit 1; }
	echo '{"number":35,"state":"OPEN","isDraft":false,"url":"https://github.com/o/r/pull/35"}' ;;
*) exit 1 ;;
esac`, "gh")
	worker := d.T("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "w", "-c", viewGitRepo(t, "feature/login"), "exec sleep 600")
	d.T("set-option", "-p", "-t", worker, "@agent_cmd", "sleep")
	d.T("set-option", "-p", "-t", worker, "@agent_state", "idle")
	waitFor(t, "w runs sleep", func() bool {
		cmd, _ := d.Try("display", "-t", worker, "-p", "#{pane_current_command}")
		return cmd == "sleep"
	})
	lookups := func() int {
		b, err := os.ReadFile(d.stubLog)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Count(string(b), "gh pr view")
	}
	a, b := actEnsure(d, "a"), actEnsure(d, "b")
	actSwitch(d, "a")
	waitFor(t, "a's sidebar stored no PR", func() bool {
		v, _ := d.Try("show-option", "-pqv", "-t", worker, "@dev_pr")
		return strings.Contains(v, `"branch":"feature/login"`)
	})
	check(t, "lookups before the PR", strconv.Itoa(lookups()), "1")

	writeFile(t, opened, "")
	cmd := exec.Command(devBin, "state", "working")
	cmd.Env = append(d.scriptEnv(), "TMUX_PANE="+worker)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"}}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dev state: %v\n%s", err, out)
	}
	check(t, "hook bumped the gen", d.T("show-option", "-pqv", "-t", worker, "@dev_pr_gen"), "1")
	start := time.Now()
	waitScreen(t, d.server, a, "#35 open")
	t.Logf("viewed sidebar: badge %s after the hook", time.Since(start).Round(100*time.Millisecond))
	check(t, "lookups after the hook", strconv.Itoa(lookups()), "2")

	actSwitch(d, "b")
	waitScreen(t, d.server, b, "#35 open")
	check(t, "lookups after the switch", strconv.Itoa(lookups()), "2")
}
