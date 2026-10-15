//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
