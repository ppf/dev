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

// dev todo tally counts per list, a closed boss's included (its events and its open asks), over --days; as text with
// a total line and as JSON; the log's start is named when the window reaches before it.
func TestPrintTally(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	live := []string{"boss-a", "boss-b"}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			var rows string
			for _, n := range live {
				rows += tmux.FormatRow(n, "1")
			}
			return rows, nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	now := time.Now()
	opts := []string{"Do it | cheap (Recommended)", "Skip | keep"}
	store := func(boss string) todo.Store {
		s, err := todo.ForPane(c, tmux.SessionTarget(boss))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b, gone := store("boss-a"), store("boss-b"), store("boss-gone")
	id, err := a.Add(todo.Item{Text: "Answered?", Options: opts}, now)
	must(err)
	_, err = a.Answer(id, "Answered?", "1", 0, now)
	must(err)
	_, err = a.Decide(todo.Item{Text: "Decided?", Options: opts}, "easy", now.Add(-3*24*time.Hour))
	must(err)
	_, err = a.Add(todo.Item{Text: "Open?", Options: opts}, now)
	must(err)
	id, err = b.Add(todo.Item{Text: "Marked?", Options: opts, Obvious: "known"}, now)
	must(err)
	_, err = b.Settle(id, "Marked?", "1", "", func(todo.Item) bool { return false }, now)
	must(err)
	_, err = gone.Add(todo.Item{Text: "Closed answered?", Options: opts}, now)
	must(err)
	_, err = gone.Answer(1, "Closed answered?", "1", 0, now)
	must(err)
	_, err = gone.Add(todo.Item{Text: "Closed open?", Options: opts}, now)
	must(err)

	var out bytes.Buffer
	must(printTally(&out, io.Discard, c, 7, true, now))
	var got tallyOut
	must(json.Unmarshal(out.Bytes(), &got))
	byBoss := map[string]todo.Count{}
	for _, r := range got.Lists {
		byBoss[r.Boss] = r.Count
	}
	want := map[string]todo.Count{
		"boss-a":    {Answered: 1, BossSettled: 1, Open: 1},
		"boss-b":    {ManagerMarked: 1},
		"boss-gone": {Answered: 1, Open: 1},
	}
	if len(byBoss) != len(want) {
		t.Errorf("lists = %+v", got.Lists)
	}
	for boss, w := range want {
		if byBoss[boss] != w {
			t.Errorf("%s = %+v, want %+v", boss, byBoss[boss], w)
		}
	}
	if got.Total != (todo.Count{Answered: 2, BossSettled: 1, ManagerMarked: 1, Open: 2}) || got.Days != 7 || got.RecordedSince == 0 {
		t.Errorf("total = %+v, days %d, recorded since %d", got.Total, got.Days, got.RecordedSince)
	}

	// A window of 2 days leaves out boss-a's decision of 3 days ago
	out.Reset()
	must(printTally(&out, io.Discard, c, 2, true, now))
	must(json.Unmarshal(out.Bytes(), &got))
	if got.Total.BossSettled != 0 || got.Total.Answered != 2 {
		t.Errorf("2 days: total = %+v", got.Total)
	}

	out.Reset()
	must(printTally(&out, io.Discard, c, 7, false, now))
	text := out.String()
	for _, want := range []string{"counted from", "list  ", "overruled manager unmarked", "boss-gone", "total"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if row := strings.Fields(text[strings.Index(text, "boss-b"):]); strings.Join(row[:9], " ") != "boss-b 0 0 1 0 0 0 0 0" {
		t.Errorf("boss-b row = %v", row[:9])
	}
	if lines := strings.Split(strings.TrimSpace(text), "\n"); !strings.HasPrefix(lines[len(lines)-1], "total") {
		t.Errorf("last line is not the total:\n%s", text)
	}
}

// Before any event the output says counts start at the release, and the open asks are still counted.
func TestPrintTallyNoLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) { return "", nil })
	var out bytes.Buffer
	if err := printTally(&out, io.Discard, c, 7, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no events recorded yet, counts start at the release that ships this") {
		t.Errorf("text:\n%s", out.String())
	}
}

// --days under 1 and --boss are refused.
func TestTallyFlags(t *testing.T) {
	for _, args := range [][]string{{"--days", "0"}, {"--boss", "boss-a"}} {
		cmd := newTodoCmd()
		cmd.SetArgs(append([]string{"tally"}, args...))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v passed", args)
		}
	}
}
