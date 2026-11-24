package todo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var obviousOptions = []string{"Re-run the job | known flake (Recommended)", "Look into it first | a worker reads the log"}

// The manager settles an ask its boss marked obvious with its recommended option: ticked, answered, decided by the
// manager with what it was picked over, why and when; shown so on the list and among the finished items.
func TestSettle(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Add(Item{Text: "Re-run CI on #301?", Options: obviousOptions, Obvious: "known flake, 2 of 10 runs"}, now)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Settle(id, "Re-run CI on #301?", "1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := &Decided{By: DecidedManager, Pick: obviousOptions[0], Over: obviousOptions[1:], Why: "known flake, 2 of 10 runs", At: now.Add(time.Minute).Unix()}
	if it.Done == 0 || it.Answer != "Re-run the job" || it.Via != ViaManager || it.DoneBy != DecidedManager || !reflect.DeepEqual(it.Decided, want) {
		t.Fatalf("settled = %+v, decided %+v", it, it.Decided)
	}
	if l := read(t, s, now.Add(time.Minute)); !l.Items[0].Done || !reflect.DeepEqual(l.Items[0].Decided, want) {
		t.Errorf("shown = %+v", l.Items[0])
	}
	l := read(t, s, now.Add(time.Hour))
	if len(l.Finished) != 1 || !reflect.DeepEqual(l.Finished[0].Decided, want) {
		t.Errorf("finished = %+v", l.Finished)
	}
	// Its number given to a new item since: refused as settled, naming who decided
	if _, err := s.Add(Item{Text: "next"}, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(id, "Re-run CI on #301?", "1", now.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "decided for the user by the manager") {
		t.Errorf("settle of a renumbered item: %v", err)
	}
	// Settled again or answered by the user once it left the list: refused, naming who decided
	if _, err := s.Settle(id, "Re-run CI on #301?", "1", now.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "decided for the user by the manager") {
		t.Errorf("settle after it left the list: %v", err)
	}
	if _, err := s.Answer(id, "Re-run CI on #301?", "2", 0, now.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "decided for the user by the manager") {
		t.Errorf("answer after settle: %v", err)
	}
}

// Settle refuses, changing nothing, an unmarked ask, a plain item, one on a worker, a changed text and one settled
// already; decide refuses an ask without exactly one recommended option.
func TestSettleRefused(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	add := func(it Item) int {
		t.Helper()
		id, err := s.Add(it, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	unmarked := add(Item{Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}})
	plain := add(Item{Text: "Review the plan"})
	worker := add(Item{Text: "Pick a port?", Options: obviousOptions, Obvious: "only one free", On: "dev-x"})
	marked := add(Item{Text: "Re-run CI?", Options: obviousOptions, Obvious: "known flake"})
	if _, err := s.Settle(marked, "Re-run CI?", "1", now); err != nil {
		t.Fatal(err)
	}
	// The user answered a marked ask first: the manager's settle is refused, naming their answer
	answered := add(Item{Text: "Bump the dep?", Options: obviousOptions, Obvious: "patch release"})
	if _, err := s.Answer(answered, "Bump the dep?", "Look into it first", 0, now); err != nil {
		t.Fatal(err)
	}
	if it, err := s.Settle(answered, "Bump the dep?", "1", now); err == nil || !strings.Contains(err.Error(), `the user answered "Look into it first"`) || it.Decided != nil {
		t.Errorf("settle after the user's answer: %v", err)
	}
	for _, tt := range []struct {
		name     string
		id       int
		question string
		want     string
	}{
		{"unmarked", unmarked, "Merge #148?", "isn't marked obvious by its boss"},
		{"plain", plain, "Review the plan", "is no question"},
		{"on a worker", worker, "Pick a port?", "is on dev-x"},
		{"changed text", unmarked, "Merge #149?", `is "Merge #148?" now`},
		{"no text", unmarked, "", "needs its text"},
		{"settled already", marked, "Re-run CI?", "decided for the user by the manager"},
		{"no item", 9, "Merge #148?", "no open item 9"},
	} {
		if _, err := s.Settle(tt.id, tt.question, "1", now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	// The manager names the pick it checked: refused once the boss made another one the recommended option
	repicked := add(Item{Text: "Retry it?", Options: obviousOptions, Obvious: "flake"})
	if _, err := s.ReplaceAsk(repicked, Item{Options: []string{"Re-run the job", "Look into it first (Recommended)"}, Obvious: "flake"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(repicked, "Retry it?", "Re-run the job", now); err == nil || !strings.Contains(err.Error(), `recommended option is "Look into it first" now, not "Re-run the job"`) {
		t.Errorf("settle of a changed pick: %v", err)
	}
	if l := read(t, s, now); l.Items[len(l.Items)-1].Done || l.Items[len(l.Items)-1].Decided != nil {
		t.Errorf("a refused settle of a changed pick wrote: %+v", l.Items[len(l.Items)-1])
	}
	if l := read(t, s, now); l.Items[0].Done || l.Items[0].Decided != nil {
		t.Errorf("a refused settle changed the unmarked ask: %+v", l.Items[0])
	}
	for _, options := range [][]string{{"A", "B"}, {"A (Recommended)", "B (Recommended)"}} {
		it := Item{ID: 1, Options: options}
		if err := it.decide(DecidedManager, DecidedManager, "why", now.Unix()); err == nil || !strings.Contains(err.Error(), "no one recommended option") {
			t.Errorf("decide %q: %v", options, err)
		}
	}
}

// An obvious ask needs exactly one recommended option and a one-line why, at add and at a replace that changes its
// options; a replace keeps the mark.
func TestObviousChecked(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	for _, tt := range []struct {
		options []string
		why     string
		want    string
	}{
		{[]string{"A", "B"}, "why", "exactly one option marked \"(Recommended)\", it has 0"},
		{[]string{"A (Recommended)", "B (Recommended)"}, "why", "it has 2"},
		{obviousOptions, strings.Repeat("x", MaxResult+1), "the why of --obvious or --decided is 301 characters"},
	} {
		if _, err := s.Add(Item{Text: "Q?", Options: tt.options, Obvious: tt.why}, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("add %q: %v, want %q", tt.options, err, tt.want)
		}
	}
	id, err := s.Add(Item{Text: "Q?", Options: obviousOptions, Obvious: "why"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceAsk(id, Item{Options: []string{"A", "B"}, Obvious: "why"}, now); err == nil || !strings.Contains(err.Error(), "it has 0") {
		t.Errorf("replace re-marking options with no recommended one: %v", err)
	}
	if _, err := s.ReplaceAsk(id, Item{Context: []string{"more"}}, now); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now); l.Items[0].Obvious != "why" {
		t.Errorf("replace keeping question and pick dropped the mark: %+v", l.Items[0])
	}
	// - removes the mark in place
	if _, err := s.ReplaceAsk(id, Item{Obvious: NotObvious}, now); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now); l.Items[0].Obvious != "" {
		t.Errorf("--obvious - kept the mark: %+v", l.Items[0])
	}
	// A new question or recommended option drops a mark not given again: it was for the old one
	for _, replace := range []Item{{Text: "Other Q?"}, {Options: []string{"A", "B (Recommended)"}}, {Options: []string{obviousOptions[0], "Wait a day"}}} {
		id, err := s.Add(Item{Text: "Q?", Options: obviousOptions, Obvious: "why"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReplaceAsk(id, replace, now); err != nil {
			t.Fatal(err)
		}
		l := read(t, s, now)
		if it := l.Items[len(l.Items)-1]; it.Obvious != "" {
			t.Errorf("replace %+v kept the mark: %+v", replace, it)
		}
	}
}

// The boss decides an easy ask itself: on the list ticked at once with its recommended option, decided by the boss;
// refused without exactly one recommended option or a why.
func TestDecide(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Decide(Item{Text: "Fix the parser first?", Options: []string{"Parser first | unblocks the export (Recommended)", "Export first"}, For: 3}, "the export test needs it", now)
	if err != nil {
		t.Fatal(err)
	}
	l := read(t, s, now)
	it := l.Items[0]
	want := &Decided{By: DecidedBoss, Pick: "Parser first | unblocks the export (Recommended)", Over: []string{"Export first"}, Why: "the export test needs it", At: now.Unix()}
	if it.ID != id || !it.Done || it.Answer != "Parser first" || it.DoneBy != Boss || it.For != 0 || !reflect.DeepEqual(it.Decided, want) {
		t.Errorf("decided = %+v, %+v", it, it.Decided)
	}
	for _, tt := range []struct {
		options []string
		why     string
		want    string
	}{
		{[]string{"A", "B"}, "why", "it has 0"},
		{obviousOptions, "", "needs why it is easy"},
	} {
		if _, err := s.Decide(Item{Text: "Q?", Options: tt.options}, tt.why, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("decide %q %q: %v, want %q", tt.options, tt.why, err, tt.want)
		}
	}
}

// Reopened, a settled ask is the user's again: no mark for the manager to settle it anew, no decision kept.
func TestReopenSettled(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Add(Item{Text: "Re-run CI?", Options: obviousOptions, Obvious: "known flake"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(id, "Re-run CI?", "1", now); err != nil {
		t.Fatal(err)
	}
	it, err := s.Reopen(id, "Re-run CI?", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if it.Obvious != "" || it.Decided != nil || it.Answer != "" || it.Done != 0 {
		t.Errorf("reopened = %+v", it)
	}
	_, err = s.Settle(id, "Re-run CI?", "1", now.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "isn't marked obvious") {
		t.Errorf("settle after reopen: %v", err)
	}
	var a Answered
	if errors.As(err, &a) {
		t.Errorf("settle after reopen is no Answered: %v", err)
	}
}
