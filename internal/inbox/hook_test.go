package inbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow(name, "1", "%1", "/p/boss", "t", "claude", "idle", "claude", "", "", "", "$1", "", "1", "", "", "", "", "", "", "", ""), nil
		case "display-message":
			if args[len(args)-1] == "#{@dev_boss_pane}" {
				return map[string]string{"%1": "coordinator\n", "%2": "previews\n"}[args[3]], nil
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
const who = "\nAn item a worker session or someone else does, not the user: dev todo add --replaces <n> --on <worker name|person>."

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
		{"stop with notes", `{"hook_event_name":"Stop"}`, true, []string{"Merge PR #9", "Answer Vince"}, pasted + "\n\n[dev todo] Your open to do list:\n1. Merge PR #9\n2. Answer Vince\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who, true},
		{"prompt with notes", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, true, []string{"Merge PR #9"}, pasted + "\n\n[dev todo] Your open to do list:\n1. Merge PR #9\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who, false},
		{"notes, empty list", `{"hook_event_name":"Stop"}`, true, nil, pasted, true},
		{"pasted batch", `{"hook_event_name":"UserPromptSubmit","prompt":` + quote(pasted) + `}`, false, []string{"Merge PR #9"}, "[dev todo] Your open to do list:\n1. Merge PR #9\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + who, false},
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
			out, busy, err := BossHook(c, "%1", []byte(tt.input))
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
			if out, busy, err := BossHook(c, tt.pane, []byte(tt.input)); out != "" || busy || err != nil {
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
	out, busy, err := BossHook(c, "%1", []byte(`{"hook_event_name":"Stop"}`))
	if !errors.Is(err, todo.ErrNotBoss) || !busy || !strings.Contains(out, `"reason":"[dev boss inbox] 1 worker update(s):\n- w finished: w finished"}`) {
		t.Errorf("BossHook = %q, busy %v, %v; want the notes, busy, and the list's error", out, busy, err)
	}
}

// A coordinator starting, resumed or /clear'ed gets its open list, kept from an earlier session on a start;
// nothing after a compaction, with no open item, or for another pane in the boss session.
func TestBossHookStart(t *testing.T) {
	check := "\nCheck each still holds: tick what's done (dev todo done <n>), update what changed (dev todo add --replaces <n> \"<new text>\")." + who
	for _, tt := range []struct {
		name, source, pane string
		list               []string
		want               string
	}{
		{"startup", "startup", "%1", []string{"Mon: reply to Vince"}, "[dev todo] Your open to do list (kept from your last boss session, updated just now):\n1. Mon: reply to Vince" + check},
		{"clear", "clear", "%1", []string{"a", "b"}, "[dev todo] Your open to do list (updated just now):\n1. a\n2. b" + check},
		{"resume", "resume", "%1", []string{"a"}, "[dev todo] Your open to do list (updated just now):\n1. a" + check},
		{"compact", "compact", "%1", []string{"a"}, ""},
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
			out, busy, err := BossHook(c, tt.pane, []byte(`{"hook_event_name":"SessionStart","source":"`+tt.source+`"}`))
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
			if got.Out.Context != tt.want {
				t.Errorf("context = %q, want %q", got.Out.Context, tt.want)
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
	out, err := StartContext(s, func() ([]todo.Session, error) { return nil, nil }, "startup", then.Add(49*time.Hour))
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
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"resume"}`))
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
					return tmux.FormatRow("manager", "boss-x", "", "", "", "1"), nil
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
