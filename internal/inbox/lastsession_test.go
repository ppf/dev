package inbox

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func TestLastSessionText(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("02.01.2006 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	f := todo.SessionFacts{
		Start: at("26.11.2026 09:23").Unix(), End: at("26.11.2026 20:38").Unix(),
		Ticked: []todo.Item{
			{ID: 345, Text: "Build fixes Q2-Q6?", Done: at("26.11.2026 17:03").Unix(), DoneBy: "you", Answer: "All"},
			{ID: 352, Text: "Q4 time stamps, then release v0.98.0", Done: at("26.11.2026 18:32").Unix(), DoneBy: "boss-dev",
				Result: "PR #335 merged; pin terminal#158"},
		},
		Added: []todo.Item{{ID: 357, Text: "Manager: closing a boss needs no advisor call"}},
		Notes: []todo.Note{{ID: 104, Kind: todo.NoteDecided, Text: "one release v0.98.0", Added: at("26.11.2026 17:08").Unix()}},
		State: &todo.Note{ID: 105, Kind: todo.NoteState, Text: "v0.98.0 live; next: restart #356", Why: "release done", Added: at("26.11.2026 18:32").Unix()},
		Left:  []todo.Item{{ID: 314, Text: "Finding: the board asks to pair"}},
	}
	workers := []Worker{
		{Name: "dev-fix-q4", Started: at("26.11.2026 17:07"), Closed: at("26.11.2026 18:32")},
		{Name: "dev-other", Started: at("26.11.2026 18:00")},
	}
	msgs := []Msg{{ID: "m845", From: "boss-dev", To: "manager", Text: "Analysis done\nmore", At: at("26.11.2026 17:08").Unix()}}
	want := `# boss-dev: last session

Session 26.11 09:23 → 26.11 20:38, closed. Written by dev at 26.11 20:38, over the session before; your list: dev todo.

## From the boss (state note)
n105, 26.11 18:32: v0.98.0 live; next: restart #356
Why: release done

## Done this session
- #345 Build fixes Q2-Q6? (26.11 17:03, you): answered All
- #352 Q4 time stamps, then release v0.98.0 (26.11 18:32, boss-dev): PR #335 merged; pin terminal#158

## Added this session, still open
- #357 Manager: closing a boss needs no advisor call

## Open asks to the user
None.

## Notes added
- n104 decided (26.11 17:08): one release v0.98.0

## Workers
| worker | started | closed |
|---|---|---|
| dev-fix-q4 | 26.11 17:07 | 26.11 18:32 |
| dev-other | 26.11 18:00 | still open |

## PRs named
PR #335 · terminal#158

## Messages
- m845 to manager (26.11 17:08): Analysis done

## Left from the session before, still open
- #314 Finding: the board asks to pair
`
	if got := LastSessionText("boss-dev", EndedClosed, f, workers, msgs); got != want {
		t.Errorf("text =\n%s\nwant\n%s", got, want)
	}
}

// startContext is a SessionStart hook output's added context.
func startContext(t *testing.T, out string) string {
	t.Helper()
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
	return got.Out.Context
}

// A boss's start writes the file of a session gone without one, a /clear overwrites it with the next, and the
// start's context names it.
func TestBossStartLastSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := namedTmux(t, "boss-x", "1")
	s, err := todo.ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	path, err := LastSessionPath("boss-x")
	if err != nil {
		t.Fatal(err)
	}
	start := func(source string) string {
		out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"`+source+`"}`), noHint)
		if err != nil {
			t.Fatal(err)
		}
		return startContext(t, out)
	}
	if ctx := start("startup"); strings.Contains(ctx, "last session") {
		t.Errorf("first start names a file: %q", ctx)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("first start wrote %s: %v", path, err)
	}
	if _, err := s.Add(todo.Item{Text: "first session's", On: "boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Gone without a close: the next start writes it
	ctx := start("startup")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, EndedGoneStart) || !strings.Contains(text, "first session's") {
		t.Errorf("file =\n%s", text)
	}
	if !strings.Contains(ctx, "Your last session's file: "+path) {
		t.Errorf("context = %q, want the file named", ctx)
	}
	if ctx := start("resume"); strings.Contains(ctx, "last session") {
		t.Errorf("a resume names the file: %q", ctx)
	}
	if _, err := s.Add(todo.Item{Text: "second session's", On: "boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	start("clear")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "# boss-x: last session") != 1 || !strings.Contains(text, EndedCleared) ||
		!strings.Contains(text, "second session's") || !strings.Contains(text, "Left from the session before, still open\n- #1 first session's") {
		t.Errorf("after /clear, file =\n%s\nwant it overwritten, #1 left from before", text)
	}
}

// The manager's start writes the files of bosses gone without one and names the files there are; none for itself.
func TestManagerStartLastSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := t.TempDir()
	boss := namedTmux(t, "boss-gone", "1")
	t.Setenv("XDG_STATE_HOME", state)
	s, err := todo.ForPane(boss, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EndSession(time.Now().Add(-time.Hour), 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(todo.Item{Text: "left half-way", On: "boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Only the manager's session is open now
	inner := namedTmux(t, Manager, "1")
	t.Setenv("XDG_STATE_HOME", state)
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "list-sessions" {
			// The manager's name, then empty fields
			row := make([]string, strings.Count(args[len(args)-1], tmux.Sep))
			row[0] = Manager
			return tmux.FormatRow(row...), nil
		}
		return inner.Run(args...)
	})
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"startup"}`), noHint)
	if err != nil {
		t.Fatal(err)
	}
	path, err := LastSessionPath("boss-gone")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, EndedGoneManager) || !strings.Contains(text, "left half-way") {
		t.Errorf("file =\n%s", text)
	}
	dir, _ := LastSessionDir()
	if ctx := startContext(t, out); !strings.Contains(ctx, "Bosses' last-session files in "+dir+" (boss-gone)") {
		t.Errorf("context = %q, want the files named", ctx)
	}
	if _, err := os.Stat(strings.Replace(path, "boss-gone", Manager, 1)); !os.IsNotExist(err) {
		t.Errorf("the manager got a file: %v", err)
	}
	if un, err := s.Unwritten(); err != nil || un {
		t.Errorf("Unwritten = %v, %v after the sweep", un, err)
	}
}
