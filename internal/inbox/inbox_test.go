package inbox

import (
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestStuckNote(t *testing.T) {
	if got := StuckNote("w", "working", 25*time.Minute+30*time.Second, "1").String(); got != "w stuck: working for 25m" {
		t.Errorf("working: %q", got)
	}
	if got := StuckNote("w", "waiting", 4*time.Minute, "1").String(); got != "w stuck: waiting on a decision for 4m, unanswered" {
		t.Errorf("waiting: %q", got)
	}
	if a, b := StuckNote("w", "working", 21*time.Minute, "1"), StuckNote("w", "working", 40*time.Minute, "1"); a.Key != b.Key {
		t.Error("one stretch gives two keys")
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
		if _, err := b.Add(n); err != nil {
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
	if added, _ := b.Add(note("a", "y")); added {
		t.Error("repeat after take was queued")
	}
}

// A new prompt (Forget) lets a stretch's stuck note repeat only while the worker's screen hasn't moved on.
func TestAddStuck(t *testing.T) {
	b := testBox(t)
	add := func(n Note, screen string) bool {
		t.Helper()
		added, err := b.AddStuck(n, screen)
		if err != nil {
			t.Fatal(err)
		}
		return added
	}
	at20, at25 := StuckNote("w", "working", 20*time.Minute, "1"), StuckNote("w", "working", 25*time.Minute, "1")
	if !add(at20, "tests running") {
		t.Fatal("first stuck note not queued")
	}
	if add(at25, "tests running") {
		t.Error("repeat without a new prompt")
	}
	if err := b.Forget("w"); err != nil {
		t.Fatal(err)
	}
	if add(at25, "tests passed") {
		t.Error("repeat after the screen moved on")
	}
	if add(at25, "tests passed") {
		t.Error("a moved-on screen became the baseline")
	}
	if !add(at25, "tests running") {
		t.Error("no repeat for an unchanged screen")
	}
	if err := b.Forget("w"); err != nil {
		t.Fatal(err)
	}
	if !add(StuckNote("w", "working", 20*time.Minute, "2"), "other") {
		t.Error("a new stretch's first note not queued")
	}
	if !add(StuckNote("v", "working", 20*time.Minute, "1"), "tests passed") {
		t.Error("another worker's note not queued")
	}
	bt, err := b.Take()
	if err != nil {
		t.Fatal(err)
	}
	want := "[dev boss inbox] 4 worker update(s):\n- w stuck: working for 20m\n- w stuck: working for 25m\n- w stuck: working for 20m\n- v stuck: working for 20m"
	if bt.String() != want {
		t.Errorf("batch = %q, want %q", bt, want)
	}
}

func TestAddCaps(t *testing.T) {
	b := testBox(t)
	for i := range maxNotes + 3 {
		_, _ = b.Add(note("w", strings.Repeat("x", i+1)))
	}
	bt, _ := b.Take()
	if len(bt.Notes) != maxNotes || bt.Dropped != 3 || !strings.HasSuffix(bt.String(), "(3 older update(s) dropped: run dev agent list --mine)") {
		t.Errorf("got %d notes, %d dropped", len(bt.Notes), bt.Dropped)
	}
}

func TestTakeForPasteRateLimits(t *testing.T) {
	b := testBox(t)
	now := time.Unix(1000, 0)
	_, _ = b.Add(note("a", "1"))
	if bt, _ := b.TakeForPaste(now); len(bt.Notes) != 1 {
		t.Fatalf("first paste took %d", len(bt.Notes))
	}
	_, _ = b.Add(note("a", "2"))
	if bt, _ := b.TakeForPaste(now.Add(pasteGap - time.Second)); len(bt.Notes) != 0 {
		t.Error("paste inside the gap")
	}
	bt, _ := b.TakeForPaste(now.Add(pasteGap))
	if len(bt.Notes) != 1 {
		t.Fatal("no paste after the gap")
	}
	_, _ = b.Add(note("b", "3"))
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

func TestStuck(t *testing.T) {
	now := time.Unix(10_000, 0)
	ago := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) }
	row := func(state, waitingFor, since string) paneRow {
		return paneRow{Session: "w", Command: "claude", State: state, StateCmd: "claude", WaitingFor: waitingFor, Since: since}
	}
	tests := []struct {
		name string
		r    paneRow
		want string
	}{
		{"working long", row("working", "", ago(StuckWorking)), "w stuck: working for 20m"},
		{"working short", row("working", "", ago(StuckWorking-time.Second)), ""},
		{"decision unanswered", row("waiting", "permission_prompt", ago(StuckDecision)), "w stuck: waiting on a decision for 3m, unanswered"},
		{"idle reminder isn't a decision", row("waiting", "idle_prompt", ago(time.Hour)), ""},
		{"idle", row("idle", "", ago(time.Hour)), ""},
		{"untrusted state", paneRow{Session: "w", Command: "zsh", State: "working", StateCmd: "claude", Since: ago(time.Hour)}, ""},
		{"no since", row("working", "", ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := tt.r.stuck(now)
			if got := map[bool]string{true: n.String()}[ok]; got != tt.want {
				t.Errorf("stuck = %q, want %q", got, tt.want)
			}
		})
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
