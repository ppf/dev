//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clearStub is a Claude stand-in for dev agent clear: hooks like agentStub's (SessionStart's output kept in
// $STUB_DIR/start-<n>.json, transcript $STUB_DIR/s<n>.jsonl), an empty input box drawn after each turn, and the
// files dev asks for copied from $STUB_DIR: save.md as the handoff, fix.md on a handoff fix, proof.md as the proof,
// proof-fix.md on a proof fix (a missing source writes nothing); with $STUB_DIR/noclear there it ignores /clear. On
// the save prompt it runs $STUB_DIR/on-save if
// there; with $STUB_DIR/decide there, a turn ends on a permission prompt.
func clearStub(t *testing.T, dir string) string {
	t.Helper()
	dev := shellQuote(devBin)
	return filepath.Join(writeStubs(t, dir, `n=0
box() { printf '────────────────────\n❯ \n────────────────────\n'; }
hook() { printf '{"hook_event_name":"%s","source":"%s","notification_type":"%s","session_id":"s%s","transcript_path":"%s/s%s.jsonl"}' "$2" "$3" "$4" "$n" "$STUB_DIR" "$n" | `+dev+` state "$1" > "$STUB_DIR/$2-$n.json"; }
put() { f=$(printf '%s' "$line" | sed "s/$1/\\1/"); [ -f "$STUB_DIR/$2" ] && cp "$STUB_DIR/$2" "$f"; }
stty -icanon 2>/dev/null
hook idle SessionStart startup
box
while IFS= read -r line; do
	if [ "$line" = /clear ] && [ -f "$STUB_DIR/noclear" ]; then
		box
		continue
	fi
	if [ "$line" = /clear ]; then
		hook clear SessionEnd
		n=$((n + 1))
		hook idle SessionStart clear
		box
		continue
	fi
	hook working UserPromptSubmit
	case "$line" in
	*"Save for a clear"*)
		put '.*write the handoff file \([^ ]*\) with.*' save.md
		[ -x "$STUB_DIR/on-save" ] && "$STUB_DIR/on-save" ;;
	*"fails dev's check"*) put '.*Your handoff \([^ ]*\) fails.*' fix.md ;;
	*"Your proof "*) put '.*Your proof \([^ ]*\) misses.*' proof-fix.md ;;
	*"Before touching any worker"*) put '.*Before touching any worker, write \([^ ]*\) with.*' proof.md ;;
	esac
	if [ -f "$STUB_DIR/decide" ]; then
		hook waiting Notification "" permission_prompt
	else
		hook idle Stop
	fi
	box
done`, "claude"), "claude")
}

// dev agent clear on a stub boss: each refusal leaves it uncleared and unprompted; a handoff that fails dev's check
// twice stops before the clear; a handoff fixed on the first miss is cleared, the fresh boss's start hook names it,
// and a proof fixed on its first miss passes.
func TestAgentClear(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	stub := clearStub(t, filepath.Join(tmp, "bin"))
	pane := func(session string) string { return s.T("display", "-t", "="+session+":", "-p", "#{pane_id}") }
	opt := func(p, name string) string { return s.T("display", "-t", p, "-p", "#{"+name+"}") }

	s.T("new-session", "-d", "-s", "manager", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	env := s.scriptEnv("XDG_STATE_HOME="+stateDir, "CLAUDECODE=1")
	manager := pane("manager")
	// boss opens a boss session of the manager's running the stub, its files in its own dir
	boss := func(name string) (string, string) {
		t.Helper()
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		s.T("new-session", "-d", "-s", name, "-e", "STUB_DIR="+dir, stub)
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		s.T("set-option", "-t", "="+name+":", "@dev_started_by", "manager")
		p := pane(name)
		s.T("set-option", "-p", "-t", p, "@dev_boss_pane", "coordinator")
		waitFor(t, name+" registered", func() bool { return opt(p, "@agent_session") == "s0" })
		s.T("set-option", "-p", "-t", p, "@agent_context", "30")
		return p, dir
	}
	dev := func(p string, args ...string) (string, string, string) {
		return runDev(append(env, "TMUX_PANE="+p), "", args...)
	}
	todo := func(p string, args ...string) {
		t.Helper()
		if _, e, st := dev(p, append([]string{"todo"}, args...)...); st != "0" {
			t.Fatalf("❌ dev todo %q: exit %s: %s", args, st, e)
		}
	}
	write := func(dir, name, text string) { writeFile(t, filepath.Join(dir, name), text) }

	app, appDir := boss("boss-app")
	todo(app, "add", "Send the release mail", "--details", "Draft ready, not sent; the user sends it Friday")
	todo(app, "ask", "Ship v2 now?", "--option", "Ship | starts the release (Recommended)", "--option", "Wait | parks it",
		"--details", "v2 is built; shipping starts the release worker")
	s.T("new-session", "-d", "-s", "boss-peer", "-e", "STUB_DIR="+tmp, stub)
	s.T("set-option", "-t", "=boss-peer:", "@dev_herd", "1")
	s.T("new-session", "-d", "-s", "app-fix", "sleep 600")
	s.T("set-option", "-t", "=app-fix:", "@dev_started_by", "boss-app")
	fix := pane("app-fix")
	// Set, not hooked: a worker's finished turn would paste a note into boss-app
	workerState := func(st string) {
		s.T("set-option", "-p", "-t", fix, "@agent_state", st, ";", "set-option", "-p", "-t", fix, "@agent_cmd", "sleep")
	}
	workerState("working")
	s.T("new-session", "-d", "-s", "plain", "-e", "STUB_DIR="+tmp, stub)
	waitFor(t, "plain registered", func() bool { return opt(pane("plain"), "@agent_session") == "s0" })

	prompts := opt(app, "@agent_prompts")
	refused := func(name, caller, target, want string, extra ...string) {
		t.Helper()
		out, e, st := dev(caller, append([]string{"agent", "clear", target}, extra...)...)
		if st == "0" || !strings.Contains(e, want) {
			t.Errorf("❌ %s: exit %s, %q %q; want an error with %q", name, st, out, e, want)
			return
		}
		if opt(app, "@agent_session") != "s0" || opt(app, "@agent_prompts") != prompts {
			t.Errorf("❌ %s: boss-app was prompted or cleared", name)
			return
		}
		t.Logf("✅ refused: %s", name)
	}
	refused("a peer boss's Claude", pane("boss-peer"), "boss-app", "cleared only by the manager")
	refused("its own pane", app, "boss-app", "this agent's own pane")
	refused("the manager from a Claude session", pane("boss-peer"), "manager", "the manager's prompts are the user's")
	refused("not a boss", manager, "plain", "plain isn't a boss")
	refused("a worker working", manager, "boss-app", "worker app-fix is working")
	workerState("idle")
	s.T("set-option", "-p", "-t", app, "@agent_context", "12")
	refused("context under 20%", manager, "boss-app", "its context is 12%, under 20%")
	s.T("set-option", "-pu", "-t", app, "@agent_context")
	refused("context share unknown", manager, "boss-app", "its context share is unknown")
	s.T("set-option", "-p", "-t", app, "@agent_context", "30")
	s.T("new-session", "-d", "-s", "app-new", "sleep 600")
	s.T("set-option", "-t", "=app-new:", "@dev_started_by", "boss-app")
	refused("a worker with no agent registered", manager, "boss-app", "worker app-new has no hook state")
	s.T("kill-session", "-t", "=app-new:")
	s.T("set-option", "-p", "-t", app, "@dev_handoff", strconv.Itoa(os.Getpid())+" "+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+" s0 /other.md")
	refused("another clear running", manager, "boss-app", "another dev agent clear runs for boss-app")
	s.T("set-option", "-pu", "-t", app, "@dev_handoff")
	s.T("set-option", "-pu", "-t", app, "@agent_transcript")
	refused("no transcript recorded", manager, "boss-app", "boss-app's hook records no transcript path")
	s.T("set-option", "-p", "-t", app, "@agent_transcript", filepath.Join(appDir, "s0.jsonl"))
	refused("--facts unreadable", manager, "boss-app", "--facts: open "+filepath.Join(tmp, "none.md"), "--facts", filepath.Join(tmp, "none.md"))
	writeFile(t, filepath.Join(tmp, "plain.md"), "the user is away\n")
	refused("--facts without numbered facts", manager, "boss-app", "no numbered facts", "--facts", filepath.Join(tmp, "plain.md"))

	// A running clear's hold: a worker's note to an idle boss with an empty box stays queued, not pasted
	held, _ := boss("boss-held")
	s.T("set-option", "-p", "-t", held, "@dev_handoff", strconv.Itoa(os.Getpid())+" "+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+" s0 /h.md")
	s.T("new-session", "-d", "-s", "held-w", "sleep 600")
	s.T("set-option", "-t", "=held-w:", "@dev_started_by", "boss-held")
	hw := pane("held-w")
	s.T("set-option", "-p", "-t", hw, "@agent_state", "working", ";", "set-option", "-p", "-t", hw, "@agent_cmd", "sleep")
	heldPrompts := opt(held, "@agent_prompts")
	if _, e, st := runDev(append(env, "TMUX_PANE="+hw), `{"hook_event_name":"Stop","last_assistant_message":"done"}`, "state", "idle"); st != "0" {
		t.Fatalf("❌ worker state: %s", e)
	}
	// The note waits in its inbox (the clear refuses on it), and nothing was pasted
	if _, e, st := dev(manager, "agent", "clear", "boss-held"); st == "0" || !strings.Contains(e, "inbox notes wait for it") || opt(held, "@agent_prompts") != heldPrompts {
		t.Errorf("❌ held boss: exit %s, prompts %q → %q: %s", st, heldPrompts, opt(held, "@agent_prompts"), e)
	}
	s.T("set-option", "-pu", "-t", held, "@dev_handoff")
	t.Log("✅ a hold keeps a worker's note queued, unpasted")
	if _, e, st := runDev(env, "", "agent", "clear", "boss-app"); st == "0" || !strings.Contains(e, "cleared only by its manager's coordinator pane") {
		t.Errorf("❌ a Claude session outside tmux: exit %s, %s", st, e)
	}
	orphan, _ := boss("boss-orphan")
	s.T("set-option", "-u", "-t", "=boss-orphan:", "@dev_started_by")
	if _, e, st := dev(manager, "agent", "clear", "boss-orphan"); st == "0" || !strings.Contains(e, "cleared only by the manager") || opt(orphan, "@agent_session") != "s0" {
		t.Errorf("❌ a boss nobody started, from the manager: exit %s, %s", st, e)
	}

	// handoff is a passing handoff of the boss whose files are in dir
	handoff := func(dir string) string {
		return "## Key facts\n1. The mail goes Friday. confirmed: #1\n2. v2 is ready. unconfirmed\n\n## Open asks\n#2: Ship starts the release, Wait parks it.\n\n" +
			"## Work in flight\nNone.\n\n## The user's standing words\nMerge after green CI.\n\n## Open risks\nNone.\n\n## Old transcript\n" + filepath.Join(dir, "s0.jsonl") + "\n"
	}
	transcript, good := filepath.Join(appDir, "s0.jsonl"), handoff(appDir)
	proof := "## Key facts\n1. Mail on Friday. confirmed: #1\n2. v2 ready. unconfirmed\n\n## Open asks\n#2: Ship → release worker; Wait → park.\n\n" +
		"## Roles\nThe manager answers my workers' permission prompts and dialogs and relays my asks.\n"
	// stopped runs dev agent clear on name (with extra args) from caller with env, and wants it to fail with every one of want and
	// the boss on conversation s<n>
	stopped := func(t *testing.T, env []string, extra []string, caller, name, conversation string, want ...string) {
		t.Helper()
		_, e, st := runDev(append(env, "TMUX_PANE="+caller), "", append([]string{"agent", "clear", name}, extra...)...)
		for _, w := range want {
			if st == "0" || !strings.Contains(e, w) {
				t.Fatalf("❌ exit %s, want %q in: %s", st, w, e)
			}
		}
		check(t, name+"'s conversation", opt(pane(name), "@agent_session"), conversation)
		check(t, name+"'s inbox hold released", opt(pane(name), "@dev_handoff"), "")
	}

	t.Run("handoff missing twice, from the user's shell: not cleared", func(t *testing.T) {
		web, webDir := boss("boss-web")
		todo(web, "add", "Bare item")
		write(webDir, "save.md", strings.Replace(handoff(webDir), "## Open risks", "## Risks", 1))
		write(webDir, "fix.md", strings.Replace(handoff(webDir), "## Open risks", "## Risks", 1))
		shell := slices.DeleteFunc(slices.Clone(env), func(v string) bool { return strings.HasPrefix(v, "CLAUDECODE=") })
		_, e, st := runDev(append(shell, "TMUX_PANE="+manager), "", "agent", "clear", "boss-web")
		if st == "0" || !strings.Contains(e, "still misses: sections missing: ## Open risks") || !strings.Contains(e, "without details on the list: #1") ||
			!strings.Contains(e, "nothing cleared") {
			t.Fatalf("❌ exit %s: %s", st, e)
		}
		if opt(web, "@agent_session") != "s0" {
			t.Errorf("❌ boss-web was cleared")
		}
		t.Log("✅ two misses stop before the clear")
	})

	t.Run("handoff never written: not cleared", func(t *testing.T) {
		boss("boss-mute")
		stopped(t, env, nil, manager, "boss-mute", "s0", "wasn't written", "nothing cleared")
	})

	t.Run("save turn ends on a permission prompt: not cleared", func(t *testing.T) {
		_, dir := boss("boss-ask")
		write(dir, "decide", "")
		stopped(t, env, nil, manager, "boss-ask", "s0", "turn ended on a decision (permission_prompt)", "nothing cleared")
	})

	t.Run("a worker's note during the save: held, then refuses", func(t *testing.T) {
		_, dir := boss("boss-busy")
		write(dir, "save.md", handoff(dir))
		s.T("new-session", "-d", "-s", "busy-w", "sleep 600")
		s.T("set-option", "-t", "=busy-w:", "@dev_started_by", "boss-busy")
		w := pane("busy-w")
		s.T("set-option", "-p", "-t", w, "@agent_state", "idle", ";", "set-option", "-p", "-t", w, "@agent_cmd", "sleep")
		// The worker's turn ends during the save: its finished note queues for boss-busy
		write(dir, "on-save", "#!/bin/sh\nprintf '{\"hook_event_name\":\"Stop\",\"last_assistant_message\":\"done\"}' | TMUX_PANE="+w+" "+shellQuote(devBin)+" state idle\n")
		if err := os.Chmod(filepath.Join(dir, "on-save"), 0o755); err != nil {
			t.Fatal(err)
		}
		stopped(t, env, nil, manager, "boss-busy", "s0", "inbox notes wait for it", "nothing cleared")
	})

	t.Run("proof never written: cleared, paths and the proof prompt printed", func(t *testing.T) {
		_, dir := boss("boss-quiet")
		write(dir, "save.md", handoff(dir))
		stopped(t, env, nil, manager, "boss-quiet", "s1", "wasn't written", "the proof prompt to send by hand first")
	})

	t.Run("/clear not confirmed: the boss may still clear, proof prompt printed", func(t *testing.T) {
		_, dir := boss("boss-stuck")
		write(dir, "save.md", handoff(dir))
		write(dir, "noclear", "")
		stopped(t, env, []string{"--stall", "2s"}, manager, "boss-stuck", "s0", "/clear: ", "it may still clear", "the proof prompt to send by hand first")
	})
	t.Run("proof misses twice: cleared, paths and the proof prompt printed", func(t *testing.T) {
		_, dir := boss("boss-lax")
		write(dir, "save.md", handoff(dir))
		write(dir, "proof.md", strings.Replace(proof, "## Roles", "## Who", 1))
		write(dir, "proof-fix.md", strings.Replace(proof, "## Roles", "## Who", 1))
		stopped(t, env, nil, manager, "boss-lax", "s1", "still misses: sections missing: ## Roles", "the proof prompt to send by hand first", "command ends): [dev agent clear] Read ",
			"old transcript "+filepath.Join(dir, "s0.jsonl"))
	})

	t.Run("cleared and proved", func(t *testing.T) {
		// The caller's fact, copied in as key fact 3
		carried := strings.Replace(good, "\n\n## Open asks", "\n3. The user leaves at 18:00. confirmed: said\n\n## Open asks", 1)
		write(appDir, "save.md", strings.Replace(carried, "#2:", "Ask:", 1))
		write(appDir, "fix.md", carried)
		proof := strings.Replace(proof, "\n\n## Open asks", "\n3. Leaves at 18:00. confirmed: said\n\n## Open asks", 1)
		write(appDir, "proof.md", strings.Replace(proof, "2. v2 ready. unconfirmed\n", "", 1))
		write(appDir, "proof-fix.md", proof)
		facts := filepath.Join(tmp, "facts.md")
		writeFile(t, facts, "1. The user leaves at 18:00. confirmed: said\n")
		out, e, st := dev(manager, "agent", "clear", "boss-app", "--facts", facts)
		if st != "0" {
			t.Fatalf("❌ exit %s: %s\n%s", st, e, out)
		}
		check(t, "new conversation", opt(app, "@agent_session"), "s1")
		check(t, "handoff option unset", opt(app, "@dev_handoff"), "")
		for _, want := range []string{"Handoff misses: open items not named: #2", "Proof misses: key fact 2 not restated: v2 is ready. unconfirmed", "Cleared: boss-app"} {
			if !strings.Contains(e, want) {
				t.Errorf("❌ progress lacks %q: %s", want, e)
			}
		}
		handoffs, _ := filepath.Glob(filepath.Join(stateDir, "dev", "handoff", "boss-app", "*.md"))
		var handoff, proofPath, factsCopy string
		for _, h := range handoffs {
			switch {
			case strings.HasSuffix(h, "-proof.md"):
				proofPath = h
			case strings.HasSuffix(h, "-facts.md"):
				factsCopy = h
			default:
				handoff = h
			}
		}
		if handoff == "" || proofPath == "" || factsCopy == "" {
			t.Fatalf("❌ files %q", handoffs)
		}
		for _, want := range []string{"Cleared boss-app: 3 key facts restated", "handoff: " + handoff, "proof: " + proofPath, "old transcript: " + transcript} {
			if !strings.Contains(out, want) {
				t.Errorf("❌ output lacks %q: %s", want, out)
			}
		}
		if data, _ := os.ReadFile(factsCopy); !strings.Contains(string(data), "leaves at 18:00") {
			t.Errorf("❌ facts copy = %q", data)
		}
		start, _ := os.ReadFile(filepath.Join(appDir, "SessionStart-1.json"))
		if !strings.Contains(string(start), "Your handoff from before this /clear: "+handoff) || !strings.Contains(string(start), "Ship v2 now?") {
			t.Errorf("❌ fresh boss's start context = %s", start)
		}
		t.Log("✅ cleared; start hook named the handoff; proof fixed once")
	})
}
