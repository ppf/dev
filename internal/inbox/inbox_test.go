package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

func TestFromHook(t *testing.T) {
	long := strings.Repeat("word ", 60)
	tests := []struct {
		name, input string
		want        string
		ok          bool
	}{
		{"finished", `{"hook_event_name":"Stop","last_assistant_message":"Done.\n\nPR opened"}`, "w finished: Done. PR opened", true},
		{"finished without a message", `{"hook_event_name":"Stop"}`, "w finished", true},
		{"failed", `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "w failed: rate_limit", true},
		{"failed with Claude's message", `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 7:10pm (Europe/Warsaw)"}`,
			"w failed: rate_limit: You've hit your session limit · resets 7:10pm (Europe/Warsaw)", true},
		{"failed with details", `{"hook_event_name":"StopFailure","error":"rate_limit","error_details":"429 Too Many Requests"}`, "w failed: rate_limit (429 Too Many Requests)", true},
		{"failed with a message and details", `{"hook_event_name":"StopFailure","error":"server_error","error_details":"Overloaded","last_assistant_message":"API Error: 529"}`,
			"w failed: server_error: API Error: 529 (Overloaded)", true},
		{"failed without an error", `{"hook_event_name":"StopFailure"}`, "w failed: stopped on an error", true},
		{"permission", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`,
			"w needs a decision: Claude needs your permission to use Bash", true},
		{"question", `{"hook_event_name":"Notification","notification_type":"elicitation_dialog","message":"Pick one"}`, "w needs a decision: Pick one", true},
		{"codex permission", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"Delete build/"}}`,
			"w needs a decision: Delete build/", true},
		{"codex patch permission", `{"hook_event_name":"PermissionRequest","tool_name":"apply_patch","tool_input":"*** Begin Patch"}`,
			"w needs a decision: permission request apply_patch", true},
		{"codex permission without a tool", `{"hook_event_name":"PermissionRequest"}`, "w needs a decision: permission request", true},
		{"idle reminder makes none", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, "", false},
		{"tool event makes none", `{"hook_event_name":"PostToolUse"}`, "", false},
		{"malformed makes none", `{`, "", false},
		{"truncated", `{"hook_event_name":"Stop","last_assistant_message":"` + long + `"}`, "w finished: " + strings.Repeat("word ", 39) + "word…", true},
		{"asks", `{"hook_event_name":"Stop","last_assistant_message":"PR #12 merged.\n\nShould I start the next item?"}`, "w asks: Should I start the next item?", true},
		{"asks in bold", `{"hook_event_name":"Stop","last_assistant_message":"Done.\n**Merge it now?**\n"}`, "w asks: **Merge it now?**", true},
		{"asks with its options", `{"hook_event_name":"Stop","last_assistant_message":"Two ways:\nWhich one?\n1. Rewrite\n2. Patch"}`, "w asks: Which one? 1. Rewrite 2. Patch", true},
		{"a question further up isn't asked", `{"hook_event_name":"Stop","last_assistant_message":"Which?\n- a\n- b\n- c\n- d\n- e"}`, "w finished: Which? - a - b - c - d - e", true},
		{"a question answered after isn't asked", `{"hook_event_name":"Stop","last_assistant_message":"Why did it fail?\nThe cache was stale.\nFixed."}`, "w finished: Why did it fail? The cache was stale. Fixed.", true},
		{"code ending in ? isn't asked", `{"hook_event_name":"Stop","last_assistant_message":"Made it optional: ` + "`colou?r?`" + `"}`, "w finished: Made it optional: `colou?r?`", true},
		{"long options keep the question", `{"hook_event_name":"Stop","last_assistant_message":"Which approach?\n1. ` + long + `\n2. b"}`, "w asks: Which approach? 1. " + strings.Repeat("word ", 36) + "…", true},
		{"a heading isn't asked", `{"hook_event_name":"Stop","last_assistant_message":"## What changed?\n- a\n- b"}`, "w finished: ## What changed? - a - b", true},
		{"a long question keeps its end", `{"hook_event_name":"Stop","last_assistant_message":"` + long + `ok?"}`, "w asks: …" + strings.Repeat("word ", 39) + "ok?", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := FromHook("w", []byte(tt.input))
			if ok != tt.ok || ok && n.String() != tt.want {
				t.Errorf("FromHook = %q, %v; want %q, %v", n.String(), ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResetTime(t *testing.T) {
	warsaw := mustZone(t, "Europe/Warsaw")
	now := time.Date(2026, 10, 20, 18, 30, 0, 0, warsaw)
	tests := []struct {
		name, msg string
		want      time.Time
		ok        bool
	}{
		{"today", "You've hit your session limit · resets 7:10pm (Europe/Warsaw)", time.Date(2026, 10, 20, 19, 10, 0, 0, warsaw), true},
		{"on the hour", "You've hit your session limit · resets 8pm (Europe/Warsaw)", time.Date(2026, 10, 20, 20, 0, 0, 0, warsaw), true},
		{"tomorrow", "You've hit your session limit · resets 2:05am (Europe/Warsaw)", time.Date(2026, 10, 21, 2, 5, 0, 0, warsaw), true},
		{"just passed", "You've hit your session limit · resets 6pm (Europe/Warsaw)", time.Date(2026, 10, 20, 18, 0, 0, 0, warsaw), true},
		{"just passed, before midnight", "resets 11:55pm (Europe/Warsaw)", time.Date(2026, 10, 20, 23, 55, 0, 0, warsaw), true},
		{"noon", "resets 12pm (Europe/Warsaw)", time.Date(2026, 10, 21, 12, 0, 0, 0, warsaw), true},
		{"a date", "You've hit your weekly limit · resets Oct 22 at 8pm (Europe/Warsaw)", time.Date(2026, 10, 22, 20, 0, 0, 0, warsaw), true},
		{"next year's date", "resets Jan 3 at 9am (Europe/Warsaw)", time.Date(2027, 1, 3, 9, 0, 0, 0, warsaw), true},
		{"another zone", "resets 7:10pm (America/New_York)", time.Date(2026, 10, 20, 19, 10, 0, 0, mustZone(t, "America/New_York")), true},
		{"no zone: now's", "resets 7:10pm", time.Date(2026, 10, 20, 19, 10, 0, 0, warsaw), true},
		{"the last one", "resets 1pm (Europe/Warsaw)\nresets 7:10pm (Europe/Warsaw)", time.Date(2026, 10, 20, 19, 10, 0, 0, warsaw), true},
		{"unknown zone", "resets 7:10pm (Mars/Olympus)", time.Time{}, false},
		{"no reset", "You've hit your monthly spend limit. Run /usage-credits to manage your limit", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResetTime(tt.msg, now)
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("ResetTime = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		17*time.Minute + 59*time.Second: "17m", time.Hour: "1h", 65 * time.Minute: "1h 5m", 300 * time.Minute: "5h",
	} {
		if got := Span(d); got != want {
			t.Errorf("span(%s) = %q, want %q", d, got, want)
		}
	}
}

func testBox(t *testing.T) Box {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b, err := Open("/private/tmp/tmux-501/default", "$3")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	b, _ := Open("/private/tmp/tmux-501/default", "$3")
	if b.path != "/state/dev/inbox/private_tmp_tmux-501_default-_3.json" {
		t.Errorf("path = %q", b.path)
	}
}

func note(worker, text string) Note {
	n, _ := FromHook(worker, []byte(`{"hook_event_name":"Stop","last_assistant_message":"`+text+`"}`))
	return n
}

func TestAddDedupesAndTakes(t *testing.T) {
	b := testBox(t)
	for _, n := range []Note{note("a", "x"), note("a", "x"), note("b", "x"), note("a", "y")} {
		if _, err := b.Add(n, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	bt, err := b.Take()
	if err != nil {
		t.Fatal(err)
	}
	want := "[dev boss inbox] 3 worker update(s):\n- a finished: x\n- b finished: x\n- a finished: y"
	if bt.String() != want {
		t.Errorf("batch = %q, want %q", bt, want)
	}
	if bt, _ := b.Take(); bt.String() != "" {
		t.Errorf("second take = %q", bt)
	}
	// Dedupe outlives a take: the same note again is still a repeat
	if added, _ := b.Add(note("a", "y"), time.Time{}); added {
		t.Error("repeat after take was queued")
	}
}

// An Asks note is kept for Watch to repeat until the worker's next prompt (Forget).
func TestAddAsksKeepsQuestion(t *testing.T) {
	b := testBox(t)
	n, _ := FromHook("w", []byte(`{"hook_event_name":"Stop","last_assistant_message":"Merge it?"}`))
	if added, err := b.Add(n, time.Unix(100, 0)); !added || err != nil {
		t.Fatalf("Add = %v, %v", added, err)
	}
	ws, err := b.workers()
	if err != nil || ws["w"] == nil || ws["w"].Question != "Merge it?" || ws["w"].AskedAt != 100 {
		t.Fatalf("workers = %+v, %v; want the question kept", ws["w"], err)
	}
	if err := b.Forget("w"); err != nil {
		t.Fatal(err)
	}
	if ws, _ := b.workers(); ws["w"].Question != "" {
		t.Errorf("question %q kept past a new prompt", ws["w"].Question)
	}
}

func TestAddCaps(t *testing.T) {
	b := testBox(t)
	for i := range maxNotes + 3 {
		_, _ = b.Add(note("w", strings.Repeat("x", i+1)), time.Time{})
	}
	bt, _ := b.Take()
	if len(bt.Notes) != maxNotes || bt.Dropped != 3 || !strings.HasSuffix(bt.String(), "(3 older update(s) dropped: run dev agent list --mine and dev agent messages)") {
		t.Errorf("got %d notes, %d dropped", len(bt.Notes), bt.Dropped)
	}
}

func TestTakeForPasteRateLimits(t *testing.T) {
	b := testBox(t)
	now := time.Unix(1000, 0)
	_, _ = b.Add(note("a", "1"), now)
	if bt, _ := b.TakeForPaste(now); len(bt.Notes) != 1 {
		t.Fatalf("first paste took %d", len(bt.Notes))
	}
	_, _ = b.Add(note("a", "2"), now)
	if bt, _ := b.TakeForPaste(now.Add(pasteGap - time.Second)); len(bt.Notes) != 0 {
		t.Error("paste inside the gap")
	}
	bt, _ := b.TakeForPaste(now.Add(pasteGap))
	if len(bt.Notes) != 1 {
		t.Fatal("no paste after the gap")
	}
	_, _ = b.Add(note("b", "3"), now)
	if err := b.Restore(bt); err != nil {
		t.Fatal(err)
	}
	// A restored batch is due at once, ahead of newer notes
	got, _ := b.TakeForPaste(now.Add(pasteGap))
	if got.String() != "[dev boss inbox] 2 worker update(s):\n- a finished: 2\n- b finished: 3" {
		t.Errorf("after restore: %q", got)
	}
}

const (
	rule = "\x1b[38;5;244m" + "────────────────────────────────────────" + "\n\x1b[39m"
	foot = "  \x1b[38;5;110m~/repo\x1b[39m\n  auto mode on\n"
)

func TestInputEmpty(t *testing.T) {
	tests := []struct {
		name, screen string
		want         bool
	}{
		{"placeholder", "output\n" + rule + "❯\u00a0\x1b[2mTry \"create a util\"\x1b[0m\n" + rule + foot, true},
		{"blank", "output\n" + rule + "❯\u00a0\n" + rule + foot, true},
		{"named session", "output\n\x1b[38;5;244m──────────────────────────────────────── nightly-scan ─\n\x1b[39m❯\u00a0\n" + rule + foot, true},
		{"old box", "╭────────────────────────╮\n│ > \x1b[2mTry it\x1b[22m │\n╰────────────────────────╯\n", false},
		{"draft", "output\n" + rule + "❯\u00a0fix the login\n" + rule + foot, false},
		{"true-colour draft", "output\n" + rule + "❯\u00a0\x1b[38;2;100;100;100mfix the login\x1b[39m\n" + rule + foot, false},
		{"256-colour draft", "output\n" + rule + "❯\u00a0\x1b[38;5;2mfix\x1b[39m\n" + rule + foot, false},
		{"draft styled after dim reset", "output\n" + rule + "❯\u00a0\x1b[2m\x1b[0mfix\n" + rule + foot, false},
		{"multi-line draft", "output\n" + rule + "❯\u00a0\nsecond line\n" + rule + foot, false},
		{"no box (a dialog)", "Do you want to proceed?\n❯ 1. Yes\n  2. No\n", false},
		{"rule without prompt row", "output\n" + rule + "Esc to cancel\n" + rule + foot, false},
		{"empty screen", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InputEmpty(tt.screen); got != tt.want {
				t.Errorf("InputEmpty = %v, want %v", got, tt.want)
			}
		})
	}
}

// observer runs Watch's rounds on one state, without tmux: each round returns the queued batch.
type observer struct {
	t   *testing.T
	s   state
	now time.Time
}

func newObserver(t *testing.T) *observer {
	return &observer{t: t, s: state{Last: map[string]string{}, Workers: map[string]*health{}}, now: time.Unix(100_000, 0)}
}

func (o *observer) ago(d time.Duration) string { return strconv.FormatInt(o.now.Add(-d).Unix(), 10) }

// round observes sights after d passes and returns the batch queued, and the notes holding.
func (o *observer) round(d time.Duration, sights ...sight) (queued string, holding []string) {
	o.t.Helper()
	o.now = o.now.Add(d)
	live := map[string]bool{}
	for _, w := range sights {
		live[w.Session] = true
	}
	for _, n := range o.s.observe(o.now, live, sights) {
		holding = append(holding, n.String())
	}
	return o.s.take().String(), holding
}

func worker(state, waitingFor, since string) sight {
	return sight{paneRow: paneRow{Session: "w", Since: since, WaitingFor: waitingFor}, trusted: state}
}

// A working worker is stuck once its hooks and screen both stay still StuckWorking, once per stretch.
func TestObserveProgress(t *testing.T) {
	o := newObserver(t)
	w := worker("working", "", o.ago(5*time.Hour))
	w.Active, w.screen = o.ago(StuckWorking-time.Minute), "s1"
	if got, _ := o.round(0, w); got != "" {
		t.Errorf("a long turn with recent tool calls: %q", got)
	}
	if got, _ := o.round(StuckWorking-time.Second, w); got != "" {
		t.Errorf("before the screen sat still StuckWorking: %q", got)
	}
	got, holding := o.round(time.Second, w)
	if want := "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 15m"; got != want {
		t.Errorf("stalled = %q, want %q", got, want)
	}
	w.Context = "85" // a note between doesn't let the stuck one repeat
	if got, _ := o.round(0, w); got != "[dev boss inbox] 1 worker update(s):\n- w high context: 85% used" {
		t.Errorf("context in the stretch = %q", got)
	}
	if got, holding2 := o.round(time.Minute, w); got != "" || len(holding2) != 1 || holding2[0] != "w stuck: no progress for 16m" || len(holding) != 1 {
		t.Errorf("same stretch = %q, holding %q then %q; want no repeat, its minutes current", got, holding, holding2)
	}
	w.screen = "s2"
	if got, _ := o.round(time.Minute, w); got != "" {
		t.Errorf("screen moved on: %q", got)
	}
	w.parked = true
	if got, _ := o.round(StuckWorking, w); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 15m, waiting on its background tasks" {
		t.Errorf("next stretch, parked on background tasks = %q", got)
	}
	w.Active = strconv.FormatInt(o.now.Unix(), 10) // a subagent's tool call
	if got, _ := o.round(StuckWorking-time.Second, w); got != "" {
		t.Errorf("a subagent's recent tool call: %q", got)
	}
}

// Without @agent_active (an older dev's hooks) progress falls back to the state's start.
func TestObserveProgressWithoutActive(t *testing.T) {
	o := newObserver(t)
	w := worker("working", "", o.ago(StuckWorking-time.Minute))
	w.screen = "s1"
	if got, _ := o.round(time.Minute, w); got != "" {
		t.Errorf("first sight: %q; want its screen taken as progress", got)
	}
	if got, _ := o.round(StuckWorking, w); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 15m" {
		t.Errorf("stalled = %q", got)
	}
}

func TestObserveDecision(t *testing.T) {
	o := newObserver(t)
	if got, _ := o.round(0, worker("waiting", "permission_prompt", o.ago(StuckDecision))); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: waiting on a decision for 3m, unanswered" {
		t.Errorf("unanswered decision = %q", got)
	}
	if got, _ := o.round(time.Minute, worker("waiting", "idle_prompt", o.ago(time.Hour))); got != "" {
		t.Errorf("idle reminder isn't a decision: %q", got)
	}
}

// A decision handed to the Manager goes stuck as "leave it" while the Manager is there; gone, the boss gets it plain.
func TestObserveDecisionHanded(t *testing.T) {
	o := newObserver(t)
	w := worker("waiting", "permission_prompt", o.ago(StuckDecision))
	w.managed = true
	o.s.health("w").Handed = "an earlier wait"
	const plain = "[dev boss inbox] 1 worker update(s):\n- w stuck: waiting on a decision for 3m, unanswered"
	if got, _ := o.round(0, w); got != plain {
		t.Errorf("another wait handed = %q, want %q", got, plain)
	}
	o = newObserver(t)
	o.s.health("w").Handed = w.Since
	got, holding := o.round(0, w)
	if want := plain + "; " + leaveToManager; got != want || len(holding) != 1 || !strings.HasSuffix(holding[0], leaveToManager) {
		t.Errorf("handed = %q, holding %q; want %q", got, holding, want)
	}
	w.managed = false
	if got, _ := o.round(time.Minute, w); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: waiting on a decision for 4m, unanswered" {
		t.Errorf("Manager gone = %q, want the plain note", got)
	}
	w.managed = true
	if got, holding := o.round(time.Minute, w); got != "" || len(holding) != 1 || strings.HasSuffix(holding[0], leaveToManager) {
		t.Errorf("Manager back = %q, holding %q; want it kept by the boss", got, holding)
	}
}

// A question a turn ended on is repeated while the worker rests: after AskAgain, then twice as long each time.
func TestObserveAsksAgain(t *testing.T) {
	o := newObserver(t)
	o.s.health("w").Question, o.s.health("w").AskedAt = "Merge it?", o.now.Unix()
	idle := worker("idle", "", o.ago(0))
	if got, _ := o.round(AskAgain-time.Second, idle); got != "" {
		t.Errorf("before AskAgain: %q", got)
	}
	if got, _ := o.round(time.Second, worker("waiting", "idle_prompt", o.ago(0))); got != "[dev boss inbox] 1 worker update(s):\n- w asks: Merge it? (unanswered for 30m)" {
		t.Errorf("first repeat = %q", got)
	}
	if got, _ := o.round(AskAgain-time.Second, idle); got != "" {
		t.Errorf("before twice AskAgain: %q", got)
	}
	if got, _ := o.round(time.Second, idle); got != "[dev boss inbox] 1 worker update(s):\n- w asks: Merge it? (unanswered for 1h)" {
		t.Errorf("second repeat = %q", got)
	}
	o.now = o.now.Add(5 * time.Hour)
	if got, _ := o.round(0, worker("working", "", o.ago(0))); got != "" {
		t.Errorf("working: %q", got)
	}
	if got, _ := o.round(time.Minute, idle); got != "[dev boss inbox] 1 worker update(s):\n- w asks: Merge it? (unanswered for 6h 1m)" {
		t.Errorf("past several waits = %q; want one repeat", got)
	}
	if got, _ := o.round(time.Minute, idle); got != "" {
		t.Errorf("right after: %q", got)
	}
	if got, _ := o.round(0); got != "" || len(o.s.Workers) != 0 {
		t.Errorf("worker gone: %q, health %v", got, o.s.Workers)
	}
}

// An idle worker's conclusive PR check with merge conflicts or no CI makes one note per idle stretch.
func TestObservePR(t *testing.T) {
	o := newObserver(t)
	since := o.ago(StuckIdlePR)
	add := func(pr prCheck) string {
		t.Helper()
		if n, ok := o.s.observePR(o.now, "w", since, false, pr); ok != (n.Worker != "") {
			t.Errorf("observePR = %+v, %v", n, ok)
		}
		return o.s.take().String()
	}
	if got := add(prCheck{number: 12, noChecks: true}); got != "" || o.s.health("w").PRChecked != "" {
		t.Errorf("inconclusive = %q, PRChecked %q; want no note, checked again", got, o.s.health("w").PRChecked)
	}
	if got := add(prCheck{number: 12, done: true, conflict: true, noChecks: true}); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: idle for 15m, PR #12 has merge conflicts and no CI checks" {
		t.Errorf("conflicted PR = %q", got)
	}
	if o.s.health("w").PRChecked != since {
		t.Errorf("PRChecked = %q, want the stretch %q", o.s.health("w").PRChecked, since)
	}
	o.round(0, worker("idle", "", since), worker("idle", "", since)) // a note between doesn't let it repeat
	if got := add(prCheck{number: 12, done: true, conflict: true}); got != "" {
		t.Errorf("same stretch: %q", got)
	}
	o.now = o.now.Add(time.Minute)
	since = o.ago(StuckIdlePR)
	if got := add(prCheck{number: 12, done: true, noChecks: true}); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: idle for 15m, PR #12 has no CI checks" {
		t.Errorf("next stretch = %q", got)
	}
	if got := add(prCheck{done: true}); got != "" {
		t.Errorf("merged or failed check: %q", got)
	}
}

// A worker's context use at ContextFull or over makes one note, again only after it drops back.
func TestObserveContext(t *testing.T) {
	o := newObserver(t)
	w := worker("working", "", o.ago(0))
	w.Context = "79"
	if got, _ := o.round(0, w); got != "" {
		t.Errorf("under ContextFull: %q", got)
	}
	w.Context = "85"
	if got, _ := o.round(0, w); got != "[dev boss inbox] 1 worker update(s):\n- w high context: 85% used" {
		t.Errorf("full = %q", got)
	}
	w.Context = "90"
	if got, _ := o.round(0, w); got != "" {
		t.Errorf("still full: %q", got)
	}
	w.Context = "20" // compacted, in the same turn: no prompt between
	o.round(0, w)
	w.Context = "85"
	if got, _ := o.round(time.Second, w); got != "[dev boss inbox] 1 worker update(s):\n- w high context: 85% used" {
		t.Errorf("full again = %q", got)
	}
}

// A decision handed to the manager tells the boss to leave it and the manager to answer the worker; Outside wins.
func TestNoteHanded(t *testing.T) {
	n := Note{Worker: "w", Kind: Decision, Text: "Bash"}
	for _, tt := range []struct {
		name string
		note func(Note) Note
		want string
	}{
		{"boss's", func(n Note) Note { n.Handed = true; return n }, "w needs a decision: Bash; " + leaveToManager},
		{"manager's", func(n Note) Note { n.Of = "boss-a"; return n },
			"w (boss-a's worker) needs a decision: Bash; ask the user, then send-keys only their pick to w yourself (its boss leaves it to you)"},
		{"outside", func(n Note) Note { n.Outside, n.Handed = true, true; return n }, "w needs a decision: Bash; " + leaveIt},
	} {
		if got := tt.note(n).String(); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Watch's notes on a worker a coordinator outside tmux started tell the boss to leave it, as its hook notes do.
func TestObserveOutside(t *testing.T) {
	o := newObserver(t)
	w := worker("working", "", o.ago(0))
	w.Context, w.Outside = "85", "1"
	want := "[dev boss inbox] 1 worker update(s):\n- w high context: 85% used; " + leaveIt
	if got, _ := o.round(0, w); got != want {
		t.Errorf("outside = %q, want %q", got, want)
	}
}

func TestCheckPR(t *testing.T) {
	old := ghPR
	t.Cleanup(func() { ghPR = old })
	pr := sidebar.PR{Number: 12, State: "open", URL: "https://github.com/o/r/pull/12"}
	repo := t.TempDir()
	dir := filepath.Join(repo, "sub")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".github", "workflows"), dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		out  string
		want prCheck
	}{
		{`{"state":"OPEN","mergeable":"CONFLICTING","statusCheckRollup":[]}`, prCheck{number: 12, done: true, conflict: true, noChecks: true}},
		{`{"state":"OPEN","mergeable":"MERGEABLE","statusCheckRollup":[{"name":"test"}]}`, prCheck{number: 12, done: true}},
		{`{"state":"OPEN","mergeable":"UNKNOWN","statusCheckRollup":[{"name":"test"}]}`, prCheck{number: 12}},
		{`{"state":"MERGED","mergeable":"UNKNOWN","statusCheckRollup":[]}`, prCheck{done: true}},
	}
	for _, tt := range tests {
		ghPR = func(url string) ([]byte, error) {
			if url != pr.URL {
				t.Errorf("gh asked for %q", url)
			}
			return []byte(tt.out), nil
		}
		if got, err := checkPR(pr, dir); err != nil || got != tt.want {
			t.Errorf("checkPR(%s) = %+v, %v; want %+v", tt.out, got, err, tt.want)
		}
	}
	// No workflows: no checks are expected
	if err := os.RemoveAll(filepath.Join(repo, ".github")); err != nil {
		t.Fatal(err)
	}
	ghPR = func(string) ([]byte, error) { return []byte(tests[0].out), nil }
	if got, err := checkPR(pr, dir); err != nil || got != (prCheck{number: 12, done: true, conflict: true}) {
		t.Errorf("checkPR without workflows = %+v, %v; want no noChecks", got, err)
	}
}

func TestTeammates(t *testing.T) {
	claude := func(session string, window int, id string) paneRow {
		return paneRow{Session: session, Window: window, Pane: id, Command: "claude", StateCmd: "claude", Sidebar: "%1"}
	}
	rows := []paneRow{
		{Session: "team", Window: 1, Pane: "%1", Command: "dev", Sidebar: "%1"}, // its sidebar
		claude("team", 1, "%2"), claude("team", 1, "%3"), claude("team", 1, "%4"),
		claude("solo", 1, "%5"),
		claude("elsewhere", 2, "%6"), // no agent pane in window 1: not taken for a teammate
	}
	got := teammates(rows)
	for _, pane := range []string{"%1", "%2", "%3", "%4", "%5", "%6"} {
		if want := pane == "%1" || pane == "%3" || pane == "%4"; got[pane] != want {
			t.Errorf("teammates[%s] = %v, want %v", pane, got[pane], want)
		}
	}
}

// An answer's line has no worker; the same answer twice is queued twice, and the batch says it is the user's.
func TestAddAnswer(t *testing.T) {
	b := testBox(t)
	n := Answer(4, "Merge #148?", "Merge")
	for range 2 {
		if added, err := b.Add(n, time.Time{}); !added || err != nil {
			t.Fatalf("Add = %v, %v", added, err)
		}
	}
	bt, _ := b.Take()
	want := "[dev boss inbox] 2 worker update(s):\n- you answered #4 \"Merge #148?\": Merge\n- you answered #4 \"Merge #148?\": Merge\n" + answeredHint
	if bt.String() != want {
		t.Errorf("batch = %q, want %q", bt, want)
	}
}

// A start note says what to do by whose the item is: the user's, the boss's own parked one, a gone or running worker's; a closed
// boss's, whoever's, asks the manager to reopen it.
func TestStarted(t *testing.T) {
	gone := &todo.Session{ID: "$5", Name: "fix-login"}
	for _, tt := range []struct {
		it     todo.Shown
		closed bool
		want   string
	}{
		{todo.Shown{ID: 4, Text: "a"}, false, `#4 "a": while it still reads so and is marked started on your list, start a worker on it and link it with dev todo add --replaces 4 --on <worker>; if it is the user's own to do, say so on the list (dev todo add --replaces 4 --on me "<text, why>")`},
		{todo.Shown{ID: 4, Text: "a", On: todo.Boss}, false, `#4 "a": while it still reads so and is marked started on your list, it is your own parked item: do it yourself (dev todo doing 4 takes the start), or start a worker on it and link it with dev todo add --replaces 4 --on <worker>`},
		{todo.Shown{ID: 4, Text: "a", On: "fix-login", Session: gone, Status: todo.StatusGone}, false, `#4 "a": while it still reads so and is marked started on your list, its worker fix-login is gone: start a fresh one on it and link it with dev todo add --replaces 4 --on <worker>`},
		{todo.Shown{ID: 4, Text: "a", On: "fix-login", Session: gone}, false, `#4 "a": while it still reads so and is marked started on your list, it is on fix-login: see what it does, or start a fresh worker on it and link it with dev todo add --replaces 4 --on <worker>`},
		{todo.Shown{ID: 4, Text: "a", On: "fix-login", Session: gone}, true, `boss-x #4 "a": boss-x is closed: reopen it (dev boss x --background), its start list asks it to start #4`},
		{todo.Shown{ID: 4, Text: "a", Details: "x"}, false, `#4 "a": while it still reads so and is marked started on your list, start a worker on it and link it with dev todo add --replaces 4 --on <worker>; if it is the user's own to do, say so on the list (dev todo add --replaces 4 --on me "<text, why>"); it has details: read them in dev todo --json (item 4's details), and put them in a worker's brief`},
		{todo.Shown{ID: 4, Text: "a", Details: "x"}, true, `boss-x #4 "a": boss-x is closed: reopen it (dev boss x --background), its start list asks it to start #4`},
	} {
		if n := Started("boss-x", tt.it, tt.closed); n.Text != tt.want || n.Kind != Start || n.Key != "start boss-x 4" {
			t.Errorf("Started(%+v, %v) = %+v, want text %q", tt.it, tt.closed, n, tt.want)
		}
	}
}

// A full inbox drops its oldest notes, never an answer nor a start: its item is ticked already, or waits on it.
func TestAddCapsKeepsAnswers(t *testing.T) {
	b := testBox(t)
	_, _ = b.Add(Answer(1, "a?", "Yes"), time.Time{})
	_, _ = b.Add(Started("boss-x", todo.Shown{ID: 2, Text: "b"}, false), time.Time{})
	for i := range maxNotes + 3 {
		_, _ = b.Add(note("w", strings.Repeat("x", i+1)), time.Time{})
	}
	bt, _ := b.Take()
	if len(bt.Notes) != maxNotes || bt.Dropped != 5 || bt.Notes[0].Kind != Answered || bt.Notes[1].Kind != Start {
		t.Errorf("got %d notes, %d dropped, first %+v, %+v; want the answer and the start kept first", len(bt.Notes), bt.Dropped, bt.Notes[0], bt.Notes[1])
	}
}

// A worker that is itself a boss sends its boss its unanswered question's repeat and stuck notes, not high context;
// its card's note still shows it.
func TestObserveBoss(t *testing.T) {
	o := newObserver(t)
	o.s.health("w").Question, o.s.health("w").AskedAt = "Merge it?", o.now.Unix()
	w := worker("idle", "", o.ago(0))
	w.Context, w.Herd = "85", "1"
	got, holding := o.round(AskAgain, w)
	if got != "[dev boss inbox] 1 worker update(s):\n- w asks: Merge it? (unanswered for 30m)" {
		t.Errorf("queued = %q", got)
	}
	if !slices.Contains(holding, "w high context: 85% used") {
		t.Errorf("holding = %q, want the high context note", holding)
	}
	w = worker("working", "", o.ago(StuckWorking))
	w.Herd = "1"
	if got, _ := o.round(0, w); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 15m" {
		t.Errorf("stuck = %q", got)
	}
}

func TestUp(t *testing.T) {
	for kind, want := range map[string]bool{WaitsOnYou: true, Asks: true, Decision: true, Failed: true, Stuck: true,
		Finished: false, Context: false, Resumed: false} {
		if up(kind) != want {
			t.Errorf("up(%q) = %v", kind, !want)
		}
	}
}

// A planned wait mutes the stuck note of a worker parked on background tasks and the repeat of its question, not
// the stuck note of one still working nor a decision; once the wait ends while it works, its stuck clock starts there.
func TestObserveWaiting(t *testing.T) {
	o := newObserver(t)
	parked := worker("working", "", o.ago(5*time.Hour))
	parked.screen, parked.parked, parked.waiting = "s1", true, true
	o.round(0, parked)
	if got, _ := o.round(StuckWorking, parked); got != "" {
		t.Errorf("parked in a planned wait: %q", got)
	}
	busy := parked
	busy.parked = false
	if got, _ := o.round(time.Minute, busy); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 16m" {
		t.Errorf("working through a planned wait = %q, want stuck", got)
	}

	o = newObserver(t)
	parked.Since = o.ago(5 * time.Hour)
	o.round(0, parked)
	ended := parked
	ended.waiting, ended.waitEnd = false, o.now.Unix()
	if got, _ := o.round(time.Minute, ended); got != "" {
		t.Errorf("right after its wait ended: %q", got)
	}
	after := ended
	after.waitEnd = 0
	if got, _ := o.round(StuckWorking-time.Minute, after); got != "[dev boss inbox] 1 worker update(s):\n- w stuck: no progress for 15m, waiting on its background tasks" {
		t.Errorf("15m after its wait ended = %q", got)
	}

	o = newObserver(t)
	o.s.health("w").Question, o.s.health("w").AskedAt = "Merge it?", o.now.Unix()
	idle := worker("idle", "", o.ago(0))
	idle.waiting = true
	if got, _ := o.round(AskAgain, idle); got != "" {
		t.Errorf("asks again in a planned wait: %q", got)
	}
	deciding := worker("waiting", "permission_prompt", o.ago(StuckDecision))
	deciding.waiting = true
	if got, _ := o.round(0, deciding); !strings.Contains(got, "w stuck: waiting on a decision for 3m, unanswered") {
		t.Errorf("a decision in a planned wait = %q, want it reported", got)
	}
}

// Real turn ends (boss-nocworx-core, 2026-11-18): an instruction to reply naming its options, and list items asking
// questions with a recommendation under a lead-in.
const (
	replyTurn = "**Step 2 of 4: connect the VPN.** I just checked and git.liquidweb.com still times out. Once you're connected, the pr-care worker (still open) checks the lw merge requests, including whether !2408 (CLOUD-539) has a reviewer yet.\n\n" +
		"Reply **Done** once it's connected, or **Skip** to leave the lw check for later."
	questionsTurn = "## To do\n1. ✅ Triage permission prompt\n2. ✅ lw merge request check\n" +
		"3. 👉 #11691: what to do with Priyanka's 2 non-blocking threads (Step 3 above)\n4. ⏳ !2408: add Carl as reviewer\n" +
		"5. ⏳ Send your Slack drafts: Vince (COM-120), Martin (CLOUD-360), David handover DM (#23)\n\n" +
		"Two questions:\n" +
		"- **#11691 threads:** should I post the two \"follow-up\" replies and file an unassigned COM card? I recommend **Yes**.\n" +
		"- **!2408:** should the pr-care worker add Carl as reviewer? I recommend **Yes**. He already plans to review it, and the reviewer notification is the only message he'd get."
)

// A turn ending on an instruction to reply with named options, or on list items that are questions, asks; a reply
// line naming no option and a courtesy closing don't.
func TestFromHookTurnEndAsks(t *testing.T) {
	tests := []struct {
		name, last, want string
		options          []string
	}{
		{"reply with bold options", replyTurn, "w asks: Reply **Done** once it's connected, or **Skip** to leave the lw check for later.", []string{"Done", "Skip"}},
		{"reply with slash options", "Merged.\nReply Done / Skip.", "w asks: Reply Done / Skip.", []string{"Done", "Skip"}},
		{"answer with bold options", "Answer **yes** or **no**.", "w asks: Answer **yes** or **no**.", []string{"yes", "no"}},
		{"answer either", "Answer either **A**, **B** or **C**.", "w asks: Answer either **A**, **B** or **C**.", []string{"A", "B", "C"}},
		{"hyphenated slash options", "Reply go/no-go.", "w asks: Reply go/no-go.", []string{"go", "no-go"}},
		{"options stop with the sentence", "Reply **Done** when it's up. I'll rerun **foo** then.", "w asks: Reply **Done** when it's up. I'll rerun **foo** then.", []string{"Done"}},
		{"reply after a sentence", "The VPN is down. Reply **Done** when it's up.", "w asks: The VPN is down. Reply **Done** when it's up.", []string{"Done"}},
		{"questions with recommendations", questionsTurn,
			"w asks: Two questions: - **#11691 threads:** should I post the two \"follow-up\" replies and file an unassigned COM card? I recommend **Yes**. - **!2408:** should the pr-care worker add Carl as reviewer? I rec…", nil},
		{"questions without a lead-in", "Done.\n- Post it? I recommend **Yes**.\n- Ping Carl? I'd suggest **No**.",
			"w asks: - Post it? I recommend **Yes**. - Ping Carl? I'd suggest **No**.", nil},
		{"questions under a question", "Which one?\n- Post it? I recommend **Yes**.\n- Ping? I recommend **No**.",
			"w asks: Which one? - Post it? I recommend **Yes**. - Ping? I recommend **No**.", nil},
		{"long questions keep the first", "Two questions:\n- " + strings.Repeat("word ", 25) + "post it? I recommend **Yes**.\n- " + strings.Repeat("word ", 25) + "ping? I recommend **No**.",
			"w asks: Two questions: - " + strings.Repeat("word ", 25) + "post it? I recommend **Yes**. - " + strings.Repeat("word ", 4) + "word …", nil},
		{"questions past the lines read", "Six questions:\n- A? I recommend **Yes**.\n- B? I recommend **Yes**.\n- C? I recommend **Yes**.\n- D? I recommend **Yes**.\n- E? I recommend **Yes**.\n- F? I recommend **Yes**.",
			"w asks: - B? I recommend **Yes**. - C? I recommend **Yes**. - D? I recommend **Yes**. - E? I recommend **Yes**. - F? I recommend **Yes**.", nil},
		{"options under a question asking more", "Which one?\n- A: cache it? I recommend **this**.\n- B: drop it", "w asks: Which one? - A: cache it? I recommend **this**. - B: drop it",
			[]string{"A: cache it? I recommend this.", "B: drop it"}},
		{"a quoted question label", "Your question: did the wait-until call succeed?\n- The call succeeded.\n- Nothing woke me.", "w finished: Your question: did the wait-until call succeed? - The call succeeded. - Nothing woke me.", nil},
		{"a bold quoted question label", "**Your question:** did it work?\n- Yes.", "w finished: **Your question:** did it work? - Yes.", nil},
		{"a question label", "Question: is it done?\n1. Yes", "w finished: Question: is it done? 1. Yes", nil},
		{"a q label", "q: is it done?\n- yes", "w finished: q: is it done? - yes", nil},
		{"a you asked label", "You asked: is it done?\n- Yes.", "w finished: You asked: is it done? - Yes.", nil},
		{"a blockquote question", "> is it done?\n- Yes.", "w finished: > is it done? - Yes.", nil},
		{"questions after a quote", "> is it done?\nTwo questions:\n- Deploy now? I recommend **Yes**.\n- Ping? I recommend **No**.", "w asks: Two questions: - Deploy now? I recommend **Yes**. - Ping? I recommend **No**.", nil},
		{"questions right under a quote", "> is it done?\n- Deploy now? I recommend **Yes**.", "w asks: - Deploy now? I recommend **Yes**.", nil},
		{"a closing question with options", "Which variants should I build: A1 + B1 + C1, and D1 too?\n- A1\n- B1", "w asks: Which variants should I build: A1 + B1 + C1, and D1 too? - A1 - B1", []string{"A1", "B1"}},
		{"a closing question with numbered options", "Merge now?\n1. Yes\n2. No", "w asks: Merge now? 1. Yes 2. No", []string{"Yes", "No"}},
		{"a reply sent", "Reply sent to Vince.", "w finished: Reply sent to Vince.", nil},
		{"a bold name after a reply verb", "Reply sent to **Vince**.", "w finished: Reply sent to **Vince**.", nil},
		{"a courtesy closing", "Merged and released v1.2.\nLet me know if anything breaks.", "w finished: Merged and released v1.2. Let me know if anything breaks.", nil},
		{"a reply instruction above the end", "Reply **Done** when connected.\nI'll wait.", "w finished: Reply **Done** when connected. I'll wait.", nil},
		{"a summary list", "Summary:\n- a\n- b", "w finished: Summary: - a - b", nil},
		{"a command to type", "Added the alias.\nType `dev status` to see it.", "w finished: Added the alias. Type `dev status` to see it.", nil},
		{"a check to confirm", "Released.\nConfirm `go test ./...` passes before merging.", "w finished: Released. Confirm `go test ./...` passes before merging.", nil},
		{"a slash after let me know", "Merged.\nLet me know if/when it lands.", "w finished: Merged. Let me know if/when it lands.", nil},
		{"an answer with a slash", "Answer: CI/CD pipelines are green.", "w finished: Answer: CI/CD pipelines are green.", nil},
		{"pick up with a slash", "Pushed.\nPick up/drop off logic fixed.", "w finished: Pushed. Pick up/drop off logic fixed.", nil},
		{"a how-to with two bold options", "Choose **Settings** or **Preferences** to find the toggle.", "w finished: Choose **Settings** or **Preferences** to find the toggle.", nil},
		{"slash words in a reply's sentence", "Reply with TCP/IP settings when you have them.", "w finished: Reply with TCP/IP settings when you have them.", nil},
		{"a recommendation without a bold answer", "Notes:\n- Was it slow? Yes, so I recommend caching.\n- Retry? Added; I suggest 3.", "w finished: Notes: - Was it slow? Yes, so I recommend caching. - Retry? Added; I suggest 3.", nil},
		{"answered questions in a list", "Findings:\n- Why did it fail? The cache was stale.\n- Fixed? Yes.", "w finished: Findings: - Why did it fail? The cache was stale. - Fixed? Yes.", nil},
		{"some items aren't questions", "Two things:\n- Post it? I recommend **Yes**.\n- Done the rest.", "w finished: Two things: - Post it? I recommend **Yes**. - Done the rest.", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, _ := json.Marshal(map[string]string{"hook_event_name": "Stop", "last_assistant_message": tt.last})
			if n, _ := FromHook("w", in); n.String() != tt.want {
				t.Errorf("FromHook = %q\nwant        %q", n.String(), tt.want)
			}
			var lines []string
			for l := range strings.Lines(tt.last) {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, strings.TrimRight(l, "\n"))
				}
			}
			var opts []string
			if i := QuestionAt(lines); i >= 0 {
				opts = Options(lines[i:])
			}
			if !slices.Equal(opts, tt.options) {
				t.Errorf("Options = %q, want %q", opts, tt.options)
			}
		})
	}
}

// The user's doing note names the item, what they do, or that they stopped, the closed boss for the manager's; a newer
// one for the same item replaces the one still queued, others queue beside it, and a full inbox keeps it.
func TestAddUserDoing(t *testing.T) {
	for _, tt := range []struct {
		note Note
		want string
	}{
		{UserDoing("boss-a", 4, "Send the cert", "mailing ops", false, false), `the user is on #4 "Send the cert": mailing ops`},
		{UserDoing("boss-a", 4, "Send the cert", "", false, false), `the user is on #4 "Send the cert"`},
		{UserDoing("boss-a", 4, "Send the cert", "", true, false), `the user is on #4 "Send the cert": no longer on it`},
		{UserDoing("boss-a", 4, "Send the cert", "mailing ops", false, true), `the user is on boss-a #4 "Send the cert": mailing ops (boss-a is closed)`},
	} {
		if got := tt.note.String(); got != tt.want {
			t.Errorf("note = %q, want %q", got, tt.want)
		}
	}
	b := testBox(t)
	for _, n := range []Note{
		UserDoing("boss-a", 4, "a", "first", false, false),
		UserDoing("boss-a", 5, "b", "other item", false, false),
		UserDoing("boss-a", 4, "a", "second", false, false),
		UserDoing("boss-b", 4, "a", "closed one", false, true),
	} {
		if added, err := b.Add(n, time.Time{}); !added || err != nil {
			t.Fatalf("Add = %v, %v", added, err)
		}
	}
	for i := range maxNotes + 3 {
		_, _ = b.Add(note("w", strings.Repeat("x", i+1)), time.Time{})
	}
	bt, _ := b.Take()
	if len(bt.Notes) != maxNotes || bt.Notes[0].Text != `#4 "a": second` || bt.Notes[1].Text != `#5 "b": other item` || bt.Notes[2].Text != `boss-b #4 "a": closed one (boss-b is closed)` {
		t.Errorf("got %d notes, first %+v, %+v, %+v; want #4's second line in its place, #5 and boss-b's #4 beside it, all kept", len(bt.Notes), bt.Notes[0], bt.Notes[1], bt.Notes[2])
	}
}

// Doing notes past the cap go too, the oldest first, once no other droppable note is left.
func TestAddCapsDoing(t *testing.T) {
	b := testBox(t)
	_, _ = b.Add(Answer(1, "a?", "Yes"), time.Time{})
	for i := range maxNotes + 3 {
		_, _ = b.Add(UserDoing("boss-a", i+2, "x", "", false, false), time.Time{})
	}
	bt, _ := b.Take()
	if len(bt.Notes) != maxNotes || bt.Dropped != 4 || bt.Notes[0].Kind != Answered || bt.Notes[1].Key != "doing boss-a 6" {
		t.Errorf("got %d notes, %d dropped, first %+v, %+v; want %d, the answer kept, doing #2-#5 dropped", len(bt.Notes), bt.Dropped, bt.Notes[0], bt.Notes[1], maxNotes)
	}
}

// A queued doing note goes once its item is ticked or gone, or another item without doing took its number; a reworded
// item still marked doing keeps it, as does a stop on an open item; a failed lookup keeps it.
func TestWithdrawTicked(t *testing.T) {
	b := testBox(t)
	for _, n := range []Note{
		UserDoing("boss-a", 1, "old text", "on it", false, false),
		UserDoing("boss-a", 2, "ticked", "", false, false),
		UserDoing("boss-a", 3, "same title", "", false, false),
		UserDoing("boss-a", 4, "stopped", "", true, false),
		UserDoing("boss-b", 1, "unread", "", true, true),
		Answer(2, "q?", "Yes"),
	} {
		_, _ = b.Add(n, time.Time{})
	}
	type item struct{ doing bool }
	lists := map[string]map[int]item{"boss-a": {1: {doing: true}, 3: {}, 4: {}}}
	err := b.WithdrawTicked(func(boss string, id int) (bool, bool, error) {
		if boss == "boss-b" {
			return false, false, errors.New("list unread")
		}
		it, ok := lists[boss][id]
		return ok, it.doing, nil
	})
	if err == nil || !strings.Contains(err.Error(), "list unread") {
		t.Errorf("err = %v, want the failed lookup", err)
	}
	bt, _ := b.Take()
	var keys []string
	for _, n := range bt.Notes {
		keys = append(keys, n.Key)
	}
	if want := []string{"doing boss-a 1", "doing boss-a 4", "doing boss-b 1", "answer 2"}; !slices.Equal(keys, want) {
		t.Errorf("left %q, want %q", keys, want)
	}
}

// A batch put back after a failed send drops its doing note when a newer one for that item was queued meanwhile.
func TestRestoreKeepsNewerDoing(t *testing.T) {
	b := testBox(t)
	_, _ = b.Add(UserDoing("boss-a", 4, "a", "first", false, false), time.Time{})
	_, _ = b.Add(Answer(1, "q?", "Yes"), time.Time{})
	bt, _ := b.Take()
	_, _ = b.Add(UserDoing("boss-a", 4, "a", "", true, false), time.Time{})
	if err := b.Restore(bt); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Take()
	if len(got.Notes) != 2 || got.Notes[0].Kind != Answered || got.Notes[1].Text != `#4 "a": no longer on it` {
		t.Errorf("after restore: %+v, want the answer, then only the stop", got.Notes)
	}
}
