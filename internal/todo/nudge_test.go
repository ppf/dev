package todo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

// texts is what nudges or ticks say, one line each.
func texts[T interface{ String() string }](ts []T) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.String())
	}
	return out
}

// nudges is s's Nudges at now with workers live, failing the test on an error.
func nudges(t *testing.T, s Store, now time.Time, workers ...string) []string {
	t.Helper()
	ns, err := s.Nudges(now, func() ([]Session, error) {
		var ws []Session
		for _, w := range workers {
			ws = append(ws, Session{ID: w, Name: w})
		}
		return ws, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return texts(ns)
}

// Each move of an item's PR since its first lookup is told once: a PR already merged at it isn't; a replace starts afresh.
func TestNudgesPR(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	other := "https://github.com/acme/orbit/pull/418"
	for _, it := range []Item{{Text: "PR #412 open", On: "reviewers", Link: prURL}, {Text: "Deploy after #418", On: "Tomas", Link: other}} {
		if _, err := s.Add(it, t0); err != nil {
			t.Fatal(err)
		}
	}
	p := &prs{states: map[string]sidebar.PRView{
		prURL: {State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIRunning},
		other: {State: "MERGED", Ended: t0.Add(-time.Hour)},
	}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0); len(got) != 0 {
		t.Errorf("first lookup: %q, want none (merged before it: marked, not told)", got)
	}
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIFailed}
	t1 := t0.Add(LookupTTL)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	updated := read(t, s, t1).Updated
	want := []string{`#1 "PR #412 open" (on reviewers): PR #412 approved 0m ago`, `#1 "PR #412 open" (on reviewers): PR #412 checks failing 0m ago`}
	if got := nudges(t, s, t1); !slices.Equal(got, want) {
		t.Errorf("moved: %q, want %q", got, want)
	}
	if got := nudges(t, s, t1.Add(time.Minute)); len(got) != 0 {
		t.Errorf("again: %q, want none", got)
	}
	if got := read(t, s, t1).Updated; got != updated {
		t.Errorf("nudges changed Updated: %d, want %d", got, updated)
	}
	// Told survives the next lookup, which finds it merged: only the merge is new
	p.states[prURL] = sidebar.PRView{State: "MERGED", Review: "APPROVED", Checks: sidebar.CIFailed, Ended: t1.Add(time.Minute)}
	t2 := t1.Add(LookupTTL)
	if err := s.Lookup(t2, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := nudges(t, s, t2), []string{`#1 "PR #412 open" (on reviewers): PR #412 merged 9m ago`}; !slices.Equal(got, want) {
		t.Errorf("merged: %q, want %q", got, want)
	}
}

// A worker gone is told once; an item unchanged two days once, again only two days after that, and afresh once replaced.
func TestNudgesGoneUnchanged(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Rework rounding", On: "fix", Session: &Session{ID: "fix", Name: "fix"}}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Attach the DPA"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Ship?", Options: []string{"Yes", "No"}}, t0); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0, "fix"); len(got) != 0 {
		t.Errorf("worker live: %q, want none", got)
	}
	if got, want := nudges(t, s, t0.Add(time.Hour)), []string{`#1 "Rework rounding" (on fix): its session is gone`}; !slices.Equal(got, want) {
		t.Errorf("gone: %q, want %q", got, want)
	}
	day := 24 * time.Hour
	want := []string{`#2 "Attach the DPA": unchanged 2d: check it at its source`}
	if got := nudges(t, s, t0.Add(2*day)); !slices.Equal(got, append([]string{`#1 "Rework rounding" (on fix): unchanged 2d: check it at its source`}, want...)) {
		t.Errorf("2d: %q", got)
	}
	if got := nudges(t, s, t0.Add(3*day)); len(got) != 0 {
		t.Errorf("3d: %q, want none", got)
	}
	if err := s.Replace(1, Item{}, t0.Add(3*day)); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(4*day)); !slices.Equal(got, []string{`#2 "Attach the DPA": unchanged 4d: check it at its source`}) {
		t.Errorf("4d: %q, want only #2 (#1 replaced keeping its text: its worker gone not told again)", got)
	}
	if err := s.Replace(1, Item{Text: "Rework rounding again"}, t0.Add(4*day)); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(4*day)); !slices.Equal(got, []string{`#1 "Rework rounding again" (on fix): its session is gone`}) {
		t.Errorf("new text: %q, want its worker gone told", got)
	}
}

// An item waiting for a person is told unchanged once, "no action unless there is news", and not again after 2 more days.
func TestNudgesWaitingForOnce(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Review of the DPA", On: "Marek"}, t0); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	if got, want := nudges(t, s, t0.Add(2*day)), []string{`#1 "Review of the DPA" (on Marek): unchanged 2d: no action unless there is news`}; !slices.Equal(got, want) {
		t.Errorf("2d: %q, want %q", got, want)
	}
	if got := nudges(t, s, t0.Add(5*day)); len(got) != 0 {
		t.Errorf("5d: %q, want none", got)
	}
	if got, want := checked(t, s, 1, t0.Add(5*day)), "unchanged 5d: no action unless there is news"; got != want {
		t.Errorf("check = %q, want %q", got, want)
	}
	if err := s.Replace(1, Item{}, t0.Add(5*day)); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(8*day)); len(got) != 0 {
		t.Errorf("replaced keeping its text: %q, want none", got)
	}
	if err := s.Replace(1, Item{Text: "Review of the DPA, again"}, t0.Add(8*day)); err != nil {
		t.Fatal(err)
	}
	if got, want := nudges(t, s, t0.Add(10*day)), []string{`#1 "Review of the DPA, again" (on Marek): unchanged 2d: no action unless there is news`}; !slices.Equal(got, want) {
		t.Errorf("new text: %q, want %q", got, want)
	}
}

// --done-on ticks an item once a lookup finds its PR so, its first included, without changing Updated, and keeps the
// tick for its boss; a closed PR, another state, or none reached doesn't tick.
func TestDoneOn(t *testing.T) {
	cases := []struct {
		on   string
		view sidebar.PRView
		tick bool
	}{
		{OnApproved, sidebar.PRView{State: "OPEN", Review: "APPROVED"}, true},
		{OnApproved, sidebar.PRView{State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIPassed}, false},
		{OnApproved, sidebar.PRView{State: "CLOSED", Review: "APPROVED"}, false},
		{OnApproved, sidebar.PRView{State: "MERGED", Review: "APPROVED"}, true},
		{OnMerged, sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIPassed}, false},
		{OnMerged, sidebar.PRView{State: "MERGED"}, true},
		{OnGreen, sidebar.PRView{State: "OPEN", Checks: sidebar.CIRunning}, false},
		{OnGreen, sidebar.PRView{State: "OPEN", Checks: sidebar.CIPassed}, true},
	}
	for _, c := range cases {
		s := store(t)
		t0 := time.Unix(1_800_000_000, 0)
		if _, err := s.Add(Item{Text: "Second review of #412", On: "Ana", Link: prURL, DoneOn: c.on}, t0); err != nil {
			t.Fatal(err)
		}
		p := &prs{states: map[string]sidebar.PRView{prURL: c.view}}
		t1 := t0.Add(time.Minute)
		if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
			t.Fatal(err)
		}
		l := read(t, s, t1)
		if got := l.Items[0].Done; got != c.tick {
			t.Errorf("%s on %+v: ticked %v, want %v", c.on, c.view, got, c.tick)
		}
		if l.Updated != t0.Unix() {
			t.Errorf("%s: Updated %d, want the add's %d", c.on, l.Updated, t0.Unix())
		}
		ticked, err := s.TakeTicked(t1, func(t Ticked) bool { return t.PR != 0 }, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.tick != (len(ticked) == 1) {
			t.Errorf("%s: ticked %q", c.on, texts(ticked))
		}
	}
}

// --done-on needs a GitHub PR link and one of its states; a replace keeps it, and refuses dropping its link's PR.
func TestDoneOnRefused(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	for _, it := range []Item{
		{Text: "a", DoneOn: OnMerged},
		{Text: "a", DoneOn: OnMerged, Link: "https://gitlab.com/acme/orbit/-/merge_requests/7"},
		{Text: "a", DoneOn: "deployed", Link: prURL},
	} {
		if _, err := s.Add(it, now); err == nil {
			t.Errorf("%+v: added", it)
		}
	}
	if _, err := s.Add(Item{Text: "a", Link: prURL, DoneOn: OnApproved}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(1, Item{Text: "b"}, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, now).Items[0].DoneOn; got != OnApproved {
		t.Errorf("replaced: done on %q, want kept", got)
	}
	if err := s.Replace(1, Item{Link: "https://example.com/x"}, now); err == nil {
		t.Errorf("replaced with a link not a PR: no error")
	}
}

// An --until tick is kept for the boss's next reminder, also past the write that drops the item, and told once; the
// tick on a PR's move waits for its own taker.
func TestUntilTicked(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Pt 10:00 sync", Until: t0.Add(time.Hour).Unix()}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "later"}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TakeTicked(t0.Add(2*time.Hour), func(t Ticked) bool { return t.PR != 0 }, nil); err != nil || len(got) != 0 {
		t.Errorf("PR ticks: %q, %v; want none", texts(got), err)
	}
	got, err := s.TakeTicked(t0.Add(2*time.Hour), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`#1 "Pt 10:00 sync" ticked by itself: its --until time came`}; !slices.Equal(texts(got), want) {
		t.Errorf("ticks %q, want %q", texts(got), want)
	}
	if got, _ := s.TakeTicked(t0.Add(2*time.Hour), nil, nil); len(got) != 0 {
		t.Errorf("again: %q, want none", texts(got))
	}
	if got := read(t, s, t0.Add(2*time.Hour)).Updated; got != t0.Add(2*time.Hour).Unix() {
		t.Errorf("Updated %d: a take changed it", got)
	}
}

// The reminders tell the ticks first, also with no item open.
func TestReminderTicked(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ticked := []Ticked{{ID: 8, Text: "Second review of #415", PR: 415, On: OnApproved}, {ID: 3, Text: "Sync", PR: 0}, {ID: 4, Text: "CI", PR: 7, On: OnGreen}}
	head := "[dev todo] Ticked by itself since you last heard:\n" +
		`#8 "Second review of #415" ticked by itself: PR #415 approved (--done-on approved)` + "\n" +
		`#3 "Sync" ticked by itself: its --until time came` + "\n" +
		`#4 "CI" ticked by itself: PR #7 checks green (--done-on green)` + "\nAdd what comes next, if anything."
	if got := (List{Items: []Shown{}}).Reminder(ticked, now); got != head {
		t.Errorf("none open:\n%s\nwant\n%s", got, head)
	}
	l := List{Items: []Shown{{ID: 1, Text: "a"}}}
	if got := l.startReminder(false, "now", ticked, now); !strings.HasPrefix(got, head+"\n[dev todo] Your open to do list (updated now):\n1. a") {
		t.Errorf("start:\n%s", got)
	}
	if got := l.Reminder(nil, now); strings.Contains(got, "Ticked") {
		t.Errorf("no ticks: %s", got)
	}
}

// A replace that moves an item to another worker or PR tells its reasons afresh; an ask takes no --done-on.
func TestNudgesReplaceMoved(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	other := "https://github.com/acme/orbit/pull/500"
	if _, err := s.Add(Item{Text: "Rework", On: "fix", Session: &Session{ID: "fix", Name: "fix"}, Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN"}, other: {State: "OPEN"}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	p.states[prURL] = sidebar.PRView{State: "MERGED", Ended: t0}
	if err := s.Lookup(t0.Add(LookupTTL), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(LookupTTL)); len(got) != 2 {
		t.Fatalf("merged and gone: %q", got)
	}
	t1 := t0.Add(time.Hour)
	if err := s.Replace(1, Item{On: "fix2", Session: &Session{ID: "fix2", Name: "fix2"}, Link: other}, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := nudges(t, s, t1), []string{`#1 "Rework" (on fix2): its session is gone`}; !slices.Equal(got, want) {
		t.Errorf("on fix2: %q, want %q", got, want)
	}
	p.states[other] = sidebar.PRView{State: "MERGED", Ended: t1}
	if err := s.Lookup(t1.Add(LookupTTL), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t1.Add(LookupTTL)); len(got) != 1 || !strings.Contains(got[0], "PR #500 merged") {
		t.Errorf("new PR merged: %q", got)
	}
	if _, err := s.Add(Item{Text: "Ship?", Options: []string{"Yes", "No"}, Link: prURL, DoneOn: OnMerged}, t1); err == nil {
		t.Errorf("an ask took --done-on")
	}
}

// One PR event is one note: a merge says nothing of the review and checks that came with it; a merge ticks any
// --done-on; set and clear drop untold ticks; nudges drop the facts of items no longer open.
func TestNudgesMergeOnly(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Deploy", On: "Tomas", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Unlinked"}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIRunning}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	p.states[prURL] = sidebar.PRView{State: "MERGED", Review: "APPROVED", Checks: sidebar.CIPassed, Ended: t0}
	t1 := t0.Add(LookupTTL)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t1); len(got) != 1 || !strings.Contains(got[0], "merged") {
		t.Errorf("merged: %q, want one note", got)
	}
	if !reached(OnGreen, prSeen{State: "MERGED"}) || !reached(OnApproved, prSeen{State: "MERGED"}) {
		t.Errorf("a merge doesn't tick --done-on approved or green")
	}
	day := 24 * time.Hour
	nudges(t, s, t0.Add(2*day))
	if err := s.Done([]int{2}, t0.Add(2*day)); err != nil {
		t.Fatal(err)
	}
	nudges(t, s, t0.Add(2*day+time.Minute))
	facts, err := s.readChecks()
	if err != nil || len(facts) != 1 {
		t.Errorf("facts %v (%v), want only the open item's", facts, err)
	}
	if _, err := s.Add(Item{Text: "Sync", Until: t0.Add(2*day + time.Hour).Unix()}, t0.Add(2*day)); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(t0.Add(3 * day)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TakeTicked(t0.Add(3*day), nil, nil); len(got) != 0 {
		t.Errorf("after clear: %q", texts(got))
	}
}

// A waiting item's unchanged check is no re-check: it isn't flagged, while its PR's move still is.
func TestFlaggedWaitingUnchanged(t *testing.T) {
	waiting := &sidebar.Check{Kind: sidebar.CheckUnchanged, Waiting: true}
	if (Shown{Check: waiting}).Flagged() {
		t.Error("waiting unchanged flagged, want not")
	}
	if !(Shown{Check: &sidebar.Check{Kind: sidebar.CheckUnchanged}}).Flagged() {
		t.Error("unchanged not flagged, want flagged")
	}
	if !(Shown{Check: &sidebar.Check{Kind: sidebar.CheckMerged, PR: 1}}).Flagged() {
		t.Error("merged not flagged, want flagged")
	}
}

// A waiting item whose PR already merged at its first lookup is news: its unchanged nudge isn't "no action" nor once only.
func TestNudgesWaitingPRMerged(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Review of #418", On: "Marek", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED", Review: "APPROVED", Checks: sidebar.CIPassed, Ended: t0}}}
	if err := s.Lookup(t0, 0, 0, p.view); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	for _, d := range []time.Duration{2, 4} {
		got := nudges(t, s, t0.Add(d*day))
		if len(got) != 1 || strings.Contains(got[0], "no action") {
			t.Errorf("%dd: %q, want a nudge without no action", d, got)
		}
	}
}

// An item an open ask waits on (ask --for) is not told unchanged at two days but at seven, and weekly after; its hint names dev todo show.
func TestNudgesWaitingOnUserPick(t *testing.T) {
	s := store(t)
	s.boss = "boss-a"
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Ship the cache fix"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Ship it?", Options: []string{"Yes", "No"}, For: 1}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Plain item"}, t0); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	first := func(at time.Duration) []string {
		var out []string
		for _, l := range nudges(t, s, t0.Add(at)) {
			if strings.HasPrefix(l, "#1 ") {
				out = append(out, l)
			}
		}
		return out
	}
	if got, want := nudges(t, s, t0.Add(2*day)), []string{"#3 \"Plain item\": unchanged 2d: dev todo show 'boss-a' 3"}; !slices.Equal(got, want) {
		t.Errorf("2d: %q, want %q", got, want)
	}
	if got := first(6 * day); len(got) != 0 {
		t.Errorf("6d: %q, want none for #1", got)
	}
	if got, want := first(7*day), []string{"#1 \"Ship the cache fix\": unchanged 7d: dev todo show 'boss-a' 1"}; !slices.Equal(got, want) {
		t.Errorf("7d: %q, want %q", got, want)
	}
	if got := first(10 * day); len(got) != 0 {
		t.Errorf("10d: %q, want no second #1 nag within the week", got)
	}
	if got := first(14 * day); len(got) != 1 {
		t.Errorf("14d: %q, want the weekly #1 nag again", got)
	}
	// Its ask answered, the item is back on the two-day cadence
	if err := s.Done([]int{2}, t0.Add(14*day)); err != nil {
		t.Fatal(err)
	}
	if got := first(16 * day); len(got) != 1 {
		t.Errorf("ask answered: %q, want #1 nagged again at the two-day cadence", got)
	}
}

// A replace that changes only a number in the text (a counter) keeps what the boss was told: its worker gone isn't told again.
func TestNudgesCounterOnlyReplace(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Rework 2 of 5", On: "fix", Session: &Session{ID: "fix", Name: "fix"}}, t0); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(time.Hour)); len(got) != 1 {
		t.Fatalf("gone: %q, want one nudge", got)
	}
	if err := s.Replace(1, Item{Text: "Rework 3 of 5"}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(3*time.Hour)); len(got) != 0 {
		t.Errorf("counter changed: %q, want none", got)
	}
	if err := s.Replace(1, Item{Text: "Rework, rewritten"}, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := nudges(t, s, t0.Add(5*time.Hour)); len(got) != 1 {
		t.Errorf("new wording: %q, want its worker gone told afresh", got)
	}
}
