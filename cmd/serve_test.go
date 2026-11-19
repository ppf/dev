package cmd

import (
	"encoding/json"
	"flag"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// /v1/board on invented lists, read by dev todo --all --json's code, is internal/serve/testdata/board.json: the
// shape the phone page reads. go test ./cmd -run TestBoardGolden -update rewrites it.
func TestBoardGolden(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-b", "1") + tmux.FormatRow("boss-a", "1"), nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		case "list-panes":
			// boss-a live: every field 0 but its name and id
			var row []string
			for _, f := range strings.Split(args[len(args)-1], tmux.Sep) {
				row = append(row, map[string]string{"#{session_name}": "boss-a", "#{session_id}": "$1"}[f])
				if row[len(row)-1] == "" {
					row[len(row)-1] = "0"
				}
			}
			return tmux.FormatRow(row[:len(row)-1]...), nil
		}
		return "", nil
	})
	now := time.Unix(1700000000, 0)
	s, err := todo.ForPane(c, tmux.SessionTarget("boss-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []todo.Item{
		{Text: "Merge #148 now or after the release?", Options: []string{"Merge now (Recommended)", "After the release"}},
		{Text: "Read the plan", Link: "https://example.com/plan", Until: 1700086400},
		{Text: "Send the invoice", On: "alice"},
	} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	b, err := todo.ForPane(c, tmux.SessionTarget("boss-b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(todo.Item{Text: "unread"}, now); err != nil {
		t.Fatal(err)
	}
	bad, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*boss-b.json"))
	if err := os.WriteFile(bad[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := serve.Handler(serve.Config{
		Version: "v0.0.0-golden",
		Now:     func() time.Time { return now },
		Lists: func() (json.RawMessage, error) {
			lists, err := todoAll(c, io.Discard, now, true)
			if err != nil {
				return nil, err
			}
			return json.Marshal(lists)
		},
		Guard: serve.LoopbackOnly(serve.Loopback),
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8417/v1/board", nil))
	if w.Code != 200 {
		t.Fatalf("board %d: %s", w.Code, w.Body)
	}
	// The unreadable list's error names a temp file
	got := strings.ReplaceAll(w.Body.String(), os.Getenv("XDG_STATE_HOME"), "$XDG_STATE_HOME")
	golden := filepath.Join("..", "internal", "serve", "testdata", "board.json")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("board differs from %s (-update rewrites it):\n%s", golden, got)
	}
}
