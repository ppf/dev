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

// A worker working past the threshold gets one stuck note; a new prompt (a background task's
// notification) lets it repeat only while its screen, timers and spinner aside, hasn't moved on.
func TestBossInboxStuckRepeat(t *testing.T) {
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
	prompt := `{"hook_event_name":"UserPromptSubmit","prompt":"<task-notification>"}`
	run(boss, "idle", `{"hook_event_name":"SessionStart"}`)
	run(worker, "working", prompt)
	s.T("set-option", "-p", "-t", worker, "@agent_since", strconv.FormatInt(time.Now().Add(-21*time.Minute).Unix(), 10))
	tty := s.T("display", "-t", worker, "-p", "#{pane_tty}")
	rule := strings.Repeat("─", 40)
	draw := func(spinner, output string) {
		t.Helper()
		writeFile(t, tty, "\x1b[2J\x1b[H⏺ Running the tests.\n"+output+"\n"+spinner+"\n\n"+rule+"\n❯ \n"+rule+"\n  ~/repo · 12:41\n")
		waitFor(t, "worker screen drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", worker), spinner) })
	}
	watch := func() string {
		t.Helper()
		if err := inbox.Watch(s.tmux, control.RealClock, boss); err != nil {
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
	note := "[dev boss inbox] 1 worker update(s):\n- w stuck: working for 21m"

	draw("✻ Brewing… (21m 3s · esc to interrupt)", "")
	check(t, "first stuck note", watch(), note)
	check(t, "no repeat without a prompt", watch(), "")
	run(worker, "working", prompt)
	draw("✶ Percolating… (21m 40s · esc to interrupt)", "")
	check(t, "repeat for an unchanged screen", watch(), note)
	run(worker, "working", prompt)
	draw("✶ Percolating… (21m 52s · esc to interrupt)", "ok  ./internal/inbox")
	check(t, "no repeat for a moved-on screen", watch(), "")
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
	watch := func() {
		t.Helper()
		if err := inbox.Watch(s.tmux, control.RealClock, boss); err != nil {
			t.Fatal(err)
		}
	}
	watch()

	stop := `{"hook_event_name":"Stop","last_assistant_message":"Done."}`
	run(mates[0], "idle", stop)
	run(worker, "idle", stop)
	run(mates[1], "working", prompt)
	run(worker, "idle", stop)
	ask := `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow Bash?"}`
	run(mates[1], "waiting", ask)
	s.T("set-option", "-p", "-t", mates[1], "@agent_since", strconv.FormatInt(time.Now().Add(-4*time.Minute).Unix(), 10))
	watch()
	check(t, "one note per event, the teammate's decision named by its pane", run(boss, "working", prompt),
		`{"hookSpecificOutput":{"additionalContext":"[dev boss inbox] 3 worker update(s):\n- w stuck: working for 21m\n`+
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
