package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// dev todo --all --json prints each boss's list by name, a list not read as a stale row with its error and no
// items; the text lists each item after its boss.
func TestPrintAll(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-b", "1") + tmux.FormatRow("boss-a", "1"), nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	now := time.Unix(1700000000, 0)
	for _, boss := range []string{"boss-a", "boss-b"} {
		s, err := todo.ForPane(c, tmux.SessionTarget(boss))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(todo.Item{Text: boss + "'s"}, now); err != nil {
			t.Fatal(err)
		}
	}
	bad, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "dev", "todo", "*boss-b.json"))
	if err := os.WriteFile(bad[0], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printAll(&out, io.Discard, c, true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`"boss":"boss-a"`, `"text":"boss-a's"`, `"boss":"boss-b","error":"list not read: `, `"items":[],"updated":0,"stale":true`} {
		if !strings.Contains(strings.Join(strings.Fields(got), ""), strings.Join(strings.Fields(want), "")) {
			t.Errorf("JSON lacks %s:\n%s", want, got)
		}
	}
	if strings.Index(got, "boss-a") > strings.Index(got, "boss-b") {
		t.Errorf("JSON not by name:\n%s", got)
	}
	out.Reset()
	if err := printAll(&out, io.Discard, c, false); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "boss-b\t⚠ list not read: ") || !strings.Contains(text, "boss-a\t") {
		t.Errorf("text:\n%s", text)
	}
}

// --all says who is on an item, but not that a closed boss is on its own: no one is.
func TestPrintBossListDoing(t *testing.T) {
	now := time.Unix(1700000000, 0)
	items := []todo.Shown{{ID: 1, Text: "mine", Doing: &todo.Doing{}}, {ID: 2, Text: "its", On: todo.Boss, Doing: &todo.Doing{}}}
	for _, tt := range []struct {
		closed bool
		want   string
	}{
		{false, "boss-a\t1\t☐\tmine\tthe user is on it\nboss-a\t2\t☐\tits\tboss's own\tthe boss is on it\n"},
		{true, "boss-a\t1\t☐\tmine\tthe user is on it\nboss-a\t2\t☐\tits\tboss's own\n"},
	} {
		var out bytes.Buffer
		printBossList(&out, bossList{Boss: "boss-a", Closed: tt.closed, List: todo.List{Items: items}}, now)
		if out.String() != tt.want {
			t.Errorf("closed %v: %q, want %q", tt.closed, out.String(), tt.want)
		}
	}
}
