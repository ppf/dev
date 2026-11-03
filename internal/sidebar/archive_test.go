package sidebar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the archive records the kill tests write out of the user's state dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dev-sidebar-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
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
			dir, err := archive(w.client(), "app/x", Ended{Note: "n"})
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
