package todo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var obviousOptions = []string{"Re-run the job | known flake (Recommended)", "Look into it first | a worker reads the log"}

func noHandOn(Item) bool { return false }

// The manager settles an ask its boss didn't mark only with a why: decided by the manager for that why, the user
// overrules it as any settled ask; refused without a why, a too long one, for no one recommended option, one option, a
// hand-on question (marked too, before any why), a why on a marked ask and an answered item, changing nothing.
func TestSettleUnmarked(t *testing.T) {
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
	handOn := func(it Item) bool { return it.Text == "Hand on to boss-dev?" }
	ask := add(Item{Text: "Rebuild the dev server?", Options: obviousOptions})
	none := add(Item{Text: "Which port?", Options: []string{"8080", "9090"}})
	gate := add(Item{Text: "Hand on to boss-dev?", Options: []string{"Hand it on (Recommended)", "Drop it"}})
	markedGate := add(Item{Text: "Hand on to boss-dev?", Options: []string{"Hand it on (Recommended)", "Drop it"}, Obvious: "agreed"})
	marked := add(Item{Text: "Re-run CI?", Options: obviousOptions, Obvious: "known flake"})
	two := add(Item{Text: "Which file?", Options: []string{"One (Recommended)", "Two (Recommended)"}})
	lone := add(Item{Text: "Rebuild it?", Options: []string{"Rebuild (Recommended)"}})
	answered := add(Item{Text: "Keep the draft?", Options: obviousOptions})
	if _, err := s.Answer(answered, "Keep the draft?", "2", 0, now); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, question, why string
		id                  int
		want                string
	}{
		{"no why", "Rebuild the dev server?", "", ask, "settle it with --why"},
		{"long why", "Rebuild the dev server?", strings.Repeat("x", MaxResult+1), ask, "--why is"},
		{"no recommended option", "Which port?", "local only", none, "no one recommended option"},
		{"two recommended options", "Which file?", "local only", two, "no one recommended option"},
		{"one option", "Rebuild it?", "local only", lone, "none to overrule it with"},
		{"hand-on without a why", "Hand on to boss-dev?", "", gate, "is a hand-on question"},
		{"hand-on", "Hand on to boss-dev?", "the user said so", gate, "is a hand-on question"},
		{"marked hand-on", "Hand on to boss-dev?", "", markedGate, "is a hand-on question"},
		{"marked with a why", "Re-run CI?", "cheap", marked, "leave out --why"},
		{"answered", "Keep the draft?", "reversible", answered, `the user answered "Look into it first"`},
	} {
		if _, err := s.Settle(tt.id, tt.question, "1", tt.why, handOn, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	for _, it := range read(t, s, now).Items {
		if it.Decided != nil || (it.Done && it.ID != answered) {
			t.Errorf("a refused settle wrote: %+v", it)
		}
	}

	it, err := s.Settle(ask, "Rebuild the dev server?", "Re-run the job", "local, rebuilt in a minute", handOn, now)
	if err != nil {
		t.Fatal(err)
	}
	want := &Decided{By: DecidedManager, Pick: obviousOptions[0], Over: obviousOptions[1:], Why: "local, rebuilt in a minute", At: now.Unix(), Unmarked: true}
	if it.Answer != "Re-run the job" || it.Via != ViaManager || !reflect.DeepEqual(it.Decided, want) {
		t.Fatalf("settled = %+v, decided %+v", it, it.Decided)
	}
	over, was, err := s.Overrule(ask, "Rebuild the dev server?", "2", "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if over.Answer != "Look into it first" || over.Decided != nil || was.Why != "local, rebuilt in a minute" {
		t.Errorf("overruled = %+v, was %+v", over, was)
	}
}

// The manager settles an ask its boss marked obvious with its recommended option: ticked, answered, decided by the
// manager with what it was picked over, why and when; shown so on the list and among the finished items.
func TestSettle(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Add(Item{Text: "Re-run CI on #301?", Options: obviousOptions, Obvious: "known flake, 2 of 10 runs"}, now)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Settle(id, "Re-run CI on #301?", "1", "", noHandOn, now.Add(time.Minute))
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
	if _, err := s.Settle(id, "Re-run CI on #301?", "1", "", noHandOn, now.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "decided for the user by the manager") {
		t.Errorf("settle of a renumbered item: %v", err)
	}
	// Settled again or answered by the user once it left the list: refused, naming who decided
	if _, err := s.Settle(id, "Re-run CI on #301?", "1", "", noHandOn, now.Add(2*time.Hour)); err == nil || !strings.Contains(err.Error(), "decided for the user by the manager") {
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
	if _, err := s.Settle(marked, "Re-run CI?", "1", "", noHandOn, now); err != nil {
		t.Fatal(err)
	}
	// The user answered a marked ask first: the manager's settle is refused, naming their answer
	answered := add(Item{Text: "Bump the dep?", Options: obviousOptions, Obvious: "patch release"})
	if _, err := s.Answer(answered, "Bump the dep?", "Look into it first", 0, now); err != nil {
		t.Fatal(err)
	}
	if it, err := s.Settle(answered, "Bump the dep?", "1", "", noHandOn, now); err == nil || !strings.Contains(err.Error(), `the user answered "Look into it first"`) || it.Decided != nil {
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
		if _, err := s.Settle(tt.id, tt.question, "1", "", noHandOn, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	// The manager names the pick it checked: refused once the boss made another one the recommended option
	repicked := add(Item{Text: "Retry it?", Options: obviousOptions, Obvious: "flake"})
	if _, err := s.ReplaceAsk(repicked, Item{Options: []string{"Re-run the job", "Look into it first (Recommended)"}, Obvious: "flake"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(repicked, "Retry it?", "Re-run the job", "", noHandOn, now); err == nil || !strings.Contains(err.Error(), `recommended option is "Look into it first" now, not "Re-run the job"`) {
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
	if _, err := s.Settle(id, "Re-run CI?", "1", "", noHandOn, now); err != nil {
		t.Fatal(err)
	}
	it, err := s.Reopen(id, "Re-run CI?", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if it.Obvious != "" || it.Decided != nil || it.Answer != "" || it.Done != 0 {
		t.Errorf("reopened = %+v", it)
	}
	_, err = s.Settle(id, "Re-run CI?", "1", "", noHandOn, now.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "isn't marked obvious") {
		t.Errorf("settle after reopen: %v", err)
	}
	var a Answered
	if errors.As(err, &a) {
		t.Errorf("settle after reopen is no Answered: %v", err)
	}
}

// Overruled, an ask settled for the user is answered with their pick among the others in one write: ticked by the user,
// no longer decided; from the list and once it left it, for a day.
func TestOverrule(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Add(Item{Text: "Re-run CI?", Options: obviousOptions, Obvious: "known flake"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(id, "Re-run CI?", "1", "", noHandOn, now); err != nil {
		t.Fatal(err)
	}
	it, was, err := s.Overrule(id, "Re-run CI?", "look into it first", ViaPage, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != id || it.Done != now.Add(time.Minute).Unix() || it.DoneBy != DoneByYou || it.Answer != "Look into it first" || it.Via != ViaPage ||
		it.Decided != nil || it.Obvious != "" || was.By != DecidedManager || was.Pick != obviousOptions[0] {
		t.Fatalf("overruled = %+v, was %+v", it, was)
	}
	l := read(t, s, now.Add(time.Minute))
	if len(l.Items) != 1 || !l.Items[0].Done || l.Items[0].Decided != nil || l.Items[0].Answer != "Look into it first" {
		t.Errorf("shown = %+v", l.Items)
	}

	// A boss's own decision, overruled once it left the list for the finished items
	id, err = s.Decide(Item{Text: "Parser first?", Options: []string{"Parser first (Recommended)", "Export first"}}, "unblocks", now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(3 * time.Hour)
	if l := read(t, s, later); len(l.Finished) != 2 {
		t.Fatalf("finished = %+v", l.Finished)
	}
	if it, was, err = s.Overrule(id, "Parser first?", "2", "", later); err != nil || it.Answer != "Export first" || was.By != DecidedBoss {
		t.Fatalf("overrule of a finished item = %+v, %+v, %v", it, was, err)
	}
	if l := read(t, s, later); len(l.Finished) != 1 || len(l.Items) != 1 || l.Items[0].Decided != nil {
		t.Errorf("after: items %+v, finished %+v", l.Items, l.Finished)
	}
}

// Options named by numbers: a label names its option before a number reads as a position, as Pick reads them.
func TestOverruleNumberLabels(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	id, err := s.Decide(Item{Text: "How many replicas?", Options: []string{"3 (Recommended)", "2", "1"}}, "the default", now)
	if err != nil {
		t.Fatal(err)
	}
	// The decided pick's own label is refused, not read as option 2's number
	id2, err := s.Decide(Item{Text: "Shards?", Options: []string{"2 (Recommended)", "Wait"}}, "the default", now)
	if err != nil {
		t.Fatal(err)
	}
	if it, _, err := s.Overrule(id2, "Shards?", "2", "", now); err == nil {
		t.Errorf("overrule with the decided label 2 = %q", it.Answer)
	}
	it, _, err := s.Overrule(id, "How many replicas?", "1", "", now)
	if err != nil || it.Answer != "1" {
		t.Errorf("overrule with label 1 = %q, %v", it.Answer, err)
	}
}

// Overrule refuses, changing nothing: a pick not among the others (the decided pick itself, own words), an item not
// decided for the user, and one ticked too long ago to reopen.
func TestOverruleRefused(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	settled, err := s.Add(Item{Text: "Re-run CI?", Options: obviousOptions, Obvious: "known flake"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(settled, "Re-run CI?", "1", "", noHandOn, now); err != nil {
		t.Fatal(err)
	}
	answered, err := s.Add(Item{Text: "Merge?", Options: []string{"Merge", "Wait"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(answered, "Merge?", "Merge", 0, now); err != nil {
		t.Fatal(err)
	}
	before := read(t, s, now)
	for _, tt := range []struct {
		name string
		id   int
		text string
		pick string
		at   time.Time
		want string
	}{
		{"the decided pick", settled, "Re-run CI?", "Re-run the job", now, `other options are "Look into it first", not "Re-run the job"`},
		{"the decided pick's number", settled, "Re-run CI?", "1", now, `not "1"`},
		{"own words", settled, "Re-run CI?", "do both", now, `not "do both"`},
		{"not decided", answered, "Merge?", "Wait", now, "wasn't decided for the user"},
		{"too old to reopen", settled, "Re-run CI?", "Look into it first", now.Add(25 * time.Hour), "no ticked item"},
	} {
		_, _, err := s.Overrule(tt.id, tt.text, tt.pick, "", tt.at)
		if err == nil || !strings.Contains(err.Error(), tt.want) || !errors.As(err, new(Refused)) {
			t.Errorf("%s: %v, want a refusal %q", tt.name, err, tt.want)
		}
	}
	if after := read(t, s, now); !reflect.DeepEqual(after.Items, before.Items) {
		t.Errorf("refusals changed the list: %+v, was %+v", after.Items, before.Items)
	}
}
