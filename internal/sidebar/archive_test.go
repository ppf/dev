package sidebar

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// TestMain keeps the archive records the kill tests write out of the user's state dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dev-sidebar-state")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		panic(err)
	}
	os.Exit(code)
}

func TestArchive(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pane := func(id, transcript, endedID, endedTranscript string) map[string]string {
		return map[string]string{"session_name": "app/x", "@project": "app", "session_path": "/app",
			"@agent_session": id, "@agent_transcript": transcript, "@agent_ended_session": endedID, "@agent_ended_transcript": endedTranscript}
	}
	for _, tt := range []struct {
		name             string
		panes            []map[string]string
		conversation, tr string
	}{
		{"a live conversation beats an earlier pane's ended one", []map[string]string{
			pane("", "", "", ""), pane("", "", "old", "/t/old"), pane("live", "/t/live", "", "")}, "live", "/t/live"},
		{"else the ended one", []map[string]string{pane("", "", "", ""), pane("", "", "old", "/t/old")}, "old", "/t/old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &actWorld{panes: tt.panes}
			dir, _, err := archive(w.client(), "app/x", Ended{Note: "n"})
			if err != nil {
				t.Fatal(err)
			}
			// A / in the name doesn't nest the directory
			if filepath.Dir(dir) != filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev/archive") || !strings.HasPrefix(filepath.Base(dir), "app_x") {
				t.Errorf("dir = %s", dir)
			}
			data, err := os.ReadFile(filepath.Join(dir, recordFile))
			if err != nil {
				t.Fatal(err)
			}
			var e Ended
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			if e.Session != "app/x" || e.Project != "app" || e.Path != "/app" || e.Conversation != tt.conversation || e.Transcript != tt.tr || e.Note != "n" {
				t.Errorf("record = %+v", e)
			}
		})
	}
}

// A session the kill leaves keeps no record.
func TestKillNowUndyingNoRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	w := &actWorld{sessions: []string{"a", "b"}, undying: true}
	if killed, _, err := killNow(w.client(), "/dev/tty1", "a", "b", Ended{}); killed || err != nil {
		t.Fatalf("killNow = %t, %v", killed, err)
	}
	if entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev/archive")); err != nil || len(entries) != 0 {
		t.Errorf("archive = %v, %v; want empty", entries, err)
	}
}

// A record names its starter by name and when its session was made; a boss's close writes its last-session file
// first (BossEnding), a failure noted in the record; a session that is no boss's writes none.
func TestArchiveStarterAndBossEnding(t *testing.T) {
	for _, herd := range []string{"1", ""} {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		w := &actWorld{panes: []map[string]string{{"session_name": "boss-x", "session_created": "1700000000",
			"@dev_started_by": "$3", "@dev_herd": herd}}}
		c := tmux.NewFake(func(args []string) (string, error) {
			if slices.Contains(args, "$3") && slices.Contains(args, "#{session_name}") {
				return "manager\n", nil
			}
			return w.run(args)
		})
		var ended []string
		BossEnding = func(_ *tmux.Client, session string, created int64) (func() error, error) {
			ended = append(ended, fmt.Sprintf("%s %d", session, created))
			return nil, errors.New("disk full")
		}
		t.Cleanup(func() { BossEnding = nil })
		if _, _, err := archive(c, "boss-x", Ended{}); err != nil {
			t.Fatal(err)
		}
		records, err := Records()
		if err != nil || len(records) != 1 {
			t.Fatalf("Records = %+v, %v", records, err)
		}
		e := records[0]
		if e.StartedBy != "manager" || e.StartedAt.Unix() != 1700000000 {
			t.Errorf("record = %+v", e)
		}
		if herd == "" {
			if len(ended) != 0 || e.Note != "" {
				t.Errorf("no boss: BossEnding calls %v, note %q; want none", ended, e.Note)
			}
			continue
		}
		if !slices.Equal(ended, []string{"boss-x 1700000000"}) || e.Note != "its last-session file isn't written: disk full" {
			t.Errorf("BossEnding calls = %v, note %q", ended, e.Note)
		}
	}
}

// A boss's close whose kill fails undoes its last-session file's mark; one that kills it doesn't.
func TestKillNowUndoesBossEnding(t *testing.T) {
	for _, undying := range []bool{true, false} {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		w := &actWorld{sessions: []string{"a", "boss-x"}, undying: undying, bosses: []string{"boss-x"},
			panes: []map[string]string{{"session_name": "boss-x", "@dev_herd": "1"}}}
		undone := 0
		BossEnding = func(*tmux.Client, string, int64) (func() error, error) {
			return func() error { undone++; return nil }, nil
		}
		t.Cleanup(func() { BossEnding = nil })
		killed, _, err := killNow(w.client(), "/dev/tty1", "a", "boss-x", Ended{})
		if err != nil || killed == undying {
			t.Fatalf("undying %v: killNow = %v, %v", undying, killed, err)
		}
		if want := map[bool]int{true: 1, false: 0}[undying]; undone != want {
			t.Errorf("undying %v: undone %d times, want %d", undying, undone, want)
		}
	}
}
