package cmd

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// add and ask take --details (- reads stdin), --replaces on either keeps them unless given; done --result ticks one item
// with what was done, and the item says who ticked it: the boss in Claude Code, else the user, a board's --text tick too.
func TestTodoDetailsResult(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDECODE", "1")
	run(t, "Second reminder, firmer.\n\n- attach the PDF\n", "todo", "add", "Send the reminder", "--details", "-")
	run(t, "", "todo", "add", "--no-details", "--replaces", "1", "--link", "https://mail.example/1")
	run(t, "", "todo", "ask", "Send it today?", "--option", "Yes (Recommended)", "--option", "No", "--details", "The customer asked for Friday.")
	run(t, "", "todo", "ask", "--replaces", "2", "--context", "asked on Monday")
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"details": "The customer asked for Friday."`) {
		t.Errorf("ask --replaces without --details dropped them:\n%s", got)
	}
	run(t, "", "todo", "ask", "--replaces", "2", "--details", "The customer asked for Thursday.")
	run(t, "", "todo", "ask", "--replaces", "2", "--details", "The customer asked for Friday.")
	run(t, "", "todo", "add", "--no-details", "Other")
	run(t, "", "todo", "add", "--no-details", "Mine")
	run(t, "", "todo", "add", "--no-details", "Board's")
	if _, _, err := execute(t, "", "todo", "add", "x", "--details", "-"); err == nil || !strings.Contains(err.Error(), "stdin is empty") {
		t.Errorf("--details - on empty stdin: %v", err)
	}
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
	run(t, "", "todo", "done", "5", "--text", "Board's", "--result", "called")
	t.Setenv("CLAUDECODE", "")
	run(t, "", "todo", "done", "4")
	if _, _, err := execute(t, strings.Repeat("x", 64<<10+1), "todo", "add", "x", "--details", "-"); err == nil || !strings.Contains(err.Error(), "stdin is over") {
		t.Errorf("--details - over the byte cap: %v", err)
	}
	if _, _, err := execute(t, "", "todo", "ask", "Q?", "--option", "A (Recommended)", "--option", "B", "--details", strings.Repeat("x", todo.MaxDetails+1)); err == nil || !strings.Contains(err.Error(), "at most 4000") {
		t.Errorf("ask over MaxDetails: %v", err)
	}
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
		4: "||" + todo.DoneByYou + "|",
		5: "|called|" + todo.DoneByYou + "|",
	} {
		if got[id] != want {
			t.Errorf("item %d = %q, want %q", id, got[id], want)
		}
	}
}

// dev todo --all --json and dev serve's board carry a ticked item's context, details, result and who ticked it, and an
// open item's worker's latest note.
func TestTodoAllFinishedDetails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-vitay", "1"), nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		case "list-panes":
			// boss-vitay and its worker vitay-mail, every field 0 but these
			rows := ""
			for _, set := range []map[string]string{{"#{session_name}": "boss-vitay", "#{session_id}": "$1"},
				{"#{session_name}": "vitay-mail", "#{session_id}": "$2", "#{pane_id}": "%2", "#{window_index}": "1", "#{@agent_state}": "working",
					"#{@dev_started_by}": "boss-vitay", "#{@dev_boss_note}": "finished: draft ready"}} {
				var row []string
				for _, f := range strings.Split(args[len(args)-1], tmux.Sep) {
					row = append(row, cmp.Or(set[f], "0"))
				}
				rows += tmux.FormatRow(row[:len(row)-1]...)
			}
			return rows, nil
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
	if _, err := s.Add(todo.Item{Text: "Draft the reply", On: "vitay-mail", Session: &todo.Session{ID: "$2", Name: "vitay-mail"}}, t0); err != nil {
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
	for name, got := range map[string]string{"--all --json": strings.Join(strings.Fields(out.String()), ""), "board": strings.Join(strings.Fields(string(board)), "")} {
		for _, want := range []string{`"context":["template:`, `"details":"Second`, `"result":"Mail`, `"done_by":"boss-vitay"`, `"note":"finished:draftready"`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %s:\n%s", name, want, got)
			}
		}
	}
}
