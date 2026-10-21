//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
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
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	run := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev %s: %v, stderr %q", strings.Join(argv, " "), err, stderr.String())
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
	// The pasted prompt's own hooks find the inbox drained, also after a repeat note
	check(t, "pasted notes aren't context again", run(pane["boss"], `{"hook_event_name":"UserPromptSubmit"}`, "state", "working"), "")
	run(pane["boss-worker"], stop, "state", "idle")
	check(t, "pasted and repeat notes don't block the Stop", run(pane["boss"], `{"hook_event_name":"Stop"}`, "state", "idle"), "")

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

// A worker's Stop that only waits on background tasks keeps it working, from the turn's start, and tells
// its boss nothing; the Stop after they finish is its finished turn. A coordinator's such Stop leaves it idle.
func TestBossInboxBackgrounded(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	s.T("new-session", "-d", "-s", "boss", "bash", script, "", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	state := func() string {
		return s.T("show-option", "-pqv", "-t", worker, "@agent_state") + "@" + s.T("show-option", "-pqv", "-t", worker, "@agent_since")
	}
	boxLog := func() string { data, _ := os.ReadFile(log); return string(data) }
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "Try") })
	bg := `"background_tasks":[{"id":"a1","type":"subagent","status":"running"},{"id":"b1","type":"shell","status":"running"}]`
	run(boss, "idle", `{"hook_event_name":"Stop",`+bg+`}`)
	check(t, "backgrounded coordinator stays idle", s.T("show-option", "-pqv", "-t", boss, "@agent_state"), "idle")

	run(worker, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	start := state()
	time.Sleep(1100 * time.Millisecond) // @agent_since counts seconds
	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"⏳ Trimming fix comments.",`+bg+`}`)
	check(t, "backgrounded Stop keeps the turn working", state(), start)
	run(worker, "working", `{"hook_event_name":"UserPromptSubmit","prompt":"<task-notification>"}`)
	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Done.","background_tasks":[]}`)
	check(t, "finished turn", strings.Split(state(), "@")[0], "idle")
	waitFor(t, "boss gets only the finished note", func() bool { return boxLog() == "[dev boss inbox] 1 worker update(s):\n- w finished: Done.\n" })
}

// A working worker is stuck once neither its hooks nor its screen (timers and spinner aside) moved for
// StuckWorking, once per stretch; one whose turn only waits on background tasks says so. A subagent's tool
// call is progress too.
func TestBossInboxStuckProgress(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	// A draft in the coordinator's box keeps the notes in the inbox
	s.T("new-session", "-d", "-s", "boss", "bash", script, "a draft", filepath.Join(tmp, "boss.log"))
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(worker, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	tty := s.T("display", "-t", worker, "-p", "#{pane_tty}")
	rule := strings.Repeat("─", 40)
	draw := func(spinner, output, status string) {
		t.Helper()
		writeFile(t, tty, "\x1b[2J\x1b[H⏺ Running the tests.\n"+output+"\n"+spinner+"\n\n"+rule+"\n❯ \n"+rule+"\n  "+status+"\n")
		waitFor(t, "worker screen drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", worker), status) })
	}
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
	note := func() string { return s.T("show-option", "-pqv", "-t", worker, inbox.OptNote) }

	draw("✻ Brewing… (1m 3s · esc to interrupt)", "", "esc to interrupt · 12:41")
	check(t, "first sight", watch(0), "")
	draw("✶ Percolating… (16m 40s · esc to interrupt)", "", "esc to interrupt · 12:57")
	check(t, "stalled", watch(16*time.Minute), "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 16m")
	check(t, "no repeat in the stretch", watch(17*time.Minute), "")
	check(t, "its pane note stays current", note(), "stuck: no progress for 17m")
	draw("✶ Percolating… (18m 2s · esc to interrupt)", "ok  ./internal/inbox", "esc to interrupt · 12:59")
	check(t, "a moved-on screen is progress", watch(18*time.Minute), "")
	// Its turn ended, but waits on a background shell: the box is back without "esc to interrupt"
	draw("", "ok  ./internal/inbox", "1 shell · 13:00")
	check(t, "its turn's end is progress", watch(19*time.Minute), "")
	check(t, "parked on background tasks", watch(35*time.Minute),
		"[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 16m, waiting on its background tasks")

	before := s.T("show-option", "-pqv", "-t", worker, "@agent_active")
	time.Sleep(1100 * time.Millisecond) // @agent_active counts seconds
	run(worker, "working", `{"hook_event_name":"PostToolUse","agent_id":"a1"}`)
	if after := s.T("show-option", "-pqv", "-t", worker, "@agent_active"); after == before {
		t.Errorf("❌ a subagent's tool call left @agent_active at %s", after)
	}
	check(t, "a subagent's tool call leaves the state", s.T("show-option", "-pqv", "-t", worker, "@agent_state"), "working")

	// On Watch's clock: a hook 14m ago is progress, though the screen sat still 16m
	s.T("set-option", "-p", "-t", worker, "@agent_active", strconv.FormatInt(start.Add(37*time.Minute).Unix(), 10))
	check(t, "a recent hook is progress", watch(51*time.Minute), "")
	check(t, "its pane note left as it was", note(), "stuck: no progress for 16m, waiting on its background tasks")
	check(t, "a stretch from the hook", watch(53*time.Minute),
		"[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 16m, waiting on its background tasks")
	s.T("set-option", "-p", "-t", worker, "@agent_context", "85")
	check(t, "high context", watch(53*time.Minute), "[dev boss inbox] 1 worker update(s):\n- w high context: 85% used")
}

// An idle worker's open PR is checked with gh once per idle stretch, a draft's never; a conflict makes a note.
func TestBossInboxPR(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	bin := filepath.Join(tmp, "bin")
	ghLog := filepath.Join(tmp, "gh.log")
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$*\" >> "+ghLog+"\n"+
		`echo '{"state":"OPEN","mergeable":"CONFLICTING","statusCheckRollup":[{"name":"test"}]}'`+"\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	run := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	// A busy coordinator: notes wait in the inbox
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(boss, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	for i, state := range []string{"open", "draft"} {
		name := "w" + state
		s.T("new-session", "-d", "-s", name, "-c", tmp, "sleep 600")
		s.T("set-option", "-t", "="+name+":", "@dev_started_by", "boss")
		worker := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"PR opened."}`)
		s.T("set-option", "-p", "-t", worker, "@agent_since", strconv.FormatInt(time.Now().Add(-16*time.Minute).Unix(), 10))
		path := s.T("display", "-t", worker, "-p", "#{pane_current_path}")
		s.T("set-option", "-p", "-t", worker, "@dev_pr", fmt.Sprintf(`{"path":%q,"branch":"b","pr":{"Number":%d,"State":%q,"URL":"https://github.com/o/r/pull/%d"}}`, path, 12+i, state, 12+i))
	}
	b, ok, err := inbox.Here(s.tmux, boss)
	if !ok || err != nil {
		t.Fatalf("❌ no boss inbox: %v", err)
	}
	if _, err := b.Box.Take(); err != nil { // the finished notes
		t.Fatal(err)
	}
	for range 2 {
		if err := inbox.Watch(s.tmux, control.RealClock, boss); err != nil {
			t.Fatal(err)
		}
	}
	bt, err := b.Box.Take()
	if err != nil {
		t.Fatal(err)
	}
	check(t, "one note", bt.String(), "[dev boss inbox] 1 worker update(s):\n- wopen stuck: idle for 16m, PR #12 has merge conflicts")
	calls, _ := os.ReadFile(ghLog)
	check(t, "one gh call, none for the draft", string(calls), "pr view https://github.com/o/r/pull/12 --json state,mergeable,statusCheckRollup\n")
}

// A turn ending on a question reaches the boss as "asks", and again while the worker rests unanswered;
// the worker's next prompt answers it.
func TestBossInboxAsksAgain(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	// A busy coordinator: notes wait for its own hooks
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run := func(pane, st, stdin string) string {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev state %s %s: %v, stderr %q", st, stdin, err, stderr.String())
		}
		return string(out)
	}
	prompt := `{"hook_event_name":"UserPromptSubmit"}`
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(boss, "working", prompt)
	run(worker, "working", prompt)
	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"PR #12 merged.\n\nStart the next item?"}`)
	start := time.Now()
	watch := func(after time.Duration) {
		t.Helper()
		clk := control.Clock{Now: func() time.Time { return start.Add(after) }, Sleep: time.Sleep}
		if err := inbox.Watch(s.tmux, clk, boss); err != nil {
			t.Fatal(err)
		}
	}
	context := func(text string) string {
		return `{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 1 worker update(s):\n- w asks: ` + text + `","hookEventName":"UserPromptSubmit"}}` + "\n"
	}
	check(t, "asks", run(boss, "working", prompt), context("Start the next item?"))
	watch(inbox.AskAgain + 10*time.Second)
	check(t, "asks again", run(boss, "working", prompt), context("Start the next item? (unanswered for 30m)"))
	run(worker, "working", prompt)
	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Started."}`)
	watch(5 * time.Hour)
	check(t, "answered", run(boss, "working", prompt),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 1 worker update(s):\n- w finished: Started.","hookEventName":"UserPromptSubmit"}}`+"\n")
}

// A Codex worker's approval request, found by its hook's cwd, reaches its boss as a decision note.
func TestBossInboxCodexDecision(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	s.T("new-session", "-d", "-s", "boss", "bash", script, "", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "cx", sleeper(t, "codex"))
	s.T("set-option", "-t", "=cx:", "@dev_started_by", s.T("display", "-t", "=boss:", "-p", "#{session_id}"))
	run := func(env []string, stdin string, argv ...string) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev %s: %v %s", strings.Join(argv, " "), err, out)
		}
	}
	run(s.scriptEnv("TMUX_PANE="+boss), `{"hook_event_name":"SessionStart"}`, "state", "idle")
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "Try") })

	cwd := s.T("display", "-t", "=cx:", "-p", "#{pane_current_path}")
	run(s.scriptEnv(), `{"hook_event_name":"SessionStart","cwd":"`+cwd+`"}`, "state", "--codex", "idle")
	run(s.scriptEnv(), `{"hook_event_name":"PermissionRequest","cwd":"`+cwd+`","tool_name":"Bash",`+
		`"tool_input":{"command":"rm -rf build","description":"Delete build/"}}`, "state", "--codex", "waiting")
	want := "[dev boss inbox] 1 worker update(s):\n- cx needs a decision: Delete build/\n"
	waitFor(t, "boss gets the decision note", func() bool {
		data, _ := os.ReadFile(log)
		return string(data) == want
	})
}

// A worker's new prompt resets its dedupe: a second turn ending like the first still reaches the boss,
// here as context on the coordinator's next prompt. Each note reaches it once, as context or through a
// blocked Stop, never both.
func TestBossInboxNextTurn(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	run := func(pane, stdin string, state ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, "state", append(state, "idle")[0])
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev state: %v, stderr %q", err, stderr.String())
		}
		return string(out)
	}
	// A busy coordinator: notes wait for its own hooks
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss") // by name, as dev agent start writes it
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run(boss, `{"hook_event_name":"SessionStart"}`)
	run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "working")
	stop := `{"hook_event_name":"Stop","last_assistant_message":"Done."}`
	run(worker, stop)
	run(worker, `{"hook_event_name":"UserPromptSubmit"}`)
	run(worker, stop)
	// Two alike permission dialogs in one turn, the tool running between them, are two decisions
	ask := `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow Bash?"}`
	run(worker, `{"hook_event_name":"UserPromptSubmit"}`, "working")
	run(worker, ask, "waiting")
	time.Sleep(1100 * time.Millisecond) // @agent_since counts seconds
	run(worker, `{"hook_event_name":"PreToolUse"}`, "working")
	// The repeat must land in the same second as its dialog: start both at a second's start
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	run(worker, ask, "waiting")
	run(worker, ask, "waiting")
	check(t, "both turns and both dialogs as context", run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "working"),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 4 worker update(s):\n- w finished: Done.\n- w finished: Done.\n`+
			`- w needs a decision: Allow Bash?\n- w needs a decision: Allow Bash?","hookEventName":"UserPromptSubmit"}}`+"\n")
	// Each note reaches the coordinator once, whichever hook takes it
	check(t, "notes given as context don't block the Stop", run(boss, `{"hook_event_name":"Stop"}`), "")
	run(worker, `{"hook_event_name":"UserPromptSubmit"}`, "working")
	run(worker, stop)
	check(t, "boss Stop blocks with the note", run(boss, `{"hook_event_name":"Stop"}`),
		`{"decision":"block","reason":"[dev boss inbox] 1 worker update(s):\n- w finished: Done."}`+"\n")
	check(t, "notes a Stop blocked with aren't context again", run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "working"), "")
}

// A Claude worker's decision note says what its dialog asks, from the dev question hook's record when its
// screen shows that dialog, else Claude's own message; the worker's pane note (inbox.OptNote) says it too.
func TestBossInboxDecisionText(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	run := func(pane, stdin string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev %s: %v, stderr %q", strings.Join(argv, " "), err, stderr.String())
		}
		return string(out)
	}
	// A busy coordinator: its next prompt gets the notes as context
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	run(boss, `{"hook_event_name":"SessionStart"}`, "state", "idle")
	run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "state", "working")
	// draw shows a dialog on the worker's screen
	draw := func(name, screen string) {
		t.Helper()
		file := filepath.Join(tmp, name)
		writeFile(t, file, screen)
		s.T("respawn-pane", "-k", "-t", worker, "sh", "-c", `cat "$1"; exec sleep 600`, "sh", file)
		waitFor(t, name+" drawn", func() bool {
			return strings.Contains(s.T("capture-pane", "-p", "-t", worker), "1. ")
		})
	}
	ask := `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission"}`
	decide := func(question, want string) {
		t.Helper()
		run(worker, `{"hook_event_name":"UserPromptSubmit"}`, "state", "working")
		if question != "" {
			run(worker, question, "question")
		}
		run(worker, ask, "state", "waiting")
		check(t, "boss gets: "+want, run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "state", "working"),
			`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 1 worker update(s):\n- w needs a decision: `+want+
				`","hookEventName":"UserPromptSubmit"}}`+"\n")
		check(t, "pane note: "+want, s.T("show-option", "-pqv", "-t", worker, inbox.OptNote), "needs a decision: "+want)
	}
	rule := strings.Repeat("─", 40) + "\n"
	draw("colors", rule+"☐ Color\n\nWhich test color should I pick?\n\n❯ 1. Red\n  2. Green\n  3. Blue\n  4. Type something.\n"+rule+"  5. Chat about this\n")
	decide(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":`+
		`"Which test color should I pick?","header":"Color","options":[{"label":"Red"},{"label":"Green"},{"label":"Blue"}]}]}}`,
		"Which test color should I pick? (Red / Green / Blue)")
	draw("bash", "⏺ Bash(ls)\n"+rule+" Bash command\n\n  rm -rf build\n  Delete build/\n\nDo you want to proceed?\n❯ 1. Yes\n  2. No\n")
	bash := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"Delete build/"}}`
	decide(bash, "allow Bash: rm -rf build")
	// The record of a tool auto mode approved meanwhile isn't the dialog on screen, though its call is above it
	draw("colors-again", "⏺ Bash(rm -rf build)\n  Do you want to proceed? Yes\n"+rule+"☐ Color\n\nWhich test color should I pick?\n\n❯ 1. Red\n  2. Green\n")
	run(worker, `{"hook_event_name":"UserPromptSubmit"}`, "state", "working")
	run(worker, bash, "question")
	run(worker, ask, "state", "waiting")
	check(t, "boss gets Claude's message", run(boss, `{"hook_event_name":"UserPromptSubmit"}`, "state", "working"),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 1 worker update(s):\n- w needs a decision: Claude needs your permission",`+
			`"hookEventName":"UserPromptSubmit"}}`+"\n")
}

// A Grok worker's hooks run plain `dev state` with GROK_HOOK_EVENT set: its subagents' events and the
// extra Stop at session end are left out, no session is recorded, and its finished turn reaches the boss.
func TestGrokState(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	s.T("new-session", "-d", "-s", "boss", "bash", script, "", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "gk", sleeper(t, "grok"))
	s.T("set-option", "-t", "=gk:", "@dev_started_by", s.T("display", "-t", "=boss:", "-p", "#{session_id}"))
	gk := s.T("display", "-t", "=gk:", "-p", "#{pane_id}")
	waitFor(t, "grok stub running", func() bool { return s.T("display", "-t", gk, "-p", "#{pane_current_command}") == "grok" })
	run := func(pane, st, stdin string, env ...string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv(append(env, "TMUX_PANE="+pane)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	grok := func(st, stdin string) { t.Helper(); run(gk, st, stdin, "GROK_HOOK_EVENT=x") }
	state := func() string {
		return s.T("show-option", "-pqv", "-t", gk, "@agent_state") + "/" + s.T("show-option", "-pqv", "-t", gk, "@agent_cmd")
	}
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "Try") })

	grok("working", `{"hook_event_name":"UserPromptSubmit","session_id":"g1","transcript_path":"/t/g1.jsonl"}`)
	check(t, "prompt", state(), "working/grok")
	check(t, "no session for restore to resume", s.T("show-option", "-pqv", "-t", gk, "@agent_session"), "")
	grok("idle", `{"hook_event_name":"StopCancelled","reason":"max_turns","subagentType":"explore"}`)
	grok("idle", `{"hook_event_name":"Stop","reason":"session_end","last_assistant_message":"bye"}`)
	check(t, "subagent and session-end events skipped", state(), "working/grok")
	time.Sleep(time.Second)
	check(t, "no note for skipped events", func() string { data, _ := os.ReadFile(log); return string(data) }(), "")

	grok("idle", `{"hook_event_name":"Stop","reason":"end_turn","last_assistant_message":"Tests pass"}`)
	check(t, "turn end", state(), "idle/grok")
	waitFor(t, "boss gets the note", func() bool {
		data, _ := os.ReadFile(log)
		return string(data) == "[dev boss inbox] 1 worker update(s):\n- gk finished: Tests pass\n"
	})

	// An earlier turn's reports landing after the next prompt leave that prompt's turn working
	grok("working", `{"hook_event_name":"UserPromptSubmit","promptId":"p1"}`)
	grok("working", `{"hook_event_name":"UserPromptSubmit","promptId":"p2"}`)
	grok("idle", `{"hook_event_name":"StopCancelled","promptId":"p1","reason":"user_interrupt"}`)
	grok("idle", `{"hook_event_name":"StopFailure","promptId":"p1","error":"rate_limit"}`)
	check(t, "earlier turn's reports skipped", state(), "working/grok")
	time.Sleep(time.Second)
	check(t, "no note for an earlier turn", func() string { data, _ := os.ReadFile(log); return string(data) }(),
		"[dev boss inbox] 1 worker update(s):\n- gk finished: Tests pass\n")
	grok("idle", `{"hook_event_name":"StopCancelled","promptId":"p2","reason":"user_interrupt"}`)
	check(t, "own turn's report", state(), "idle/grok")
	grok("working", `{"hook_event_name":"PostToolUse"}`)
	grok("idle", `{"hook_event_name":"StopFailure","promptId":"p1","error":"rate_limit"}`)
	check(t, "earlier turn's report skipped after the next turn ended", state(), "working/grok")
	time.Sleep(time.Second)
	check(t, "still no note for an earlier turn", func() string { data, _ := os.ReadFile(log); return string(data) }(),
		"[dev boss inbox] 1 worker update(s):\n- gk finished: Tests pass\n")
	grok("working", `{"hook_event_name":"UserPromptSubmit","promptId":"p3"}`)
	grok("idle", `{"hook_event_name":"Stop","reason":"end_turn"}`)
	check(t, "turn end without a prompt id settles", state(), "idle/grok")
	grok("working", `{"hook_event_name":"UserPromptSubmit","promptId":"p4"}`)
	grok("clear", `{"hook_event_name":"SessionEnd"}`)
	check(t, "session end drops the prompt id", s.T("show-option", "-pqv", "-t", gk, "@agent_prompt_id"), "")
}

// A worker running an agent team has its teammates in panes beside its own: the boss hears of the worker
// from its agent pane alone, and of a teammate only its decisions, named by its pane, once.
func TestBossInboxAgentTeam(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	run := func(pane, st, stdin string) string {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("❌ dev state %s %s: %v, stderr %q", st, stdin, err, stderr.String())
		}
		return string(out)
	}
	// A busy coordinator: notes wait for its own hooks
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	prompt := `{"hook_event_name":"UserPromptSubmit"}`
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(boss, "working", prompt)
	run(worker, "working", prompt)
	var mates []string
	for range 2 {
		mate := s.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=w:1", "sleep 600")
		run(mate, "working", prompt)
		mates = append(mates, mate)
	}
	for i, p := range append([]string{worker}, mates...) {
		s.T("set-option", "-p", "-t", p, "@agent_since", strconv.FormatInt(time.Now().Add(-time.Duration(21+10*i)*time.Minute).Unix(), 10))
	}
	start := time.Now()
	watch := func(after time.Duration) {
		t.Helper()
		clk := control.Clock{Now: func() time.Time { return start.Add(after) }, Sleep: time.Sleep}
		if err := inbox.Watch(s.tmux, clk, boss); err != nil {
			t.Fatal(err)
		}
	}
	watch(0)
	watch(21 * time.Minute)

	stop := `{"hook_event_name":"Stop","last_assistant_message":"Done."}`
	run(mates[0], "idle", stop)
	run(worker, "idle", stop)
	run(mates[1], "working", prompt)
	run(worker, "idle", stop)
	ask := `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow Bash?"}`
	run(mates[1], "waiting", ask)
	s.T("set-option", "-p", "-t", mates[1], "@agent_since", strconv.FormatInt(time.Now().Add(-4*time.Minute).Unix(), 10))
	watch(time.Since(start))
	check(t, "one note per event, the teammate's decision named by its pane", run(boss, "working", prompt),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 3 worker update(s):\n- w stuck: no progress for 21m\n`+
			`- w finished: Done.\n- w (`+mates[1]+`) needs a decision: Allow Bash?","hookEventName":"UserPromptSubmit"}}`+"\n")
	check(t, "teammates get no preview note", s.T("show-option", "-pqv", "-t", mates[0], inbox.OptNote), "")

	run(mates[1], "working", prompt)
	time.Sleep(1100 * time.Millisecond) // @agent_since counts seconds
	run(mates[1], "waiting", ask)
	check(t, "the teammate's next alike dialog is a new decision", run(boss, "working", prompt),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 1 worker update(s):\n- w (`+mates[1]+`) needs a decision: Allow Bash?","hookEventName":"UserPromptSubmit"}}`+"\n")
}

// A worker a rate limit stopped is resumed by its boss's Watch once the reset shown passed, unless it was
// prompted since; its failed note says when, and its resume is a note too.
func TestBossInboxRateLimitResume(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	run := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	// A busy coordinator: notes wait in the inbox
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	prompt := `{"hook_event_name":"UserPromptSubmit"}`
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(boss, "working", prompt)
	script := filepath.Join(tmp, "worker.sh")
	writeFile(t, script, stubBoss) // an empty input box that logs what it's sent
	worker := map[string]string{}
	for _, w := range []string{"w", "busy"} {
		s.T("new-session", "-d", "-s", w, "bash", script, "", filepath.Join(tmp, w+".log"))
		s.T("set-option", "-t", "="+w+":", "@dev_started_by", "boss")
		worker[w] = s.T("display", "-t", "="+w+":", "-p", "#{pane_id}")
		waitFor(t, w+" box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", worker[w]), "❯") })
		run(worker[w], "working", prompt)
	}
	// Reset half an hour ago: due at once
	reset := time.Now().Add(-30 * time.Minute)
	failure := `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets ` + reset.Format("3:04pm") + `"}`
	for _, w := range []string{"w", "busy"} {
		run(worker[w], "idle", failure)
	}
	check(t, "resume scheduled", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resume_at"), strconv.FormatInt(reset.Truncate(time.Minute).Add(time.Minute).Unix(), 10))
	check(t, "prompts recorded", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resume_prompts"), "1")
	run(worker["busy"], "working", prompt) // prompted by hand since

	b, ok, err := inbox.Here(s.tmux, boss)
	if !ok || err != nil {
		t.Fatalf("❌ no boss inbox: %v", err)
	}
	if err := inbox.Watch(s.tmux, control.RealClock, boss); err != nil {
		t.Fatal(err)
	}
	bt, err := b.Box.Take()
	if err != nil {
		t.Fatal(err)
	}
	at := reset.Truncate(time.Minute).Add(time.Minute).Format("15:04")
	limit := "failed: rate_limit: You've hit your session limit · resets " + reset.Format("3:04pm") + "; dev resumes it at " + at
	check(t, "notes", bt.String(), "[dev boss inbox] 3 worker update(s):\n- w "+limit+"\n- busy "+limit+"\n- w resumed: its rate limit reset, dev sent it: continue")
	waitFor(t, "w sent the resume prompt", func() bool {
		data, _ := os.ReadFile(filepath.Join(tmp, "w.log"))
		return strings.Contains(string(data), inbox.ResumePrompt)
	})
	check(t, "w's resume done", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resume_at")+"/"+s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resumes"), "/1")
	check(t, "busy's resume dropped", s.T("show-option", "-pqv", "-t", worker["busy"], "@dev_resume_at"), "")
	data, _ := os.ReadFile(filepath.Join(tmp, "busy.log"))
	check(t, "busy not sent anything", string(data), "")

	// A finished turn ends the run of resumes; maxResumes in a row stops them
	run(worker["w"], "idle", `{"hook_event_name":"Stop","last_assistant_message":"Done."}`)
	check(t, "Stop clears the count", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resumes"), "")
	s.T("set-option", "-p", "-t", worker["w"], "@dev_resumes", "3")
	run(worker["w"], "working", prompt)
	run(worker["w"], "idle", failure)
	check(t, "no resume after 3", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_resume_at"), "")
	check(t, "its note says so", s.T("show-option", "-pqv", "-t", worker["w"], "@dev_boss_note"),
		"failed: rate_limit: You've hit your session limit · resets "+reset.Format("3:04pm")+"; resumed 3 times in a row, not resuming it again")
}

// A coordinator whose hooks recorded Claude's messaging socket and a prompting permission mode gets its notes
// over the socket, also while it works or holds a draft, one batch at a time until its prompt shows the batch.
// Without a known prompting mode, or with a socket that fails, the notes stay for the paste and its hooks.
func TestBossInboxSocket(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	// A draft: no paste ever goes in
	s.T("new-session", "-d", "-s", "boss", "bash", script, "a draft", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")

	dir, err := os.MkdirTemp("", "dvs") // macOS caps unix socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	lines := make(chan string, 10)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			lines <- line
		}
	}()
	received := func() string {
		t.Helper()
		select {
		case line := <-lines:
			var msg struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("❌ line %q: %v", line, err)
			}
			return msg.Message.Content
		case <-time.After(time.Second):
			return ""
		}
	}
	state := func(pane, st, stdin, socket string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDE_CODE_MESSAGING_SOCKET="+socket)
		cmd.Stdin = strings.NewReader(stdin)
		var errb strings.Builder
		cmd.Stderr = &errb
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev state %s %s: %v, stderr %q", st, stdin, err, errb.String())
		}
		return string(out), errb.String()
	}
	run := func(pane, st, stdin, socket string) string {
		t.Helper()
		out, stderr := state(pane, st, stdin, socket)
		if stderr != "" {
			t.Fatalf("❌ dev state %s %s: stderr %q", st, stdin, stderr)
		}
		return out
	}
	finish := func(text string) {
		t.Helper()
		run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"`+text+`"}`, "")
	}
	batch := func(texts ...string) string {
		var bt inbox.Batch
		for _, text := range texts {
			bt.Notes = append(bt.Notes, inbox.Note{Worker: "w", Kind: inbox.Finished, Text: text})
		}
		return bt.String()
	}

	run(boss, "idle", `{"hook_event_name":"SessionStart"}`, sock)
	check(t, "socket recorded", s.T("show-option", "-pqv", "-t", boss, "@agent_socket"), sock)
	finish("one")
	check(t, "no mode yet: not sent", received(), "")
	out := run(boss, "working", `{"hook_event_name":"UserPromptSubmit","prompt":"hi","permission_mode":"default"}`, sock)
	check(t, "the note goes as context", out, `{"hookSpecificOutput":{"additionalContext":"`+strings.ReplaceAll(batch("one"), "\n", `\n`)+`","hookEventName":"UserPromptSubmit"}}`+"\n")

	// Working, with a draft: sent anyway
	finish("two")
	msg := batch("two") + "\n(from dev's boss inbox: act on these like any inbox batch; dev reads no reply)"
	check(t, "sent to a working coordinator", received(), msg)
	finish("three")
	check(t, "one batch in flight at a time", received(), "")
	out = run(boss, "working", `{"hook_event_name":"UserPromptSubmit","prompt":`+strconv.Quote(msg)+`,"permission_mode":"default"}`, sock)
	check(t, "its arrival takes the next note as context", out, `{"hookSpecificOutput":{"additionalContext":"`+strings.ReplaceAll(batch("three"), "\n", `\n`)+`","hookEventName":"UserPromptSubmit"}}`+"\n")
	finish("four")
	check(t, "sent again once shown", received(), batch("four")+"\n(from dev's boss inbox: act on these like any inbox batch; dev reads no reply)")
	run(boss, "working", `{"hook_event_name":"UserPromptSubmit","prompt":`+strconv.Quote(batch("four")+"\n(from dev's boss inbox: act on these like any inbox batch; dev reads no reply)")+`,"permission_mode":"default"}`, sock)

	// Bypass mode would hold the message behind a dialog
	run(boss, "idle", `{"hook_event_name":"Stop","permission_mode":"bypassPermissions"}`, sock)
	finish("five")
	check(t, "bypass mode: not sent", received(), "")
	out = run(boss, "idle", `{"hook_event_name":"Stop","permission_mode":"bypassPermissions"}`, sock)
	check(t, "bypass mode: its Stop gets the note", out, `{"decision":"block","reason":"`+strings.ReplaceAll(batch("five"), "\n", `\n`)+`"}`+"\n")
	run(boss, "idle", `{"hook_event_name":"Stop","stop_hook_active":true,"permission_mode":"bypassPermissions"}`, sock)

	// A socket that's gone: the note stays
	gone := filepath.Join(dir, "gone")
	run(boss, "idle", `{"hook_event_name":"Stop","permission_mode":"auto"}`, gone)
	if _, stderr := state(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"six"}`, ""); !strings.Contains(stderr, "send to "+gone) {
		t.Errorf("failed send not reported: %q", stderr)
	}
	out = run(boss, "idle", `{"hook_event_name":"Stop","permission_mode":"auto"}`, gone)
	check(t, "failed send: its Stop gets the note", out, `{"decision":"block","reason":"`+strings.ReplaceAll(batch("six"), "\n", `\n`)+`"}`+"\n")
	data, _ := os.ReadFile(log)
	check(t, "never pasted", string(data), "")
}

// A batch sent over the socket that the coordinator doesn't show 5 minutes into its rest is reported and pasted;
// the batch after it goes over the socket again.
func TestBossInboxSocketLost(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss.log")
	s.T("new-session", "-d", "-s", "boss", "bash", script, "", log)
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "w", "sleep 600")
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	worker := s.T("display", "-t", "=w:", "-p", "#{pane_id}")

	dir, err := os.MkdirTemp("", "dvs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	received := make(chan struct{}, 10)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			received <- struct{}{}
		}
	}()
	sent := func() bool {
		select {
		case <-received:
			return true
		case <-time.After(time.Second):
			return false
		}
	}
	run := func(pane, st, stdin string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", st)
		cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDE_CODE_MESSAGING_SOCKET="+sock)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Fatalf("❌ dev state %s %s: %v, output %q", st, stdin, err, out)
		}
	}
	boxLog := func() string { data, _ := os.ReadFile(log); return string(data) }
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(boss, "idle", `{"hook_event_name":"Stop","permission_mode":"default"}`)
	waitFor(t, "stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", boss), "Try") })

	start := time.Now()
	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"x"}`)
	if !sent() {
		t.Fatal("❌ not sent over the socket")
	}
	watch := func(after time.Duration) error {
		clk := control.Clock{Now: func() time.Time { return start.Add(after) }, Sleep: time.Sleep}
		return inbox.Watch(s.tmux, clk, boss)
	}
	if err := watch(4 * time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	check(t, "in flight: no paste", boxLog(), "")
	if err := watch(5*time.Minute + 2*time.Second); err == nil || !strings.Contains(err.Error(), "went unshown 5m0s into a rest") {
		t.Errorf("lost batch not reported: %v", err)
	}
	waitFor(t, "lost batch pasted", func() bool { return boxLog() == "[dev boss inbox] 1 worker update(s):\n- w finished: x\n" })
	if sent() {
		t.Error("lost batch sent again")
	}

	run(worker, "idle", `{"hook_event_name":"Stop","last_assistant_message":"y"}`)
	if !sent() {
		t.Error("the next batch isn't sent over the socket")
	}
}
