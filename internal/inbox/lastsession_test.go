package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
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

## PRs and links named
PR #335 · terminal#158

## Messages
- m845 to manager (26.11 17:08): Analysis done

## Left from the session before, still open
- #314 Finding: the board asks to pair
`
	if got := LastSessionText("boss-dev", EndedClosed, f, workers, nil, msgs); got != want {
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
	// Session starts are in seconds: the next one in a later second
	time.Sleep(1100 * time.Millisecond)
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
	time.Sleep(1100 * time.Millisecond)
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
	if _, err := s.EndSession(time.Now().Add(-time.Hour), 0, 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(todo.Item{Text: "left half-way", On: "boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A live boss's unwritten session is its own
	live := namedTmux(t, "boss-live", "1")
	t.Setenv("XDG_STATE_HOME", state)
	ls, err := todo.ForPane(live, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ls.EndSession(time.Now().Add(-time.Hour), 0, 0, true, nil); err != nil {
		t.Fatal(err)
	}
	// Only the manager's and boss-live's sessions are open now
	inner := namedTmux(t, Manager, "1")
	t.Setenv("XDG_STATE_HOME", state)
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "list-sessions" {
			// The manager's name, then empty fields
			row := make([]string, strings.Count(args[len(args)-1], tmux.Sep))
			row[0] = Manager
			rows := tmux.FormatRow(row...)
			row[0] = "boss-live"
			return rows + tmux.FormatRow(row...), nil
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
	if _, un, err := s.SessionStart(); err != nil || un {
		t.Errorf("unwritten = %v, %v after the sweep", un, err)
	}
	if _, un, err := ls.SessionStart(); err != nil || !un {
		t.Errorf("boss-live unwritten = %v, %v: want its live session left to it", un, err)
	}
	if _, err := os.Stat(strings.Replace(path, "boss-gone", "boss-live", 1)); !os.IsNotExist(err) {
		t.Errorf("a live boss got a file: %v", err)
	}
}

// A boss's close writes its file as closed, with no mark before from its session's creation; undo, for a kill that
// failed, leaves its session to its later close.
func TestBossClosing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := namedTmux(t, "boss-x", "1")
	s, err := todo.ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-time.Hour).Unix()
	if _, err := s.Add(todo.Item{Text: "made before the upgrade", On: "boss"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	undo, err := BossClosing(c, "boss-x", created)
	if err != nil || undo == nil {
		t.Fatalf("BossClosing: undo %t, %v", undo != nil, err)
	}
	path, _ := LastSessionPath("boss-x")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, ", closed.") || !strings.Contains(text, "- #1 made before the upgrade") {
		t.Errorf("file =\n%s", text)
	}
	if again, err := BossClosing(c, "boss-x", created); err != nil || again != nil {
		t.Errorf("a second close: undo %t, %v: want nothing written", again != nil, err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if _, un, err := s.SessionStart(); err != nil || !un {
		t.Errorf("after undo unwritten = %v, %v", un, err)
	}
}

// A boss's file lists the workers it started that closed in its session and those still open, last; and the
// messages to or from it in its session.
func TestSessionWorkersAndMessages(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	start := time.Unix(1700000000, 0)
	f := todo.SessionFacts{Start: start.Unix(), End: start.Add(time.Hour).Unix()}
	for name, e := range map[string]sidebar.Ended{
		"w-in":      {Session: "w-in", StartedBy: "boss-x", StartedAt: start.Add(time.Minute), At: start.Add(2 * time.Minute)},
		"w-other":   {Session: "w-other", StartedBy: "boss-y", At: start.Add(2 * time.Minute)},
		"w-earlier": {Session: "w-earlier", StartedBy: "boss-x", At: start.Add(-time.Minute)},
	} {
		dir := filepath.Join(state, "dev", "archive", name)
		data, _ := json.Marshal(e)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			// session_name, session_id, session_created, @dev_started_by
			return tmux.FormatRow("boss-x", "$1", "1700000000", "") + tmux.FormatRow("w-open", "$2", "1700000600", "boss-x") +
				tmux.FormatRow("w-else", "$3", "1700000600", "boss-y"), nil
		case "display-message":
			return "/tmp/tmux-1/default\n", nil
		}
		return "", nil
	})
	// One record unreadable: named, the others still read
	bad := filepath.Join(state, "dev", "archive", "w-bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "session.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, unread, err := sessionWorkers(c, "boss-x", f)
	if err != nil {
		t.Fatal(err)
	}
	if unread == nil || !strings.Contains(unread.Error(), "w-bad") {
		t.Errorf("unread = %v, want w-bad named", unread)
	}
	if text := LastSessionText("boss-x", EndedClosed, f, ws, unread, nil); !strings.Contains(text, "⚠ archive records not read") {
		t.Errorf("text without the unread line:\n%s", text)
	}
	var names []string
	for _, w := range ws {
		names = append(names, w.Name)
	}
	if strings.Join(names, " ") != "w-in w-open" || !ws[1].Closed.IsZero() {
		t.Errorf("workers = %+v, want w-in then w-open still open", ws)
	}
	store, err := OpenMessages("/tmp/tmux-1/default")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		from, to string
		at       time.Time
	}{{"manager", "boss-x", start.Add(time.Minute)}, {"boss-y", "manager", start.Add(time.Minute)}, {"boss-x", "manager", start.Add(-time.Minute)}} {
		if _, err := store.Post(m.from, m.to, "hi "+m.from, false, m.at); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := sessionMessages(c, "boss-x", f)
	if err != nil || len(msgs) != 1 || msgs[0].From != "manager" {
		t.Errorf("messages = %+v, %v: want only the manager's in the session", msgs, err)
	}
}
