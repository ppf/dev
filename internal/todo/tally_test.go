package todo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var tallyOptions = []string{"Rebuild | local only (Recommended)", "Skip | keep it"}

// Each way an ask ends is one column: answered by the user, settled by the boss, by the manager (marked, unmarked),
// overruled of each of the three kinds; an ask still open counts as open. A test item, a refusal and a trial write
// nothing; events older than the window are left out of it.
func TestTally(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	ask := func(text string, obvious string, at time.Time) int {
		t.Helper()
		id, err := s.Add(Item{Text: text, Options: tallyOptions, Obvious: obvious}, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// A week and a day back: out of a 7 day window, in a 9 day one
	old := now.Add(-8 * 24 * time.Hour)
	_, err := s.Answer(ask("Old?", "", old), "Old?", "1", 0, old)
	must(err)

	_, err = s.Answer(ask("Answered?", "", now), "Answered?", "1", 0, now)
	must(err)
	_, err = s.Decide(Item{Text: "Boss settled?", Options: tallyOptions}, "easy", now)
	must(err)
	_, err = s.Settle(ask("Marked?", "known", now), "Marked?", "1", "", noHandOn, now)
	must(err)
	_, err = s.Settle(ask("Unmarked?", "", now), "Unmarked?", "1", "cheap", noHandOn, now)
	must(err)

	// Overruled, one of each kind
	id, err := s.Decide(Item{Text: "Boss overruled?", Options: tallyOptions}, "easy", now)
	must(err)
	_, _, err = s.Overrule(id, "Boss overruled?", "2", "", now)
	must(err)
	id = ask("Marked overruled?", "known", now)
	_, err = s.Settle(id, "Marked overruled?", "1", "", noHandOn, now)
	must(err)
	_, _, err = s.Overrule(id, "Marked overruled?", "2", "", now)
	must(err)
	id = ask("Unmarked overruled?", "", now)
	_, err = s.Settle(id, "Unmarked overruled?", "1", "cheap", noHandOn, now)
	must(err)
	_, _, err = s.Overrule(id, "Unmarked overruled?", "2", "", now)
	must(err)

	// Nothing: a refusal, a test item, a trial store
	if _, err := s.Settle(ask("Refused?", "", now), "Refused?", "1", "", noHandOn, now); err == nil {
		t.Fatal("unmarked settle without a why passed")
	}
	quiet, err := s.Add(Item{Text: "Test?", Options: tallyOptions, Quiet: true}, now)
	must(err)
	_, err = s.Answer(quiet, "Test?", "1", 0, now)
	must(err)
	trial := s.Trial()
	_, err = trial.Answer(ask("Tried?", "", now), "Tried?", "1", 0, now)
	must(err)
	ask("Still open?", "", now)

	events, err := ReadEvents()
	must(err)
	l, err := s.Read(now)
	must(err)
	open := map[string]int{s.name: l.OpenAsks(s.boss)}
	want := Count{Answered: 1, BossSettled: 2, ManagerMarked: 2, ManagerUnmarked: 2, OverruledBoss: 1, OverruledMarked: 1, OverruledUnmark: 1, Open: 3}
	for _, tt := range []struct {
		name  string
		since time.Time
		want  Count
	}{
		{"7 days", now.Add(-7 * 24 * time.Hour), want},
		{"9 days", now.Add(-9 * 24 * time.Hour), func() Count { c := want; c.Answered++; return c }()},
		{"after all", now.Add(time.Second), Count{Open: 3}},
	} {
		rows, total := Tally(events, open, tt.since.Unix())
		if len(rows) != 1 || rows[0].Boss != s.name || !reflect.DeepEqual(rows[0].Count, tt.want) || !reflect.DeepEqual(total, tt.want) {
			t.Errorf("%s: rows %+v, total %+v, want %+v", tt.name, rows, total, tt.want)
		}
	}
}

// Open counts the user's open asks of the list, not a ticked one, a plain item, a worker's or a test ask.
func TestOpenAsks(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{
		{Text: "Ask?", Options: tallyOptions},
		{Text: "Worker's?", Options: tallyOptions, On: "someone"},
		{Text: "Test?", Options: tallyOptions, Quiet: true},
		{Text: "Plain"},
	} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.Add(Item{Text: "Answered?", Options: tallyOptions}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(id, "Answered?", "1", 0, now); err != nil {
		t.Fatal(err)
	}
	l, err := s.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	if n := l.OpenAsks(s.boss); n != 1 {
		t.Errorf("open asks = %d, want 1", n)
	}
}

// A log line that isn't JSON is an error naming it, never skipped; no log is no events.
func TestReadEventsBad(t *testing.T) {
	store(t)
	if events, err := ReadEvents(); err != nil || events != nil {
		t.Fatalf("no log: %v, %v", events, err)
	}
	path, err := tallyPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"at\":1}\nnope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, want line 2", err)
	}
}

// An overrule refused, or a write the log can't take, leaves the log and the list as they were.
func TestTallyRefusalsWriteNothing(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Add(Item{Text: "Plain?", Options: tallyOptions}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Overrule(id, "Plain?", "2", "", now); err == nil {
		t.Fatal("overrule of an ask not decided passed")
	}
	if events, err := ReadEvents(); err != nil || len(events) != 0 {
		t.Fatalf("events after a refused overrule: %v, %v", events, err)
	}
	// A directory where the log goes: the answer is refused, the ask stays open
	path, err := tallyPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(id, "Plain?", "1", 0, now); err == nil || !strings.Contains(err.Error(), "tally not written") {
		t.Fatalf("answer with an unwritable log: %v", err)
	}
	l, err := s.Read(now)
	if err != nil || len(l.Items) != 1 || l.Items[0].Done {
		t.Errorf("list after the refused answer: %+v, %v", l.Items, err)
	}
}

// A decision stored before the unmarked flag existed was a marked one.
func TestDecidedKind(t *testing.T) {
	for _, tt := range []struct {
		d    Decided
		want string
	}{
		{Decided{By: DecidedBoss}, TallyBoss},
		{Decided{By: DecidedManager}, TallyManagerMarked},
		{Decided{By: DecidedManager, Unmarked: true}, TallyManagerUnmarked},
	} {
		if got := tt.d.kind(); got != tt.want {
			t.Errorf("%+v: %s, want %s", tt.d, got, tt.want)
		}
	}
}
