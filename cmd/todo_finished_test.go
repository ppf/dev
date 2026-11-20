package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// A live and a closed boss whose lists hold only finished items: dev todo --all --json and dev serve's board carry
// them, items empty; the sidebar reading that JSON shows no row for them, the popup and the text list don't get them.
func TestTodoAllFinishedOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sessions := []string{"boss-live", "boss-shut", "manager"}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			var out string
			for _, n := range sessions {
				out += tmux.FormatRow(n, "1")
			}
			return out, nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		case "has-session":
			return "", nil
		}
		return "", nil
	})
	t0 := time.Now().Add(-time.Minute)
	for _, n := range []string{"boss-live", "boss-shut"} {
		s, err := todo.ForPane(c, tmux.SessionTarget(n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(todo.Item{Text: n + "'s"}, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.Done([]int{1}, t0); err != nil {
			t.Fatal(err)
		}
	}
	sessions = []string{"boss-live", "manager"}

	var out bytes.Buffer
	if err := printAll(&out, io.Discard, c, true); err != nil {
		t.Fatal(err)
	}
	var lists []sidebar.NeedList
	if err := json.Unmarshal(out.Bytes(), &lists); err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(out.String()), "")
	for _, want := range []string{`"boss":"boss-live","items":[]`, `"boss":"boss-shut","closed":true,"items":[]`, `"text":"boss-shut's"`, `"assignee":{"kind":"you"}`} {
		if !strings.Contains(flat, want) {
			t.Errorf("JSON lacks %s:\n%s", want, out.String())
		}
	}
	if len(lists) != 2 {
		t.Fatalf("--all --json: %d lists, want 2", len(lists))
	}
	sess := []sidebar.Session{{Name: "boss-live", Project: "live", ID: "$1"}, {Name: "manager", Project: "manager", ID: "$2"}}
	for _, root := range []string{"", "boss-live", sidebar.Manager} {
		if rows := sidebar.NeedRows(sess, lists, root); len(rows) != 0 {
			t.Errorf("sidebar rows under %q: %+v", root, rows)
		}
	}

	out.Reset()
	if err := printAll(&out, io.Discard, c, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no boss has a to do list\n" {
		t.Errorf("text list:\n%s", got)
	}
	if l := kanbanLists(c, nil); l.Err != nil || len(l.Lists) != 0 {
		t.Errorf("popup lists = %+v, %v", l.Lists, l.Err)
	}
	board, err := boardLists(c, io.Discard, time.Now)()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(board), `"finished":[{"id":1,"text":"boss-live's"`) {
		t.Errorf("dev serve's board lacks the finished items:\n%s", board)
	}
}
