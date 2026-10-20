package inbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
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
		if got := span(d); got != want {
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
	if len(bt.Notes) != maxNotes || bt.Dropped != 3 || !strings.HasSuffix(bt.String(), "(3 older update(s) dropped: run dev agent list --mine)") {
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
		if n, ok := o.s.observePR(o.now, "w", since, pr); ok != (n.Worker != "") {
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
