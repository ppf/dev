//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubBoss draws Claude's input box (empty, or holding draft) and appends every line it reads to log.
const stubBoss = `rule=$(printf '─%.0s' $(seq 40))
printf '\033[38;5;244m%s\033[39m\n' "$rule"
if [ -n "$1" ]; then printf '❯\302\240%s\n' "$1"; else printf '❯\302\240\033[2mTry "fix it"\033[0m\n'; fi
printf '\033[38;5;244m%s\033[39m\n' "$rule"
while IFS= read -r line; do printf '%s\n' "$line" >> "$2"; done
`

// A worker's Stop reaches its boss as one pasted inbox prompt while the coordinator is idle with an
// empty input box; a boss with a draft in its box isn't pasted into, and gets the note through its own
// Stop hook instead (blocked, recorded as working).
func TestBossInbox(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	run := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %s: %v", strings.Join(argv, " "), err)
		}
		return string(out)
	}
	logOf := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(tmp, name+".log"))
		return string(data)
	}
	pane := map[string]string{}
	for _, boss := range []struct{ name, draft string }{{"boss", ""}, {"boss-draft", "half a thought"}} {
		s.T("new-session", "-d", "-s", boss.name, "bash", script, boss.draft, filepath.Join(tmp, boss.name+".log"))
		s.T("set-option", "-t", "="+boss.name+":", "@dev_herd", "1")
		pane[boss.name] = s.T("display", "-t", "="+boss.name+":", "-p", "#{pane_id}")
		worker := boss.name + "-worker"
		s.T("new-session", "-d", "-s", worker, "sleep 600")
		s.T("set-option", "-t", "="+worker+":", "@dev_started_by", s.T("display", "-t", "="+boss.name+":", "-p", "#{session_id}"))
		pane[worker] = s.T("display", "-t", "="+worker+":", "-p", "#{pane_id}")
	}
	for _, p := range pane {
		run(p, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	}
	waitFor(t, "stub boxes drawn", func() bool {
		return strings.Contains(s.T("capture-pane", "-p", "-t", pane["boss-draft"]), "half a thought") &&
			strings.Contains(s.T("capture-pane", "-p", "-t", pane["boss"]), "Try")
	})

	stop := `{"hook_event_name":"Stop","last_assistant_message":"All done.\nPR #7 opened"}`
	check(t, "worker hook prints nothing", run(pane["boss-worker"], stop, "state", "idle"), "")
	want := "[dev boss inbox] 1 worker update(s):\n- boss-worker finished: All done. PR #7 opened\n"
	waitFor(t, "boss gets the note", func() bool { return logOf("boss") == want })
	check(t, "a repeat note isn't sent", run(pane["boss-worker"], stop, "state", "idle"), "")

	run(pane["boss-draft-worker"], stop, "state", "idle")
	time.Sleep(time.Second)
	check(t, "no paste into a draft", logOf("boss-draft"), "")
	out := run(pane["boss-draft"], `{"hook_event_name":"Stop","stop_hook_active":false}`, "state", "idle")
	check(t, "boss Stop blocks with the note", out,
		`{"decision":"block","reason":"[dev boss inbox] 1 worker update(s):\n- boss-draft-worker finished: All done. PR #7 opened"}`+"\n")
	check(t, "blocked boss stays working", s.T("show-option", "-pqv", "-t", pane["boss-draft"], "@agent_state"), "working")
	check(t, "inbox drained", run(pane["boss-draft"], `{"hook_event_name":"Stop"}`, "state", "idle"), "")
	time.Sleep(time.Second)
	check(t, "boss logs stay as they were", logOf("boss")+"|"+logOf("boss-draft"), want+"|")
}

// A worker's new prompt resets its dedupe: a second turn ending like the first still reaches the boss,
// here as context on the coordinator's next prompt.
func TestBossInboxNextTurn(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	run := func(pane, stdin string) string {
		t.Helper()
		cmd := exec.Command(devBin, "state", "idle")
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev state: %v", err)
		}
		return string(out)
	}
	// A busy coordinator: notes wait for its own hooks
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", s.T("display", "-t", "=boss:", "-p", "#{session_id}"))
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run(boss, `{"hook_event_name":"UserPromptSubmit"}`)
	stop := `{"hook_event_name":"Stop","last_assistant_message":"Done."}`
	run(worker, stop)
	run(worker, `{"hook_event_name":"UserPromptSubmit"}`)
	run(worker, stop)
	check(t, "both turns as context", run(boss, `{"hook_event_name":"UserPromptSubmit"}`),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 2 worker update(s):\n- w finished: Done.\n- w finished: Done.","hookEventName":"UserPromptSubmit"}}`+"\n")
}
