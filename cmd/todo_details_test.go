package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// add and ask take --details (- reads stdin), add --replaces keeps them unless given; done --result ticks one item
// with what was done, and the item says who ticked it.
func TestTodoDetailsResult(t *testing.T) {
	todoEnv(t)
	run(t, "Second reminder, firmer.\n\n- attach the PDF\n", "todo", "add", "Send the reminder", "--details", "-")
	run(t, "", "todo", "add", "--replaces", "1", "--link", "https://mail.example/1")
	run(t, "", "todo", "ask", "Send it today?", "--option", "Yes (Recommended)", "--option", "No", "--details", "The customer asked for Friday.")
	run(t, "", "todo", "add", "Other")
	if _, _, err := execute(t, "", "todo", "add", "x", "--details", " "); err == nil || !strings.Contains(err.Error(), "--details is empty") {
		t.Errorf("empty --details: %v", err)
	}
	if _, _, err := execute(t, "", "todo", "done", "1", "3", "--result", "x"); err == nil || !strings.Contains(err.Error(), "--result ticks one item") {
		t.Errorf("--result on two items: %v", err)
	}
	if _, _, err := execute(t, "", "todo", "done", "1", "--result", " "); err == nil || !strings.Contains(err.Error(), "--result is empty") {
		t.Errorf("empty --result: %v", err)
	}
	run(t, "", "todo", "done", "1", "--result", "  Mail sent 12:40 to accounts@example.com  ")
	var l struct {
		Items []struct {
			ID      int    `json:"id"`
			Details string `json:"details"`
			Result  string `json:"result"`
			DoneBy  string `json:"done_by"`
			Link    string `json:"link"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, it := range l.Items {
		got[it.ID] = strings.Join([]string{it.Details, it.Result, it.DoneBy, it.Link}, "|")
	}
	for id, want := range map[int]string{
		1: "Second reminder, firmer.\n\n- attach the PDF|Mail sent 12:40 to accounts@example.com|" + todo.Boss + "|https://mail.example/1",
		2: "The customer asked for Friday.|||",
		3: "|||",
	} {
		if got[id] != want {
			t.Errorf("item %d = %q, want %q", id, got[id], want)
		}
	}
}

// dev todo --all --json and dev serve's board carry a ticked item's context, details, result and who ticked it.
func TestTodoAllFinishedDetails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-vitay", "1"), nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	s, err := todo.ForPane(c, tmux.SessionTarget("boss-vitay"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Minute)
	if _, err := s.Add(todo.Item{Text: "Send the reminder", Context: []string{"template: Mahnung 2"}, Details: "Second reminder."}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.WithResult("Mail sent 12:40").Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printAll(&out, io.Discard, c, true); err != nil {
		t.Fatal(err)
	}
	board, err := boardLists(c, io.Discard, time.Now)()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"--all --json": strings.Join(strings.Fields(out.String()), ""), "board": string(board)} {
		for _, want := range []string{`"context":["template:`, `"details":"Second`, `"result":"Mail`, `"done_by":"boss-vitay"`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %s:\n%s", name, want, got)
			}
		}
	}
}
