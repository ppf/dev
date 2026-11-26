package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// bossTmux is a tmux server with one dev boss session, "boss", whose coordinator is %1; todoHerd is the
// @dev_herd its to do list reads ("" makes the list unreadable).
func bossTmux(t *testing.T, todoHerd string) *tmux.Client {
	t.Helper()
	return namedTmux(t, "boss", todoHerd)
}

// namedTmux is bossTmux with the boss session called name.
func namedTmux(t *testing.T, name, todoHerd string) *tmux.Client {
	t.Helper()
	return handoffTmux(t, name, todoHerd, "")
}

// handoffTmux is namedTmux with handoff in the coordinator pane's OptHandoff.
func handoffTmux(t *testing.T, name, todoHerd, handoff string) *tmux.Client {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow(name, "1", "%1", "/p/boss", "t", "claude", "idle", "claude", "", "", "", "$1", "", "1", "", "", "", "", "", "", "", "", "", "", ""), nil
		case "display-message":
			switch args[len(args)-1] {
			case "#{@dev_boss_pane}":
				return map[string]string{"%1": "coordinator\n", "%2": "previews\n"}[args[3]], nil
			case "#{" + OptHandoff + "}":
				return handoff + "\n", nil
			case "#{" + agent.OptEndedSession + "}":
				return "s0\n", nil
			}
			if strings.Contains(args[len(args)-1], "@dev_boss_inbox") {
				return tmux.FormatRow("$1", "1700000000", name, "1", "", "/tmp/tmux-1/default"), nil
			}
			return tmux.FormatRow("/tmp/tmux-1/default", name, todoHerd, "$1", "1700000000"), nil
		}
		return "", nil
	})
}

// who is the line the reminders end with, asking who does each item.
const who = "\nWho does an item, when not the user: dev todo add --replaces <n> --on <worker name> (a session), --on boss (your own or parked), --waiting-for <person>, --on me (back to the user, with --details when it has none)."

// nodet is the line the reminders add when an item has no details.
// addedStamp is the real time the start list gives each item.
var addedStamp = regexp.MustCompile(` \(added \d{4}-\d\d-\d\d \d\d:\d\d\)`)

const nodet = "\nAn item marked (no details) gets them: dev todo add --replaces <n> --details '<what it is and why, where it stands, what done looks like>' (an ask: dev todo ask --replaces <n> --details '…')."

// The boss's open to do list comes with the worker notes it hands over: blocking a Stop, as a prompt's context,
// and alone with a prompt that is a batch Deliver pasted; never without notes, nor with an empty list.
func TestBossHookTodo(t *testing.T) {
	note := Note{Worker: "w", Kind: Finished, Text: "w finished: PR #9 merged", Key: "k"}
	pasted := Batch{Notes: []Note{note}}.String()
	for _, tt := range []struct {
		name, input string
		notes       bool
		list        []string
		want        string // the reason or context; "" for no output
		busy        bool
	}{
		{"stop with notes", `{"hook_event_name":"Stop"}`, true, []string{"Merge PR #9", "Answer Vince"}, pasted + "\n\n[dev todo] Your open to do list:\n1. Merge PR #9 (no details)\n2. Answer Vince (no details)\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who + nodet, true},
		{"prompt with notes", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, true, []string{"Merge PR #9"}, pasted + "\n\n[dev todo] Your open to do list:\n1. Merge PR #9 (no details)\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who + nodet, false},
		{"notes, empty list", `{"hook_event_name":"Stop"}`, true, nil, pasted, true},
		{"pasted batch", `{"hook_event_name":"UserPromptSubmit","prompt":` + quote(pasted) + `}`, false, []string{"Merge PR #9"}, "[dev todo] Your open to do list:\n1. Merge PR #9 (no details)\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who + nodet, false},
		{"pasted batch, empty list", `{"hook_event_name":"UserPromptSubmit","prompt":` + quote(pasted) + `}`, false, nil, "", false},
		{"plain prompt", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, false, []string{"Merge PR #9"}, "", false},
		{"stop without notes", `{"hook_event_name":"Stop"}`, false, []string{"Merge PR #9"}, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := bossTmux(t, "1")
			b, ok, err := Self(c, "%1")
			if !ok || err != nil {
				t.Fatalf("Self = %v, %v", ok, err)
			}
			if tt.notes {
				if _, err := b.Box.Add(note, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if tt.list != nil {
				s, err := todo.ForPane(c, "%1")
				if err == nil {
					err = s.Set(tt.list, time.Now())
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			out, busy, err := BossHook(c, "%1", []byte(tt.input), noHint)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Reason string `json:"reason"`
				Out    struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if out != "" {
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("out %q: %v", out, err)
				}
			}
			if text := got.Reason + got.Out.Context; text != tt.want || busy != tt.busy {
				t.Errorf("BossHook = %q, busy %v; want %q, busy %v", text, busy, tt.want, tt.busy)
			}
		})
	}
}

// The manager coordinator's AskUserQuestion pick is kept as the user's latest prompt; a dismissed one, another
// pane's, another boss's or another tool's isn't.
func TestBossHookPick(t *testing.T) {
	const pick = `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"boss-a #1: Merge #148?"}],"answers":{"boss-a #1: Merge #148?":"Merge"}}}`
	for _, tt := range []struct {
		name, session, pane, input, want string
	}{
		{"pick", Manager, "%1", pick, "boss-a #1: Merge #148? picked Merge"},
		{"another boss", "boss", "%1", pick, ""},
		{"previews pane", Manager, "%2", pick, ""},
		{"dismissed", Manager, "%1", `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Q?"}]}}`, ""},
		{"other tool", Manager, "%1", `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := namedTmux(t, tt.session, "")
			if out, busy, err := BossHook(c, tt.pane, []byte(tt.input), noHint); out != "" || busy || err != nil {
				t.Fatalf("BossHook = %q, %v, %v", out, busy, err)
			}
			b, _, err := Self(c, "%1")
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.Box.UserPrompt()
			switch {
			case err != nil:
				t.Fatal(err)
			case tt.want == "" && p != nil:
				t.Errorf("kept %+v, want none", p)
			case tt.want != "" && (p == nil || p.Text != tt.want || !p.Pick):
				t.Errorf("kept %+v, want pick %q", p, tt.want)
			}
		})
	}
}

// Notes already taken from the inbox still reach the boss when its list can't be read.
func TestBossHookListError(t *testing.T) {
	c := bossTmux(t, "")
	b, _, err := Self(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Box.Add(Note{Worker: "w", Kind: Finished, Text: "w finished", Key: "k"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, busy, err := BossHook(c, "%1", []byte(`{"hook_event_name":"Stop"}`), noHint)
	if !errors.Is(err, todo.ErrNotBoss) || !busy || !strings.Contains(out, `"reason":"[dev boss inbox] 1 worker update(s):\n- w finished: w finished"}`) {
		t.Errorf("BossHook = %q, busy %v, %v; want the notes, busy, and the list's error", out, busy, err)
	}
}

// deadPID is a pid no process has: a dev agent clear killed before it unset OptHandoff.
const deadPID = "99999999"

// soon is a time a running dev agent clear holds until.
var soon = time.Now().Add(time.Hour)

// A boss /clear'ed by a running dev agent clear (OptHandoff set for the conversation that ended, s0) gets its
// handoff's path after its list, as does one compacted while it runs; a start or resume doesn't, nor a clear without
// one, a leftover of a killed run or one set for another conversation.
func TestBossHookHandoff(t *testing.T) {
	const path = "/s/dev/handoff/boss/2026-11-24 120000.md"
	line := "Your handoff from before this /clear: " + path + ". Read it before acting; dev agent clear asks you for a proof of it next."
	running := ClearValue("s0", path, soon)
	for _, tt := range []struct {
		source, handoff string
		want            bool
	}{
		{"clear", running, true},
		{"clear", "", false},
		{"clear", deadPID + " " + strconv.FormatInt(soon.Unix(), 10) + " s0 " + path, false},
		{"clear", ClearValue("s0", path, time.Now().Add(-time.Minute)), false},
		{"clear", ClearValue("s7", path, soon), false},
		{"startup", running, false},
		{"resume", running, false},
		{"compact", running, true},
		{"compact", "", false},
	} {
		c := handoffTmux(t, "boss", "1", tt.handoff)
		s, err := todo.ForPane(c, "%1")
		if err == nil {
			err = s.Set([]string{"a"}, time.Now())
		}
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"`+tt.source+`"}`), noHint)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Out struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if out != "" {
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("out %q: %v", out, err)
			}
		}
		if has := strings.HasSuffix(got.Out.Context, "\n"+line); has != tt.want {
			t.Errorf("%s with handoff %q: context = %q, want the handoff line %v", tt.source, tt.handoff, got.Out.Context, tt.want)
		}
	}
	// A malformed hold is reported, and the list still goes out with a line saying a handoff may wait
	c := handoffTmux(t, "boss", "1", path)
	if s, err := todo.ForPane(c, "%1"); err != nil || s.Set([]string{"a"}, time.Now()) != nil {
		t.Fatal(err)
	}
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"clear"}`), noHint)
	if err == nil {
		t.Error("a malformed handoff option passed")
	}
	if !strings.Contains(out, "couldn't tell whether a dev agent clear left you a handoff") || !strings.Contains(out, "1. a") {
		t.Errorf("malformed hold: out %q, want the list and the handoff warning", out)
	}
}

// While a dev agent clear runs, a boss's Stop or prompt hands over no note: they stay in its inbox; a leftover of a
// killed run holds nothing.
func TestBossHookClearHold(t *testing.T) {
	note := Note{Worker: "w", Kind: Finished, Text: "w finished", Key: "k"}
	for _, tt := range []struct {
		handoff string
		held    bool
	}{
		{ClearValue("s0", "/h.md", soon), true},
		{deadPID + " " + strconv.FormatInt(soon.Unix(), 10) + " s0 /h.md", false},
		{ClearValue("s0", "/h.md", time.Now().Add(-time.Minute)), false},
	} {
		c := handoffTmux(t, "boss", "1", tt.handoff)
		b, ok, err := Self(c, "%1")
		if !ok || err != nil {
			t.Fatalf("Self = %v, %v", ok, err)
		}
		if _, err := b.Box.Add(note, time.Now()); err != nil {
			t.Fatal(err)
		}
		out, busy, err := BossHook(c, "%1", []byte(`{"hook_event_name":"Stop"}`), noHint)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := b.Box.Pending()
		if err != nil {
			t.Fatal(err)
		}
		if gave := out != "" || busy; gave == tt.held || pending != tt.held {
			t.Errorf("handoff %q: out %q, busy %v, pending %v; want held %v", tt.handoff, out, busy, pending, tt.held)
		}
	}
}

// A coordinator starting, resumed or /clear'ed gets its open list, kept from an earlier session on a start;
// a compaction too; nothing with no open item, or for another pane in the boss session.
func TestBossHookStart(t *testing.T) {
	check := "\nCheck each still holds: tick what's done (dev todo done <n>), update what changed (dev todo add --replaces <n> \"<new text>\")." + who + nodet
	for _, tt := range []struct {
		name, source, pane string
		list               []string
		want               string
	}{
		{"startup", "startup", "%1", []string{"Mon: reply to Vince"}, "[dev todo] Your open to do list (kept from your last boss session, updated just now):\n1. Mon: reply to Vince (no details)" + check},
		{"clear", "clear", "%1", []string{"a", "b"}, "[dev todo] Your open to do list (updated just now):\n1. a (no details)\n2. b (no details)" + check},
		{"resume", "resume", "%1", []string{"a"}, "[dev todo] Your open to do list (updated just now):\n1. a (no details)" + check},
		{"compact", "compact", "%1", []string{"a"}, "[dev todo] Your open to do list (updated just now):\n1. a (no details)" + check},
		{"empty list", "startup", "%1", nil, ""},
		{"previews pane", "startup", "%2", []string{"a"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := bossTmux(t, "1")
			if tt.list != nil {
				s, err := todo.ForPane(c, "%1")
				if err == nil {
					err = s.Set(tt.list, time.Now())
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			out, busy, err := BossHook(c, tt.pane, []byte(`{"hook_event_name":"SessionStart","source":"`+tt.source+`"}`), noHint)
			if err != nil || busy {
				t.Fatalf("BossHook: busy %v, %v", busy, err)
			}
			var got struct {
				Out struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if out != "" {
				if err := json.Unmarshal([]byte(out), &got); err != nil || got.Out.Event != "SessionStart" {
					t.Fatalf("out %q: %v", out, err)
				}
			}
			if ctx := addedStamp.ReplaceAllString(got.Out.Context, ""); ctx != tt.want {
				t.Errorf("context = %q, want %q", ctx, tt.want)
			}
		})
	}
}

// The age is how long ago the list last changed.
func TestStartContextAge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := todo.Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	then := time.Unix(1700000000, 0)
	if err := s.Set([]string{"a"}, then); err != nil {
		t.Fatal(err)
	}
	out, err := StartContext(s, func() ([]todo.Session, error) { return nil, nil }, noHint, "", "startup", "c1", then.Add(49*time.Hour))
	if err != nil || !strings.Contains(out, "updated 49h ago):") {
		t.Errorf("StartContext = %q, %v; want updated 49h ago", out, err)
	}
}

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// A restored boss is marked one only after its coordinator starts: its SessionStart then gets nothing, quietly.
func TestBossHookStartBeforeMarked(t *testing.T) {
	c := bossTmux(t, "")
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"resume"}`), noHint)
	if out != "" || err != nil {
		t.Errorf("BossHook = %q, %v; want nothing", out, err)
	}
}

// A boss under a manager sends the manager the ask its turn ended on, an instruction to reply included, not its
// finished turns.
func TestWorkerHookBossAsksUp(t *testing.T) {
	for _, tt := range []struct {
		name, last, want string
	}{
		{"asks", replyTurn, "boss-x asks: Reply **Done** once it's connected, or **Skip** to leave the lw check for later."},
		{"finished", "Merged.", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			c := tmux.NewFake(func(args []string) (string, error) {
				if args[0] != "display-message" {
					return "", nil
				}
				if strings.Contains(args[len(args)-1], "@dev_started_by") {
					// The worker pane %5: boss-x, a boss (@dev_herd) the manager started
					return tmux.FormatRow("manager", "boss-x", "", "", "", "", "1"), nil
				}
				return tmux.FormatRow("$1", "1700000000", "manager", "1", "", "/tmp/tmux-1/default"), nil
			})
			in, _ := json.Marshal(map[string]string{"hook_event_name": "Stop", "last_assistant_message": tt.last})
			clk := control.Clock{Now: func() time.Time { return time.Unix(1700000100, 0) }, Sleep: func(time.Duration) {}}
			// The manager's coordinator isn't on the fake server: the note stays queued in its inbox
			_ = WorkerHook(c, clk, "%5", in)
			b, _, ok, err := BossOf(c, "%5")
			if !ok || err != nil {
				t.Fatalf("BossOf = %v, %v", ok, err)
			}
			bt, err := b.Box.Take()
			if err != nil {
				t.Fatal(err)
			}
			if got := bt.String(); tt.want == "" && got != "" || tt.want != "" && !strings.HasSuffix(got, "\n- "+tt.want) {
				t.Errorf("manager's inbox = %q, want %q", got, tt.want)
			}
		})
	}
}

// A start, /clear or compaction tells an --until tick once; workers that can't be read leave it.
func TestStartContextTicked(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := todo.Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	then := time.Unix(1700000000, 0)
	if _, err := s.Add(todo.Item{Text: "Sync", Until: then.Add(time.Hour).Unix()}, then); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(todo.Item{Text: "Rework", On: "fix", Session: &todo.Session{ID: "fix", Name: "fix"}}, then); err != nil {
		t.Fatal(err)
	}
	later := then.Add(2 * time.Hour)
	none := func() ([]todo.Session, error) { return nil, nil }
	if _, err := StartContext(s, func() ([]todo.Session, error) { return nil, errors.New("no workers") }, noHint, "", "clear", "c1", later); err == nil {
		t.Errorf("workers failing: no error")
	}
	tick := `#1 \"Sync\" ticked by itself: its --until time came`
	if out, err := StartContext(s, none, noHint, "", "compact", "c1", later); err != nil || !strings.Contains(out, tick) {
		t.Errorf("compact = %q, %v; want the tick", out, err)
	}
	if out, _ := StartContext(s, none, noHint, "", "clear", "c1", later); strings.Contains(out, "ticked by itself") {
		t.Errorf("again = %q, want the tick told once", out)
	}
}

// Notes follow the open list on every start, a compaction too, alone when nothing is open; a tick goes out once, the notes each time.
func TestStartContextNotes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := todo.Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	then := time.Unix(1700000000, 0)
	none := func() ([]todo.Session, error) { return nil, nil }
	if out, _ := StartContext(s, none, noHint, "", "startup", "c1", then); out != "" {
		t.Errorf("no list, no notes = %q, want nothing", out)
	}
	if _, err := s.AddNote(todo.Note{Kind: todo.NoteDecided, Text: "CSV only", Why: "parser"}, then); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"startup", "clear", "resume"} {
		out, err := StartContext(s, none, noHint, "", source, "c1", then.Add(8*24*time.Hour))
		if err != nil || !strings.Contains(out, "Your notes (1 of 12, oldest 8d)") || !strings.Contains(out, "CSV only (why: parser) (8d, still true?)") || strings.Contains(out, "open to do list") {
			t.Errorf("%s, notes only = %q, %v", source, out, err)
		}
	}
	if _, err := s.Add(todo.Item{Text: "Ship"}, then); err != nil {
		t.Fatal(err)
	}
	out, err := StartContext(s, none, noHint, "", "clear", "c1", then.Add(time.Hour))
	list, notes := strings.Index(out, "Your open to do list"), strings.Index(out, "Your notes")
	if err != nil || list < 0 || notes < list || strings.Contains(out, "still true?") {
		t.Errorf("list and notes = %q, %v; want the list, then the notes, no 7d mark", out, err)
	}
	if out, _ := StartContext(s, none, noHint, "", "compact", "c1", then.Add(time.Hour)); !strings.Contains(out, "Your open to do list") || !strings.Contains(out, "Your notes") {
		t.Errorf("compact = %q, want the list and notes", out)
	}
	if _, err := s.Add(todo.Item{Text: "Sync", Until: then.Add(time.Hour).Unix()}, then); err != nil {
		t.Fatal(err)
	}
	later := then.Add(2 * time.Hour)
	out, err = StartContext(s, none, noHint, "", "clear", "c1", later)
	tick, notes := strings.Index(out, "ticked by itself"), strings.Index(out, "Your notes")
	if err != nil || tick < 0 || notes < tick {
		t.Errorf("tick and notes = %q, %v; want the tick, then the notes", out, err)
	}
	if out, _ := StartContext(s, none, noHint, "", "clear", "c1", later); strings.Contains(out, "ticked by itself") || !strings.Contains(out, "Your notes") {
		t.Errorf("again = %q, want the notes without the tick", out)
	}
}

func noHint(todo.Store) string { return "" }

// The take-over hint's line goes out after the list and notes, alone when they are empty; after a compaction too.
func TestStartContextHint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := todo.Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	none := func() ([]todo.Session, error) { return nil, nil }
	hint := func(todo.Store) string { return "[dev todo] An earlier boss's list for dev waits" }
	out, err := StartContext(s, none, hint, "", "startup", "c1", now)
	if err != nil || !strings.Contains(out, `"additionalContext":"[dev todo] An earlier boss's list for dev waits"`) {
		t.Errorf("empty list: %q, %v, want the hint alone", out, err)
	}
	if out, _ := StartContext(s, none, hint, "", "compact", "c1", now); !strings.Contains(out, "An earlier boss's list for dev waits") {
		t.Errorf("compact: %q, want the hint", out)
	}
}

// A start's list is what the next reminder compares against, a compaction's too.
func TestStartContextShown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := todo.Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(todo.Item{Text: "Ship", Details: "d"}, now); err != nil {
		t.Fatal(err)
	}
	none := func() ([]todo.Session, error) { return nil, nil }
	remind := func() string {
		t.Helper()
		l, err := s.Read(now)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Remind(l, nil, "c1", now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if _, err := StartContext(s, none, noHint, "", "clear", "c1", now); err != nil {
		t.Fatal(err)
	}
	if got := remind(); !strings.Contains(got, "1 open, none new or changed") {
		t.Errorf("after a start = %q, want the count line", got)
	}
	if _, err := StartContext(s, none, noHint, "", "compact", "c1", now); err != nil {
		t.Fatal(err)
	}
	if got := remind(); !strings.Contains(got, "1 open, none new or changed") {
		t.Errorf("after a compaction = %q, want the count line", got)
	}
}

// After a dev agent clear's /clear, a start hook that can't read the list still gives the handoff's line, with why
// the list is missing, and reports the error: the fresh boss isn't left without it.
func TestBossHookHandoffListError(t *testing.T) {
	const path = "/s/dev/handoff/boss/2026-11-24-120000.md"
	c := handoffTmux(t, "boss", "1", ClearValue("s0", path, soon))
	s, err := todo.ForPane(c, "%1")
	if err == nil {
		err = s.Set([]string{"a"}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	lists, err := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*"))
	if err != nil || len(lists) == 0 {
		t.Fatalf("no list file: %v", err)
	}
	for _, l := range lists {
		if err := os.WriteFile(l, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"clear"}`), noHint)
	if err == nil {
		t.Error("the list's error isn't reported")
	}
	var got struct {
		Out struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("out %q: %v", out, jerr)
	}
	for _, want := range []string{"Your handoff from before this /clear: " + path, "dev couldn't give your list and notes", "run dev todo and dev todo note"} {
		if !strings.Contains(got.Out.Context, want) {
			t.Errorf("context = %q, want %q in it", got.Out.Context, want)
		}
	}
}

// A coordinator's fresh start (startup, not clear or resume) records the dev version it started on; with
// every coordinator restarted on it, a restart:<version> item ticks by dev at that start, its boss told in its list.
func TestBossHookStartVersion(t *testing.T) {
	old := devVersion
	devVersion = func() string { return "v0.93.0" }
	t.Cleanup(func() { devVersion = old })
	for _, source := range []string{"startup", "clear", "resume"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			version := ""
			c := tmux.NewFake(func(args []string) (string, error) {
				switch args[0] {
				case "set-option":
					if args[len(args)-2] == todo.OptVersion {
						version = args[len(args)-1]
					}
				case "list-panes":
					if strings.Contains(args[len(args)-1], todo.OptVersion) {
						return tmux.FormatRow("boss", "coordinator", version, "0"), nil
					}
				case "display-message":
					switch args[len(args)-1] {
					case "#{@dev_boss_pane}":
						return "coordinator\n", nil
					case "#{" + OptHandoff + "}", "#{" + agent.OptEndedSession + "}":
						return "\n", nil
					}
					if strings.Contains(args[len(args)-1], "@dev_boss_inbox") {
						return tmux.FormatRow("$1", "1700000000", "boss", "1", "", "/tmp/tmux-1/default"), nil
					}
					return tmux.FormatRow("/tmp/tmux-1/default", "boss", "1", "$1", "1700000000"), nil
				}
				return "", nil
			})
			s, err := todo.ForPane(c, "%1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Add(todo.Item{Text: "Restart for v0.93.0", Ends: "restart:v0.93.0", Details: "close and start each boss"}, time.Now().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"`+source+`"}`), noHint)
			if err != nil {
				t.Fatal(err)
			}
			at, v, _ := strings.Cut(version, " ")
			ticked := strings.Contains(out, `#1 \"Restart for v0.93.0\" closed by dev: restarted on v0.93.0 (--ends restart:v0.93.0)`)
			if source != "startup" {
				if version != "" || ticked {
					t.Errorf("❌ %s recorded %q, ticked %v: no fresh start", source, version, ticked)
				}
				return
			}
			if sec, err := strconv.ParseInt(at, 10, 64); err != nil || time.Since(time.Unix(sec, 0)) > time.Minute || v != "v0.93.0" {
				t.Errorf("❌ recorded %q, want <now> v0.93.0", version)
			}
			if !ticked || strings.Contains(out, "\n1. Restart") {
				t.Errorf("❌ start list %q: want the restart item ticked and told", out)
			}
		})
	}
}

// dev's rules failing (the coordinators unreadable) still give the start list, with the error.
func TestBossHookStartExpireError(t *testing.T) {
	c := handoffTmux(t, "boss", "1", "")
	failing := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "list-panes" && strings.Contains(args[len(args)-1], todo.OptVersion) {
			return "", errors.New("list-panes failed")
		}
		return c.Run(args...)
	})
	s, err := todo.ForPane(failing, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(todo.Item{Text: "Restart for v0.93.0", Ends: "restart:v0.93.0", Details: "close and start each boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, _, err := BossHook(failing, "%1", []byte(`{"hook_event_name":"SessionStart","source":"resume"}`), noHint)
	if err == nil || !strings.Contains(err.Error(), "list-panes failed") || !strings.Contains(out, "1. Restart for v0.93.0") {
		t.Errorf("❌ out %q, err %v: want the list and the error", out, err)
	}
}
