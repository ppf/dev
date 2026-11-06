package herd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func TestSection(t *testing.T) {
	tail := func(lines ...string) desktop.Tail { return desktop.Tail{Lines: lines} }
	tests := []struct {
		name string
		t    tile
		want section
	}{
		{"a dialog waits", tile{sess: sidebar.Session{State: agent.Waiting}, deciding: true}, needsYouSection},
		{"working", tile{sess: sidebar.Session{State: agent.Working}}, workingSection},
		{"a decision note outlives its answered dialog", tile{sess: sidebar.Session{State: agent.Working}, note: "needs a decision: allow Bash"}, workingSection},
		{"working past an old WAITING", tile{sess: sidebar.Session{State: agent.Working}, tail: tail("WAITING (CI)")}, workingSection},
		{"its turn ended on a question", tile{sess: sidebar.Session{State: agent.Idle}, note: inbox.Note{Kind: inbox.Asks, Text: "Merge #68: now?"}.Summary()}, needsYouSection},
		{"its question beats its WAITING", tile{sess: sidebar.Session{State: agent.Idle}, note: "asks: Merge?", tail: tail("WAITING (CI)")}, needsYouSection},
		{"a decision note after its dialog closed", tile{sess: sidebar.Session{State: agent.Idle}, note: "needs a decision: allow Bash"}, restingSection},
		{"its question under the idle reminder", tile{sess: sidebar.Session{State: agent.Waiting}, note: "asks: Merge?"}, needsYouSection},
		{"waiting on CI", tile{sess: sidebar.Session{State: agent.Idle}, note: "finished: pushed", tail: tail("Pushed.", "**WAITING (CI)**")}, waitingSection},
		{"IDLE after WAITING", tile{sess: sidebar.Session{State: agent.Idle}, tail: tail("WAITING (CI)", "IDLE")}, restingSection},
		{"idle", tile{sess: sidebar.Session{State: agent.Idle}, note: "finished"}, restingSection},
		{"no state", tile{}, restingSection},
	}
	for _, tt := range tests {
		if got := tt.t.section(); got != tt.want {
			t.Errorf("%s: section = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// A shown boss reads each worker's transcript, a failed read only marking its tile; a hidden one reads no
// transcript. Both read its state's start and context use.
func TestSnapshotCard(t *testing.T) {
	rows := []row{{Session: "boss", SessionID: "$1", ID: "%1", Herd: "1", WindowActive: true},
		{Session: "w", SessionID: "$2", ID: "%2", Window: 1, Command: "claude", State: agent.Idle, StateCmd: "claude",
			StartedBy: "$1", Transcript: "/t/w.jsonl", Since: "1700000000", Context: "41"},
		{Session: "fresh", SessionID: "$3", ID: "%3", Window: 1, Command: "claude", State: agent.Idle, StateCmd: "claude", StartedBy: "$1"},
		{Session: "gone", SessionID: "$4", ID: "%4", Window: 1, Command: "claude", State: agent.Idle, StateCmd: "claude", StartedBy: "$1", Transcript: "/t/gone.jsonl"}}
	clients := []sidebar.Client{{TTY: "/dev/ttys1", Session: "boss"}}
	look := &fakeLookups{tails: map[string]desktop.Tail{"/t/w.jsonl": {Lines: []string{"Done."}}}}
	s, err := buildSnapshot(rows, clients, "%1", look)
	if err != nil {
		t.Fatal(err)
	}
	i := index(s.tiles, "w")
	if w := s.tiles[i]; !slices.Equal(w.tail.Lines, []string{"Done."}) || w.tailErr != nil || !w.since.Equal(time.Unix(1700000000, 0)) || w.context != "41" {
		t.Errorf("w = %+v, want its transcript's lines, since and context", w)
	}
	if g := s.tiles[index(s.tiles, "gone")]; g.tailErr == nil || len(s.tiles) != 3 {
		t.Errorf("gone = %+v among %d tiles, want its read error and every tile kept", g, len(s.tiles))
	}
	slices.Sort(look.read)
	if !slices.Equal(look.read, []string{"/t/gone.jsonl", "/t/w.jsonl"}) {
		t.Errorf("read %q, want w's and gone's transcripts: fresh has none yet", look.read)
	}
	look.read = nil
	s, err = buildSnapshot(rows, nil, "%1", look)
	if err != nil || len(look.read) > 0 || !s.tiles[index(s.tiles, "w")].since.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("hidden boss read %q (%v), want none, and since still read", look.read, err)
	}
}

// A transcript is read again only once its size or time changed; a missing one is none yet.
func TestTailCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	l := newLookups(tmux.NewFake(nil))
	if got, err := l.Tail(path); err != nil || got.Lines != nil {
		t.Errorf("Tail of a missing transcript = %+v, %v, want none and no error", got, err)
	}
	write := func(text string, mod time.Time) {
		row := `{"type":"assistant","message":{"model":"m","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Unix(1700000000, 0)
	write("one", at)
	if got, err := l.Tail(path); err != nil || !slices.Equal(got.Lines, []string{"one"}) {
		t.Fatalf("Tail = %+v, %v, want one", got, err)
	}
	// Same size and time: the cached read stands
	write("two", at)
	if got, _ := l.Tail(path); !slices.Equal(got.Lines, []string{"one"}) {
		t.Errorf("Tail = %q, want the cached read", got.Lines)
	}
	write("two", at.Add(time.Second))
	if got, _ := l.Tail(path); !slices.Equal(got.Lines, []string{"two"}) {
		t.Errorf("Tail = %q, want it read again after a write", got.Lines)
	}
	// An append within the same second: its size tells
	write("three", at.Add(time.Second))
	if got, _ := l.Tail(path); !slices.Equal(got.Lines, []string{"three"}) {
		t.Errorf("Tail = %q, want it read again after its size changed", got.Lines)
	}
}
