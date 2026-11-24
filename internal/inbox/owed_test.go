package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/todo"
)

// compactContext is the coordinator %1's SessionStart hook context after a compaction, its list "Ship v1" and one note.
func compactContext(t *testing.T, name string, inbox func(Box)) string {
	t.Helper()
	c := namedTmux(t, name, "1")
	s, err := todo.ForPane(c, "%1")
	if err == nil {
		err = s.Set([]string{"Ship v1"}, time.Now())
	}
	if err == nil {
		_, err = s.AddNote(todo.Note{Kind: todo.NoteDecided, Text: "CSV only", Why: "parser"}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := Coordinator(c, "%1")
	if !ok || err != nil {
		t.Fatalf("Coordinator = %v, %v", ok, err)
	}
	inbox(b.Box)
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"compact","session_id":"c1"}`), noHint)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Out struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("out %q: %v", out, err)
	}
	return got.Out.Context
}

// After a compaction the manager gets what it owes first (its pick not relayed: item and option; an item marked asked
// in it now), then its list and notes; a boss with nothing owed gets its list and notes alone.
func TestCompactOwed(t *testing.T) {
	now := time.Now()
	got := compactContext(t, Manager, func(b Box) {
		if err := b.Picked([]agent.Pick{{Question: "dev #3: Ship v1 today?", Answer: "Yes", Labels: []string{"Yes", "No"}}}, now); err != nil {
			t.Fatal(err)
		}
		if err := b.SetAsking([]Asking{{Boss: "boss-app", ID: 2, Text: "Which DB?", At: now.Unix()}}); err != nil {
			t.Fatal(err)
		}
	})
	owed := "[dev] Owed from before the compaction:\n" +
		`- The user picked "Yes" on "dev #3: Ship v1 today?", not relayed yet: relay it with dev todo answer --via manager.` + "\n" +
		`- boss-app #2 "Which DB?" is marked asked in manager now, with no pick since: re-check it (dev todo show --print) and ask it again with AskUserQuestion if still open.` + "\n"
	list, notes := strings.Index(got, "Your open to do list"), strings.Index(got, "CSV only (why: parser)")
	if !strings.HasPrefix(got, owed) || list < 0 || notes < list || !strings.Contains(got, "1. Ship v1") {
		t.Errorf("manager = %q, want the owed lines, then the list, then the notes", got)
	}
	t.Log("manager:\n" + got)

	got = compactContext(t, "boss", func(Box) {})
	if strings.Contains(got, "Owed") || !strings.HasPrefix(got, "[dev todo] Your open to do list") || !strings.Contains(got, "CSV only (why: parser)") {
		t.Errorf("boss = %q, want its list and notes alone", got)
	}
	t.Log("boss:\n" + got)
}

// A pick is owed while unspent and answered: on an item, but "Leave it open"; on a hand-on question, "Hand it on" (to
// send) or a correction (to ask again), not "Drop it"; a pick of several, each question on its own.
func TestOwedLines(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	hand := "Hand on to boss-app: CSV export\nCSV export"
	labels := []string{HandItOn, DropIt}
	for _, tt := range []struct {
		name  string
		picks []agent.Pick
		spend bool
		typed string
		want  string
	}{
		{"item", []agent.Pick{{Question: "app #1: Ship?", Answer: "Yes", Labels: []string{"Yes", LeaveOpen}}}, false, "", `picked "Yes" on "app #1: Ship?", not relayed yet`},
		{"item typed", []agent.Pick{{Question: "app #1: Ship?", Answer: "Friday", Labels: []string{"Yes", LeaveOpen}}}, false, "", `picked "Friday" on "app #1: Ship?"`},
		{"item left open", []agent.Pick{{Question: "app #1: Ship?", Answer: LeaveOpen, Labels: []string{"Yes", LeaveOpen}}}, false, "", ""},
		{"item left open, recommended", []agent.Pick{{Question: "app #1: Ship?", Answer: LeaveOpen + " (Recommended)", Labels: []string{"Yes", LeaveOpen + " (Recommended)"}}}, false, "", `picked "Leave it open (Recommended)"`},
		{"item relayed", []agent.Pick{{Question: "app #1: Ship?", Answer: "Yes", Labels: []string{"Yes", LeaveOpen}}}, true, "", ""},
		{"own question", []agent.Pick{{Question: "Use ultracode?", Answer: "Yes", Labels: []string{"Yes", "No"}}}, false, "", ""},
		{"hand it on", []agent.Pick{{Question: hand, Answer: HandItOn, Labels: labels}}, false, "", `picked "Hand it on" on this hand-on question, not sent yet: send the brief it holds with --kind work: "Hand on to boss-app: CSV export\nCSV export"`},
		{"hand-on corrected", []agent.Pick{{Question: hand, Answer: "JSON too", Labels: labels}}, false, "", `corrected the hand-on question "Hand on to boss-app: CSV export" with "JSON too"`},
		{"dropped", []agent.Pick{{Question: hand, Answer: DropIt, Labels: labels}}, false, "", ""},
		{"several", []agent.Pick{{Question: "app #1: Ship?", Answer: "Yes"}, {Question: "web #4: Tag?", Answer: "No"}}, false, "", `on "app #1: Ship?"` + ", not relayed yet: relay it with dev todo answer --via manager.\n" + `- The user picked "No" on "web #4: Tag?"`},
		{"typed since", []agent.Pick{{Question: "app #1: Ship?", Answer: "Yes", Labels: []string{"Yes", LeaveOpen}}}, false, "no, wait", "- The user typed after that pick: \"no, wait\": if it withdraws or changes the pick, ask again instead."},
		{"hand-on beside others", []agent.Pick{{Question: "app #1: Ship?", Answer: "Yes"}, {Question: hand, Answer: HandItOn, Labels: labels}}, false, "", "held a hand-on question beside others"},
	} {
		b := Box{path: t.TempDir() + "/inbox.json"}
		if err := b.Picked(tt.picks, now); err != nil {
			t.Fatal(err)
		}
		if tt.typed != "" {
			if err := b.Arrived(tt.typed, now); err != nil {
				t.Fatal(err)
			}
		}
		if tt.spend {
			p, err := b.LatestPick()
			if err != nil || b.ClaimPick(*p, -1) != nil {
				t.Fatal(err)
			}
		}
		got, err := owedLines(b, now)
		if err != nil || (tt.want == "" && got != "") || !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

// An inbox it can't read still gives the manager its list, after a line saying what it owes is unknown.
func TestCompactOwedUnread(t *testing.T) {
	c := namedTmux(t, Manager, "1")
	s, err := todo.ForPane(c, "%1")
	if err == nil {
		err = s.Set([]string{"Ship v1"}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := Coordinator(c, "%1")
	if !ok || err != nil {
		t.Fatalf("Coordinator = %v, %v", ok, err)
	}
	if err := os.MkdirAll(filepath.Dir(b.Box.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Box.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := BossHook(c, "%1", []byte(`{"hook_event_name":"SessionStart","source":"compact","session_id":"c1"}`), noHint)
	if err == nil {
		t.Error("the inbox's error isn't reported")
	}
	if !strings.Contains(out, `[dev] dev couldn't tell what you still owe from before the compaction (`) || !strings.Contains(out, "1. Ship v1") {
		t.Errorf("out = %q, want the unknown owed line and the list", out)
	}
}
