package inbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// bossTmux is a tmux server with one dev boss session, "boss", whose coordinator is %1; todoHerd is the
// @dev_herd its to do list reads ("" makes the list unreadable).
func bossTmux(t *testing.T, todoHerd string) *tmux.Client {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("boss", "1", "%1", "/p/boss", "t", "claude", "idle", "claude", "", "", "", "$1", "", "1", "", "", "", "", "", ""), nil
		case "display-message":
			if strings.Contains(args[len(args)-1], "@dev_boss_inbox") {
				return tmux.FormatRow("$1", "1700000000", "boss", "1", "", "/tmp/tmux-1/default"), nil
			}
			return tmux.FormatRow("/tmp/tmux-1/default", "$1", "1700000000", "boss", todoHerd), nil
		}
		return "", nil
	})
}

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
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".`, true},
		{"prompt with notes", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, true, []string{"Merge PR #9"}, pasted + "\n\n[dev todo] Your open to do list:\n1. Merge PR #9\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".`, false},
		{"notes, empty list", `{"hook_event_name":"Stop"}`, true, nil, pasted, true},
		{"pasted batch", `{"hook_event_name":"UserPromptSubmit","prompt":` + quote(pasted) + `}`, false, []string{"Merge PR #9"}, "[dev todo] Your open to do list:\n1. Merge PR #9\n" +
			`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".`, false},
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

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
