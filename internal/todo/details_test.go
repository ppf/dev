package todo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// Details are stored by add, kept by a --replaces that gives none, replaced by one that does; over MaxDetails is refused.
func TestDetails(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	id, err := s.Add(Item{Text: "Send the reminder", Details: "Second reminder.\n\n- attach the PDF"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(id, Item{Text: "Send the March reminder"}, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, now).Items[0].Details; got != "Second reminder.\n\n- attach the PDF" {
		t.Errorf("after --replaces without details: %q", got)
	}
	if err := s.Replace(id, Item{Details: "Third reminder."}, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, now).Items[0].Details; got != "Third reminder." {
		t.Errorf("after --replaces with details: %q", got)
	}
	long := strings.Repeat("é", MaxDetails+1)
	if _, err := s.Add(Item{Text: "x", Details: long}, now); err == nil || !strings.Contains(err.Error(), "at most 1500") {
		t.Errorf("Add over MaxDetails: %v", err)
	}
	if err := s.Replace(id, Item{Details: long}, now); err == nil {
		t.Error("Replace over MaxDetails: no error")
	}
	if _, err := s.Add(Item{Text: "x", Details: strings.Repeat("é", MaxDetails)}, now); err != nil {
		t.Errorf("Add at MaxDetails: %v", err)
	}
}

func TestCleanDetails(t *testing.T) {
	for in, want := range map[string]string{
		"one": "one",
		"\n\n  first  para \r\n\r\n\r\n- a\t b\n- c\n\n": "  first para\n\n- a b\n- c",
		"- step\n  - sub  a\n\t- sub b":                  "- step\n  - sub a\n - sub b",
		"x\x07y\n \n \nz":                                "x y\n\nz",
		"\n \n":                                          "",
	} {
		if got := CleanDetails(in); got != want {
			t.Errorf("CleanDetails(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckResult(t *testing.T) {
	if got, err := CheckResult("  mail sent\nto x  "); got != "mail sent to x" || err != nil {
		t.Errorf("CheckResult = %q, %v", got, err)
	}
	if _, err := CheckResult(" \n"); err == nil {
		t.Error("empty result: no error")
	}
	if _, err := CheckResult(strings.Repeat("x", MaxResult+1)); err == nil {
		t.Error("result over MaxResult: no error")
	}
	if _, err := CheckResult(strings.Repeat("ł", MaxResult)); err != nil {
		t.Errorf("result at MaxResult: %v", err)
	}
}

// A tick says who ticked it: its boss by name (boss for a desktop boss's), the user from a board or an answer, an
// --until or --done-on by itself; a result goes on the one item ticked, kept once it left the list with its context and
// details; Reopen drops both.
func TestDoneByAndResult(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := tmuxStore(t, "boss-vitay")
	now := time.Unix(1700000000, 0)
	add := func(it Item) int {
		t.Helper()
		id, err := s.Add(it, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mail := add(Item{Text: "Send the reminder", Context: []string{"template: Mahnung 2"}, Details: "Second reminder."})
	mine := add(Item{Text: "Call the bank"})
	ask := add(Item{Text: "Send it now?", Options: []string{"Yes", "No"}})
	until := add(Item{Text: "Wait for the reply", Until: now.Add(time.Minute).Unix()})
	two := add(Item{Text: "Two"})

	if err := s.WithResult("x").Done([]int{two, mine}, now); err == nil {
		t.Error("a result on two items: no error")
	}
	if err := s.WithResult("Mail sent 12:40 to accounts@example.com").Done([]int{mail}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DoneText(mine, "Call the bank", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(ask, "Send it now?", "Yes", 0, now); err != nil {
		t.Fatal(err)
	}
	l := read(t, s, now)
	by := map[string]Shown{}
	for _, it := range l.Items {
		by[it.Text] = it
	}
	if it := by["Send the reminder"]; it.DoneBy != "boss-vitay" || it.Result != "Mail sent 12:40 to accounts@example.com" || it.Details != "Second reminder." {
		t.Errorf("boss's tick = %+v", it)
	}
	if it := by["Call the bank"]; it.DoneBy != DoneByYou || it.Result != "" {
		t.Errorf("board's tick = %+v", it)
	}
	if it := by["Send it now?"]; it.DoneBy != DoneByYou {
		t.Errorf("answer's tick = %+v", it)
	}

	later := now.Add(2 * time.Minute)
	l = read(t, s, later)
	var fin Finished
	for _, f := range l.Finished {
		if f.ID == mail {
			fin = f
		}
		if f.ID == until && f.DoneBy != DoneByUntil {
			t.Errorf("--until tick = %+v", f)
		}
	}
	if fin.Result == "" || fin.DoneBy != "boss-vitay" || fin.Details != "Second reminder." || len(fin.Context) != 1 {
		t.Errorf("finished = %+v, want its result, done_by, details and context", fin)
	}
	data, err := json.Marshal(fin)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"context":["template: Mahnung 2"]`, `"details":"Second reminder."`, `"result":"Mail sent`, `"done_by":"boss-vitay"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("finished JSON lacks %s: %s", want, data)
		}
	}

	if _, err := s.Reopen(mine, "Call the bank", later); err != nil {
		t.Fatal(err)
	}
	if it, ok := openItem(t, s, mine); !ok || it.DoneBy != "" || it.Result != "" {
		t.Errorf("reopened = %+v, %v; want no done_by, no result", it, ok)
	}
}

// A --done-on tick is by DoneByPR.
func TestDoneByPR(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "on merge", Link: prURL, DoneOn: OnMerged}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED"}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, t0).Items[0]; !it.Done || it.DoneBy != DoneByPR {
		t.Errorf("item = %+v, want done by %q", it, DoneByPR)
	}
}

// A desktop app boss's own tick is by Boss: its list has no name.
func TestDoneByDesktopBoss(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	id, err := s.Add(Item{Text: "x"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{id}, now); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, now).Items[0]; it.DoneBy != Boss {
		t.Errorf("done_by = %q, want %q", it.DoneBy, Boss)
	}
}

// A list and finished items written before details, results and done_by read as they did.
func TestOlderFilesRead(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if err := os.MkdirAll(filepath.Dir(s.donePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	list := `{"items":[{"id":1,"text":"Open one","context":["a line"],"added":1699999990},{"id":2,"text":"Ticked","done":1699999999,"added":1699999990}],"updated":1699999999,"next":3}`
	done := `[{"id":7,"text":"Gone","context":["kept"],"done":1699999000,"added":1699990000}]`
	if err := os.WriteFile(s.path, []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.donePath(), []byte(done), 0o600); err != nil {
		t.Fatal(err)
	}
	l := read(t, s, now)
	if len(l.Items) != 2 || l.Items[0].Text != "Open one" || l.Items[0].Context[0] != "a line" || l.Items[0].Details != "" || !l.Items[1].Done || l.Items[1].DoneBy != "" {
		t.Errorf("items = %+v", l.Items)
	}
	if len(l.Finished) != 1 || l.Finished[0].Text != "Gone" || l.Finished[0].Context[0] != "kept" || l.Finished[0].Result != "" || l.Finished[0].DoneBy != "" {
		t.Errorf("finished = %+v", l.Finished)
	}
}
