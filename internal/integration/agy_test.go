//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dev state --agy on a pane running agy: a turn starts at the first PreInvocation and ends at Stop, the other
// events only stamp activity, the conversation is the session, a worker's Stop reaches its boss, and nothing
// ever goes to stdout (agy reads it as a hook's decision: {} on PreToolUse denies the tool call).
func TestStateAgy(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	s.T("new-session", "-d", "-s", "boss", "sleep 600")
	s.T("set-option", "-t", "=boss:", "@dev_herd", "1")
	boss := s.T("display", "-t", "=boss:", "-p", "#{pane_id}")
	s.T("new-session", "-d", "-s", "ag", sleeper(t, "agy"))
	s.T("set-option", "-t", "=ag:", "@dev_started_by", "boss")
	pane := s.T("display", "-t", "=ag:", "-p", "#{pane_id}")
	waitFor(t, "agy running", func() bool { return s.T("display", "-t", pane, "-p", "#{pane_current_command}") == "agy" })
	run := func(env []string, stdin string, args ...string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.Command(devBin, append([]string{"state"}, args...)...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var errOut strings.Builder
		cmd.Stderr = &errOut
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev state %v: %v, stderr %q", args, err, errOut.String())
		}
		return string(out), errOut.String()
	}
	// A busy coordinator: notes wait for its own hooks
	run(s.scriptEnv("TMUX_PANE="+boss), `{"hook_event_name":"UserPromptSubmit"}`, "working")
	agy := func(state, stdin string) string {
		t.Helper()
		out, errOut := run(s.scriptEnv("TMUX_PANE="+pane), stdin, "--agy", state)
		check(t, "no stdout", out, "")
		return errOut
	}
	opts := func() string {
		var v []string
		for _, o := range []string{"@agent_state", "@agent_cmd", "@agent_prompts", "@agent_session", "@agent_transcript"} {
			v = append(v, s.T("show-option", "-pqv", "-t", pane, o))
		}
		return strings.Join(v, " ")
	}
	// Its transcript: a turn's end note says its last reply
	transcript := filepath.Join(tmp, "t.jsonl")
	writeFile(t, transcript, `{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"say ok"}`+"\n"+`{"source":"MODEL","type":"PLANNER_RESPONSE","content":"ok"}`+"\n")
	ids := `"conversationId":"110c93f6-c88f","transcriptPath":"` + transcript + `","cwd":"/w"`

	check(t, "turn start", agy("working", `{"hookEventName":"PreInvocation","invocationNum":0,"lastUserInput":"say ok",`+ids+`}`), "")
	check(t, "turn start recorded", opts(), "working agy 1 110c93f6-c88f "+transcript)
	s.T("set-option", "-p", "-t", pane, "@agent_active", "1")
	check(t, "a later model call", agy("working", `{"hookEventName":"PreInvocation","invocationNum":1,`+ids+`}`), "")
	check(t, "a later model call counts no prompt", opts(), "working agy 1 110c93f6-c88f "+transcript)
	if s.T("show-option", "-pqv", "-t", pane, "@agent_active") == "1" {
		t.Error("❌ a later model call didn't stamp @agent_active")
	}
	check(t, "a tool call", agy("working", `{"hookEventName":"PreToolUse","toolCall":{"name":"run_command"},`+ids+`}`), "")
	check(t, "a question", agy("working", `{"hookEventName":"PreToolUse","toolCall":{"name":"ask_question","args":{"questions":[`+
		`{"is_multi_select":false,"options":["Red","Blue"],"question":"Red or blue?"}]}},`+ids+`}`), "")
	check(t, "a question is a decision", opts()+" "+s.T("show-option", "-pqv", "-t", pane, "@agent_waiting_for")+" "+s.T("show-option", "-pqv", "-t", pane, "@agent_question"),
		"waiting agy 1 110c93f6-c88f "+transcript+` permission_prompt {"tool":"AskUserQuestion","questions":[{"question":"Red or blue?","header":"","options":[{"label":"Red","description":""},{"label":"Blue","description":""}],"multiSelect":false}]}`)
	check(t, "its answer", agy("working", `{"hookEventName":"PostToolUse","toolCall":{"name":"ask_question"},`+ids+`}`), "")
	// The stub's screen shows no input box, as while another dialog is open (TestAgentStartAgy has it back)
	check(t, "a tool call's end with no input box on screen", opts(), "waiting agy 1 110c93f6-c88f "+transcript)
	check(t, "turn end", agy("idle", `{"hookEventName":"Stop","fullyIdle":true,"terminationReason":"NO_TOOL_CALL",`+ids+`}`), "")
	check(t, "turn end recorded", opts(), "idle agy 1 110c93f6-c88f "+transcript)
	// A turn ended on an error: failed, not finished
	check(t, "a failed turn's start", agy("working", `{"hookEventName":"PreInvocation","invocationNum":0,"lastUserInput":"go on",`+ids+`}`), "")
	check(t, "its end", agy("idle", `{"hookEventName":"Stop","error":"quota exceeded","terminationReason":"ERROR",`+ids+`}`), "")
	check(t, "a failed turn ends idle", opts(), "idle agy 2 110c93f6-c88f "+transcript)
	check(t, "its boss note says failed", s.T("show-option", "-pqv", "-t", pane, "@dev_boss_note"), "failed: quota exceeded: ok")
	check(t, "malformed input", agy("working", `{`), "❌ dev state: agent: hook input: unexpected end of JSON input\n")
	check(t, "malformed input leaves the pane alone", opts(), "idle agy 2 110c93f6-c88f "+transcript)

	// An agy -p another agent runs in its pane inherits that pane's TMUX_PANE: it isn't the pane's agent
	s.T("set-option", "-p", "-t", boss, "@agent_state", "working")
	out, errOut := run(s.scriptEnv("TMUX_PANE="+boss), `{"hookEventName":"Stop",`+ids+`}`, "--agy", "idle")
	check(t, "a nested agy's hook", out+errOut+s.T("show-option", "-pqv", "-t", boss, "@agent_state"), "working")

	// Outside tmux, also in a desktop app session's environment: nothing, and nothing on stdout
	out, errOut = run(append(s.tmux.Env(), "CLAUDE_CODE_ENTRYPOINT=claude-desktop", "CLAUDE_CODE_HOST_SESSION_ID=x"),
		`{"hookEventName":"Stop",`+ids+`}`, "--agy", "idle")
	check(t, "outside tmux", out+errOut, "")

	// The worker's turn end queued a note for its boss, which the nested agy's hook on the boss's pane left there
	out, _ = run(s.scriptEnv("TMUX_PANE="+boss), `{"hook_event_name":"Stop"}`, "idle")
	check(t, "the worker's turn end reached its boss", out, `{"decision":"block","reason":"[dev boss inbox] 3 worker update(s):\n- ag needs a decision: Red or blue? (Red / Blue)\n- ag finished: ok\n- ag failed: quota exceeded: ok"}`+"\n")
}

// agyStub builds a stand-in for agy into dir: agy models lists two slugs; otherwise it logs its argv to
// DEV_STUB_LOG, shows its trust dialog for its directory until Enter, logs "trusted <dir>", then draws its
// idle input box and runs each line typed as a turn with dev state --agy hooks ("decline" ends its turn as a
// declined permission prompt does: on screen, without a Stop; "ask" opens a permission prompt, which a next line
// of 1 approves and any other declines; "question" asks one with its hook, which the next line answers).
func agyStub(t *testing.T, dir string) {
	t.Helper()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.go"), `package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const dev = `+strconv.Quote(devBin)+`

func logf(s string) {
	f, err := os.OpenFile(os.Getenv("DEV_STUB_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, s)
		f.Close()
	}
}

// transcript is its conversation's transcript, which Stop's note reads its reply from
var transcript = filepath.Join(os.TempDir(), fmt.Sprintf("agy-stub-%d.jsonl", os.Getpid()))

func hook(state, event string, num int, prompt string) {
	run(state, event, fmt.Sprintf("{\"hookEventName\":%q,\"invocationNum\":%d,\"lastUserInput\":%q,\"conversationId\":\"c1\",\"transcriptPath\":%q}", event, num, prompt, transcript))
}

// reply writes a turn's prompt and reply to its transcript
func reply(prompt, text string) {
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintf(f, "{\"source\":\"USER_EXPLICIT\",\"type\":\"USER_INPUT\",\"content\":%q}\n{\"source\":\"MODEL\",\"type\":\"PLANNER_RESPONSE\",\"content\":%q}\n", prompt, text)
		f.Close()
	}
}

// tool runs the PreToolUse or PostToolUse hook of a tool call, its toolCall JSON
func tool(event, call string) {
	run("working", event, fmt.Sprintf("{\"hookEventName\":%q,\"toolCall\":%s,\"conversationId\":\"c1\",\"transcriptPath\":%q}", event, call, transcript))
}

func run(state, event, in string) {
	cmd := exec.Command(dev, "state", "--agy", state)
	cmd.Stdin = strings.NewReader(in)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	if out, err := cmd.Output(); err != nil || len(out) > 0 || errOut.Len() > 0 {
		logf(fmt.Sprintf("hook %s: %v %q %q", event, err, out, errOut.String()))
	}
}

var (
	mu     sync.Mutex
	screen string
)

// draw shows text as the whole screen, as again on every resize
func draw(text string) {
	mu.Lock()
	defer mu.Unlock()
	screen = text
	fmt.Print("\033[H\033[2J" + text)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "models" {
		fmt.Print("gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\ngemini-3.8-flash-high\tGemini 3.8 Flash (High)\n")
		return
	}
	logf("agy " + strings.Join(os.Args[1:], " "))
	// As agy, which draws a pasted draft in its input box: the tty's echo would print it under the footer
	echo := exec.Command("stty", "-echo")
	echo.Stdin = os.Stdin
	_ = echo.Run()
	wd, _ := os.Getwd()
	dir, _ := filepath.EvalSymlinks(wd)
	fmt.Print("\033[?1049h")
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	go func() {
		for range resized {
			mu.Lock()
			text := screen
			mu.Unlock()
			draw(text)
		}
	}()
	draw("Accessing workspace:\n\n" + dir + "\n\nDo you trust the contents of this project?\n\n> Yes, I trust this folder\n  No, exit\n")
	in := bufio.NewReader(os.Stdin)
	if _, err := in.ReadString('\n'); err != nil {
		return
	}
	logf("trusted " + dir)
	rule := strings.Repeat("─", 40)
	box := rule + "\n>\n" + rule + "\n? for shortcuts\n"
	draw(box)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			time.Sleep(time.Hour)
			return
		}
		line = strings.TrimSpace(line)
		hook("working", "PreInvocation", 0, line)
		switch line {
		case "ask":
			const call = "{\"name\":\"run_command\",\"args\":{\"CommandLine\":\"echo hi\"}}"
			tool("PreToolUse", call)
			draw(rule + "\n> ask\n\n● Bash(echo hi)\nCommand\n" + rule + "\nRequesting permission for:\n   echo hi\n\nRun this command?\n> 1. Yes, run command\n  4. No, cancel\n\nesc to cancel\n")
			pick, err := in.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimSpace(pick) != "1" {
				line = "decline"
				break
			}
			draw(rule + "\n> ask\n\n● Bash(echo hi)\n\n⣷  Generating...\n" + rule + "\n>\n" + rule + "\nesc to cancel\n")
			tool("PostToolUse", call)
		case "question":
			const call = "{\"name\":\"ask_question\",\"args\":{\"questions\":[{\"is_multi_select\":false,\"options\":[\"(Recommended) Red\",\"Blue\"],\"question\":\"Do you prefer red or blue?\"}]}}"
			tool("PreToolUse", call)
			draw(rule + "\n> question\n\n? Do you prefer red or blue?\n\nQuestion\n" + rule + "\n\nQuestion 1/1: Do you prefer red or blue?\n\n> 1. (Recommended) Red\n  2. Blue\n  3. Write-in...\n\nesc to cancel\n")
			if _, err := in.ReadString('\n'); err != nil {
				return
			}
			draw(rule + "\n> question\n\n⣷  Generating...\n" + rule + "\n>\n" + rule + "\nesc to cancel\n")
			tool("PostToolUse", call)
		}
		time.Sleep(300 * time.Millisecond)
		if line == "decline" {
			draw(rule + "\n> decline\n\n● Bash(echo hi)\n  ⎿  User declined the tool call\n\n  ⎿  Interrupted · What should Antigravity CLI do instead?\n" + box)
			continue
		}
		hook("working", "PreInvocation", 1, line)
		reply(line, "ok")
		draw(rule + "\n> " + line + "\n\n  ok\n\n" + box)
		hook("idle", "Stop", 0, line)
	}
}
`)
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "agy"), "main.go")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agy stub: %v\n%s", err, out)
	}
}

// dev agent start --agent agy checks --model against agy models, starts agy with --model and --effort,
// confirms its trust dialog only in a worktree it made, records it idle once its input box shows, and a boss
// prompts it with --wait (also a turn ended without a Stop), reads it and closes it.
func TestAgentStartAgy(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	agyStub(t, stubs)
	repo := newRepo(t, "", "")
	// The stub runs its hooks itself; dev only looks for them before it records a new agy pane idle
	writeFile(t, filepath.Join(repo, ".agents/hooks.json"), `{"dev": {"PreInvocation": [{"type": "command", "command": "dev state --agy working"}], "Stop": [{"type": "command", "command": "dev state --agy idle"}]}}`)
	git(t, repo, "add", ".agents")
	git(t, repo, "commit", "-qm", "agy hooks")
	git(t, repo, "push", "-q", "origin", "main")
	boss := d.T("display", "-t", "=boot:", "-p", "#{pane_id}")
	env := append(d.scriptEnv(append(gitEnv, writeProjects(t, [2]string{"repo", repo}))...), "TMUX_PANE="+boss)
	log := func() string {
		raw, _ := os.ReadFile(d.stubLog)
		return string(raw)
	}

	out, errOut, status := devAgent(env, "", "start", "repo", "w-bad", "--agent", "agy", "--model", "bogus", "--timeout", "30s")
	check(t, "a model agy doesn't list", out+status+" "+errOut,
		"1 ❌ unknown --model \"bogus\" for agy, want one agy models lists: gemini-3.8-flash-low, gemini-3.8-flash-high\n")
	if _, err := os.Stat(filepath.Join(repo, ".claude/worktrees/w-bad")); err == nil {
		t.Error("❌ the refused start made its worktree")
	}
	if _, err := d.Try("has-session", "-t", "=repo-w-bad:"); err == nil {
		t.Error("❌ the refused start opened its session")
	}

	out, errOut, status = devAgent(env, "", "start", "repo", "w1", "--agent", "agy", "--model", "gemini-3.8-flash-low", "--effort", "high", "--json", "--timeout", "30s")
	var a map[string]string
	if err := json.Unmarshal([]byte(out), &a); err != nil || status != "0" {
		t.Fatalf("start agy: exit %s, %v: %q %q", status, err, out, errOut)
	}
	wt := filepath.Join(repo, ".claude/worktrees/w1")
	check(t, "agy worker", a["session"]+" "+a["agent"]+" "+a["state"]+" "+a["model"]+" "+a["effort"]+" "+a["started_by"], "repo-w1 Agy idle gemini-3.8-flash-low high boot")
	check(t, "started with its model and effort", strconv.FormatBool(strings.Contains(log(), "agy --model gemini-3.8-flash-low --effort high\n")), "true")
	check(t, "its trust dialog confirmed", strconv.FormatBool(strings.Contains(log(), "trusted "+wt+"\n")), "true")

	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "say ok", "--timeout", "30s")
	check(t, "prompt --wait returns at its Stop", status+" "+out+errOut, "0 idle\n")
	out, _, _ = devAgent(env, "", "read", "repo-w1", "--lines", "20")
	check(t, "read shows its answer", strconv.FormatBool(strings.Contains(out, "> say ok\n\n  ok")), "true")
	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "decline", "--timeout", "30s")
	check(t, "a turn ended without a Stop settles by its screen", status+" "+out+errOut, "0 idle\n")
	note := func() string { return d.T("show-option", "-pqv", "-t", "=repo-w1:1.2", "@dev_boss_note") }
	// A permission prompt, which no hook tells: its screen makes it a decision, approved it works on
	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "ask", "--timeout", "30s")
	check(t, "a permission prompt is waiting", status+" "+out+errOut, "0 waiting\n")
	waitFor(t, "its boss note says what it asks", func() bool { return note() == "needs a decision: allow Bash: echo hi" })
	_, errOut, status = devAgent(env, "", "prompt", "repo-w1", "yes please")
	check(t, "a prompt into its permission prompt refused", status+" "+errOut,
		"1 ❌ agent is waiting for a decision (permission_prompt): read it and answer with send-keys\n")
	d.T("send-keys", "-t", "=repo-w1:1.2", "1", "Enter")
	out, errOut, status = devAgent(env, "", "wait", "repo-w1", "--until", "idle", "--timeout", "30s")
	check(t, "approved, its turn goes on to its end", status+" "+out+errOut, "0 idle\n")
	check(t, "its turn's end replaced the note, with its reply", note(), "finished: ok")

	// Declined: no hook, its screen settles it idle
	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "ask", "--timeout", "30s")
	check(t, "another permission prompt", status+" "+out+errOut, "0 waiting\n")
	d.T("send-keys", "-t", "=repo-w1:1.2", "4", "Enter")
	out, errOut, status = devAgent(env, "", "wait", "repo-w1", "--until", "idle", "--timeout", "30s")
	check(t, "its declined prompt settles by its screen", status+" "+out+errOut, "0 idle\n")

	// A question: its hook records it, with its options for get --json
	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "question", "--timeout", "30s")
	check(t, "a question is waiting", status+" "+out+errOut, "0 waiting\n")
	waitFor(t, "its boss note asks it", func() bool { return note() == "needs a decision: Do you prefer red or blue? ((Recommended) Red / Blue)" })
	out, _, _ = devAgent(env, "", "get", "repo-w1", "--json")
	var got struct {
		State     string `json:"state"`
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Recommended bool   `json:"recommended"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.State != "waiting" || len(got.Questions) != 1 ||
		got.Questions[0].Question != "Do you prefer red or blue?" || len(got.Questions[0].Options) != 2 || !got.Questions[0].Options[0].Recommended {
		t.Errorf("❌ get --json of its question: %v %s", err, out)
	}
	_, errOut, status = devAgent(env, "", "prompt", "repo-w1", "blue")
	check(t, "a prompt into its question refused", status+" "+errOut,
		"1 ❌ agent is waiting for a decision (permission_prompt): read it and answer with send-keys\n")
	d.T("send-keys", "-t", "=repo-w1:1.2", "2", "Enter")
	out, errOut, status = devAgent(env, "", "wait", "repo-w1", "--until", "idle", "--timeout", "30s")
	check(t, "answered, its turn goes on to its end", status+" "+out+errOut, "0 idle\n")
	out, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "say ok again", "--timeout", "30s")
	check(t, "the next turn isn't taken for the declined one", status+" "+out+errOut, "0 idle\n")
	out, _, _ = devAgent(env, "", "read", "repo-w1", "--lines", "20")
	check(t, "read shows the next answer", strconv.FormatBool(strings.Contains(out, "> say ok again\n\n  ok")), "true")
	out, errOut, status = devAgent(env, "", "start", "repo", "w1", "--agent", "agy", "--json", "--timeout", "30s")
	check(t, "a reused agy session", status+" "+strconv.FormatBool(strings.Contains(out, `"state": "idle"`))+" "+errOut, "0 true ")
	check(t, "its trust dialog confirmed once", strconv.Itoa(strings.Count(log(), "trusted "+wt+"\n")), "1")
	_, errOut, status = devAgent(env, "", "prompt", "repo-w1", "--wait", "/clear")
	check(t, "/clear --wait refused", status+" "+errOut,
		"1 ❌ --wait confirms /clear by the new session its hook records, and this agent's hook records none at it (Codex, Grok, agy): send it without --wait\n")
	out, errOut, status = devAgent(env, "", "close", "repo-w1")
	check(t, "close", status+" "+out+errOut, "0 Closed repo-w1, removed worktree "+wt+" and branch w1\n")
	check(t, "every hook ran clean and printed nothing", strconv.FormatBool(strings.Contains("\n"+log(), "\nhook ")), "false")

	// A session running another agent is refused
	if err := os.Rename(sleeper(t, "codex"), filepath.Join(stubs, "codex")); err != nil {
		t.Fatal(err)
	}
	cxEnv := append(d.scriptEnv(writeProjects(t, [2]string{"cx", tempDir(t)})), "TMUX_PANE="+boss)
	if _, errOut, status := devAgent(cxEnv, "", "start", "cx", "--agent", "codex", "--timeout", "30s"); status != "0" {
		t.Fatalf("start codex: exit %s: %s", status, errOut)
	}
	out, errOut, status = devAgent(cxEnv, "", "start", "cx", "--agent", "agy", "--timeout", "30s")
	check(t, "a session running Codex", out+status+" "+errOut, "1 ❌ session cx already runs Codex, not agy\n")

	// A worktree that was there before, and the main checkout: their trust dialog is left for the user
	git(t, repo, "worktree", "add", "-q", "-b", "w2", filepath.Join(repo, ".claude/worktrees/w2"))
	for _, dir := range []string{filepath.Join(repo, ".claude/worktrees/w2"), repo} {
		args := []string{"start", "repo"}
		if dir != repo {
			args = append(args, "w2")
		}
		out, errOut, status = devAgent(env, "", append(args, "--agent", "agy", "--json", "--timeout", "30s")...)
		var b map[string]string
		if err := json.Unmarshal([]byte(out), &b); err != nil || status != "0" {
			t.Fatalf("start agy in %s: exit %s, %v: %q %q", dir, status, err, out, errOut)
		}
		check(t, "no state in "+dir, b["agent"]+" "+b["state"], "Agy ")
		check(t, "its trust dialog left for the user in "+dir, strconv.FormatBool(strings.HasPrefix(errOut, "⚠ agy asks whether to trust "+dir+", a folder this start didn't make")), "true")
		check(t, "not confirmed in "+dir, strconv.FormatBool(strings.Contains(log(), "trusted "+dir+"\n")), "false")
	}
	// Once the user answered it, the start run again records it idle
	d.T("send-keys", "-t", "=repo:1.2", "Enter")
	waitFor(t, "trusted by the user", func() bool { return strings.Contains(log(), "trusted "+repo+"\n") })
	out, errOut, status = devAgent(env, "", "start", "repo", "--agent", "agy", "--json", "--timeout", "30s")
	check(t, "the start run again", status+" "+strconv.FormatBool(strings.Contains(out, `"state": "idle"`))+" "+errOut, "0 true ")
}
