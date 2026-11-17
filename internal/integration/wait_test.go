//go:build integration

package integration

import (
	"cmp"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/wait"
)

// A planned wait on a real server: dev agent wait-until sets it (on itself, or --on a worker), the turn ending on it
// sends one waiting note, list/get --json carry it, the boss's Watch wakes the worker once it's due with one resumed
// note; a prompt ends it early, --cancel ends it without one, and a dialog during it still reports and ends it.
func TestPlannedWait(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	t.Setenv("XDG_STATE_HOME", stateDir) // Watch runs in this process
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	dev := func(pane, stdin string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	run := func(pane, st, stdin string) {
		t.Helper()
		if out, err := dev(pane, stdin, "state", st); err != nil || out != "" {
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
	log := filepath.Join(tmp, "w.log")
	s.T("new-session", "-d", "-s", "w", "bash", script, "", log)
	s.T("set-option", "-t", "=w:", "@dev_started_by", "boss")
	w := s.T("display", "-t", "=w:", "-p", "#{pane_id}")
	waitFor(t, "w box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", w), "❯") })
	s.T("new-session", "-d", "-s", "own", "bash", script, "", filepath.Join(tmp, "own.log"))
	own := s.T("display", "-t", "=own:", "-p", "#{pane_id}")
	run(own, "idle", `{"hook_event_name":"SessionStart"}`)
	b, ok, err := inbox.Here(s.tmux, boss)
	if !ok || err != nil {
		t.Fatalf("❌ no boss inbox: %v", err)
	}
	take := func() string {
		t.Helper()
		bt, err := b.Box.Take()
		if err != nil {
			t.Fatal(err)
		}
		return bt.String()
	}
	watch := func() {
		t.Helper()
		if err := inbox.Watch(s.tmux, control.RealClock, boss); err != nil {
			t.Fatal(err)
		}
	}
	opt := func(name string) string { return s.T("show-option", "-pqv", "-t", w, name) }

	// Set by the worker itself in its turn, which ends on it
	run(w, "working", prompt)
	out, err := dev(w, "", "agent", "wait-until", "+1m", "Codex  limit resets")
	until, _ := strconv.ParseInt(opt(wait.OptUntil), 10, 64)
	at := wait.Clock(time.Unix(until, 0), time.Now())
	check(t, "wait-until", out+errText(err), "w waits until "+at+" · Codex limit resets; dev wakes it then\n")
	if d := time.Until(time.Unix(until, 0)); d < 50*time.Second || d > time.Minute {
		t.Errorf("❌ +1m set %s from now", d)
	}
	check(t, "reason and prompts", opt(wait.OptReason)+"/"+opt(wait.OptPrompts), "Codex limit resets/1")
	run(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Codex is at its limit; waiting."}`)
	check(t, "one waiting note", take(), "[dev boss inbox] 1 worker update(s):\n- w waiting: until "+at+" (Codex limit resets), dev wakes it then")
	var got struct {
		Wait *wait.Wait `json:"wait"`
	}
	out, err = dev(boss, "", "agent", "get", "w", "--json")
	if err := cmp.Or(err, json.Unmarshal([]byte(out), &got)); err != nil || got.Wait == nil || got.Wait.Until.Unix() != until || got.Wait.Reason != "Codex limit resets" {
		t.Errorf("❌ get --json wait = %+v, %v (%s)", got.Wait, err, out)
	}
	out, _ = dev(boss, "", "agent", "list", "--json")
	check(t, "list --json carries it", strconv.FormatBool(strings.Count(out, `"wait"`) == 1), "true")

	// Not due: nothing happens
	watch()
	check(t, "no note before it's due", take(), "")
	check(t, "still held", opt(wait.OptUntil), strconv.FormatInt(until, 10))

	// Due: one wake prompt, one resumed note
	s.T("set-option", "-p", "-t", w, wait.OptUntil, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	due := wait.FromOptions(opt(wait.OptUntil), "Codex limit resets")
	watch()
	check(t, "resumed note", take(), "[dev boss inbox] 1 worker update(s):\n- w resumed: its wait "+due.Phrase(time.Now())+" ended, dev sent it: continue")
	waitFor(t, "w sent the wake prompt", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), inbox.WakePrompt(due, time.Now()))
	})
	check(t, "wait dropped", opt(wait.OptUntil)+opt(wait.OptReason)+opt(wait.OptPrompts), "")
	watch()
	check(t, "woken once", take(), "")

	// The boss sets one; a prompt ends it early
	out, err = dev(boss, "", "agent", "wait-until", "+30m", "CI reruns", "--on", "w")
	check(t, "--on", strconv.FormatBool(err == nil && strings.HasPrefix(out, "w waits until ")), "true")
	run(w, "working", prompt)
	check(t, "a prompt ends it", opt(wait.OptUntil), "")

	// --cancel ends it without a prompt; a second one has nothing to end
	run(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Done."}`)
	take()
	_, _ = dev(boss, "", "agent", "wait-until", "+30m", "CI reruns", "--on", "w")
	out, err = dev(boss, "", "agent", "wait-until", "--cancel", "--on", "w")
	check(t, "--cancel", out+errText(err), "w: planned wait ended\n")
	check(t, "cancelled", opt(wait.OptUntil), "")
	out, err = dev(boss, "", "agent", "wait-until", "--cancel", "--on", "w")
	check(t, "nothing to cancel", strconv.FormatBool(err != nil && strings.Contains(out, "w has no planned wait")), "true")

	// A dialog during the wait still reports; due, it ends the wait without a prompt
	_, _ = dev(boss, "", "agent", "wait-until", "+30m", "CI reruns", "--on", "w")
	run(w, "waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow Bash?"}`)
	check(t, "the decision reports", take(), "[dev boss inbox] 1 worker update(s):\n- w needs a decision: Allow Bash?")
	s.T("set-option", "-p", "-t", w, wait.OptUntil, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	before, _ := os.ReadFile(log)
	watch()
	after, _ := os.ReadFile(log)
	check(t, "no wake prompt into a dialog", strconv.FormatBool(len(after) == len(before)), "true")
	check(t, "the dialog ended it", opt(wait.OptUntil), "")

	// Refused: no boss would wake it, a reason ending in ;, more than 4h
	out, err = dev(own, "", "agent", "wait-until", "+5m", "x")
	check(t, "no boss", strconv.FormatBool(err != nil && strings.Contains(out, "own has no boss")), "true")
	out, err = dev(w, "", "agent", "wait-until", "+5h", "x")
	check(t, "over 4h", strconv.FormatBool(err != nil && strings.Contains(out, "more than 4h")), "true")
	out, err = dev(w, "", "agent", "wait-until", "+5m", "x;")
	check(t, "trailing ;", strconv.FormatBool(err != nil && strings.Contains(out, "can't end in ;")), "true")
	out, err = dev(w, "", "agent", "wait-until", "+5m", "limit\x1freset")
	check(t, "a control character", strconv.FormatBool(err != nil && strings.Contains(out, "no control characters")), "true")
	out, err = dev(w, "", "agent", "wait-until", "--cancel", "16:30")
	check(t, "--cancel with an argument", strconv.FormatBool(err != nil && !strings.Contains(out, "panic")), "true")

	// A turn ending on a wait still ends a run of rate limits; one ending past its time ends the wait, as finished
	run(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Done."}`)
	take()
	run(w, "working", prompt)
	s.T("set-option", "-p", "-t", w, "@dev_resumes", "2")
	_, _ = dev(w, "", "agent", "wait-until", "+30m", "CI reruns")
	run(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"Waiting on CI."}`)
	check(t, "resume count cleared", opt("@dev_resumes"), "")
	check(t, "waiting note", strconv.FormatBool(strings.Contains(take(), "- w waiting: until ")), "true")
	run(w, "working", `{"hook_event_name":"UserPromptSubmit","prompt":"<task-notification>"}`)
	_, _ = dev(w, "", "agent", "wait-until", "+30m", "CI reruns")
	s.T("set-option", "-p", "-t", w, wait.OptUntil, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	run(w, "idle", `{"hook_event_name":"Stop","last_assistant_message":"CI passed."}`)
	check(t, "past its time: finished, the wait ended", take()+"/"+opt(wait.OptUntil), "[dev boss inbox] 1 worker update(s):\n- w finished: CI passed./")

	// A rate limit's resume waits for a planned wait set since
	s.T("set-option", "-p", "-t", w, "@dev_resume_at", strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), ";",
		"set-option", "-pF", "-t", w, "@dev_resume_prompts", "#{@agent_prompts}", ";", "set-option", "-pF", "-t", w, "@dev_resume_session", "#{@agent_session}")
	_, _ = dev(boss, "", "agent", "wait-until", "+30m", "Codex limit resets", "--on", "w")
	before, _ = os.ReadFile(log)
	watch()
	after, _ = os.ReadFile(log)
	check(t, "resume held back", strconv.FormatBool(len(after) == len(before) && opt("@dev_resume_at") != ""), "true")
	_, _ = dev(boss, "", "agent", "wait-until", "--cancel", "--on", "w")
	s.T("set-option", "-pu", "-t", w, "@dev_resume_at")

	// Due with a draft in its input box: one failed note, the wait kept for the next round
	s.T("new-session", "-d", "-s", "d", "bash", script, "half a thought", filepath.Join(tmp, "d.log"))
	s.T("set-option", "-t", "=d:", "@dev_started_by", "boss")
	d := s.T("display", "-t", "=d:", "-p", "#{pane_id}")
	waitFor(t, "d box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", d), "half a thought") })
	run(d, "idle", `{"hook_event_name":"SessionStart"}`)
	_, _ = dev(boss, "", "agent", "wait-until", "+30m", "CI reruns", "--on", "d")
	s.T("set-option", "-p", "-t", d, wait.OptUntil, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	take()
	watch()
	check(t, "draft: one failed note", take(), "[dev boss inbox] 1 worker update(s):\n- d failed: waking it after its planned wait: its input box holds a draft, not woken yet")
	watch()
	check(t, "draft: once", take()+"/"+strconv.FormatBool(s.T("show-option", "-pqv", "-t", d, wait.OptUntil) != ""), "/true")
}

// errText is err's message after a newline, "" for none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return "\n" + err.Error()
}
