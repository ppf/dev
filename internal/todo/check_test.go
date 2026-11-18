package todo

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

const prURL = "https://github.com/acme/orbit/pull/412"

// prs is a view answering from states by URL, counting its calls; a missing URL fails.
type prs struct {
	states map[string]sidebar.PRView
	calls  int
}

func (p *prs) view(url string) (sidebar.PRView, error) {
	p.calls++
	v, ok := p.states[url]
	if !ok {
		return sidebar.PRView{}, errors.New("gh: no such PR")
	}
	return v, nil
}

// checked is item id's check as Read shows it, "" for none.
func checked(t *testing.T, s Store, id int, now time.Time) string {
	t.Helper()
	for _, it := range read(t, s, now).Items {
		if it.ID == id {
			if it.Check == nil {
				return ""
			}
			return it.Check.Long(now)
		}
	}
	t.Fatalf("no item %d", id)
	return ""
}

// A linked item is checked against the PR as its first lookup after the item was written saw it: a review or CI
// that moved since shows, one that was so already doesn't; a merge always shows; replacing the item starts afresh.
func TestLookupCheck(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "PR #412 open, CI pending", On: "reviewers", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIRunning}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t0); got != "" {
		t.Errorf("first lookup: check %q, want none", got)
	}
	updated := read(t, s, t0).Updated
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIPassed}
	t1 := t0.Add(5 * time.Minute)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Errorf("looked up %d times within the TTL, want 1", p.calls)
	}
	t2 := t0.Add(LookupTTL)
	if err := s.Lookup(t2, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := checked(t, s, 1, t2.Add(2*time.Hour)), "PR #412 approved 2h ago"; got != want {
		t.Errorf("approved: check %q, want %q", got, want)
	}
	if got := read(t, s, t2).Updated; got != updated {
		t.Errorf("a lookup changed the list's Updated: %d, want %d", got, updated)
	}
	// Seen again unchanged: still since it was first seen so
	if err := s.Lookup(t2.Add(LookupTTL), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := checked(t, s, 1, t2.Add(2*time.Hour)), "PR #412 approved 2h ago"; got != want {
		t.Errorf("seen again: check %q, want %q", got, want)
	}
	// Confirmed by the boss: the approval is its baseline now
	t3 := t2.Add(time.Hour)
	if err := s.Replace(1, Item{}, t3); err != nil {
		t.Fatal(err)
	}
	if err := s.Lookup(t3, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t3); got != "" {
		t.Errorf("replaced: check %q, want none", got)
	}
	merged := t3.Add(20 * time.Minute)
	p.states[prURL] = sidebar.PRView{State: "MERGED", Review: "APPROVED", Checks: sidebar.CIPassed, Ended: merged}
	if err := s.Lookup(t3.Add(LookupTTL+30*time.Minute), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := checked(t, s, 1, merged.Add(40*time.Minute)), "PR #412 merged 40m ago"; got != want {
		t.Errorf("merged: check %q, want %q (since it merged)", got, want)
	}
}

// Merged before the first lookup still shows: an open item on a merged PR is out of date whatever it said.
func TestLookupMergedAtFirst(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Deploy after #412 merges", On: "Tomas", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED", Ended: t0.Add(-time.Hour)}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := checked(t, s, 1, t0), "PR #412 merged 1h ago"; got != want {
		t.Errorf("check %q, want %q", got, want)
	}
}

// A failed lookup keeps what the last one found and is tried again after the TTL; limit caps the lookups of one
// call, ticked and unlinked items aren't looked up, and the facts of items gone are dropped.
func TestLookupLimitAndFailure(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	other := "https://github.com/acme/orbit/pull/7"
	for _, it := range []Item{{Text: "a", Link: prURL}, {Text: "b", Link: other}, {Text: "c"}, {Text: "d", Link: "https://example.com/x"}} {
		if _, err := s.Add(it, t0); err != nil {
			t.Fatal(err)
		}
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN"}, other: {State: "OPEN"}}}
	if err := s.Lookup(t0, LookupTTL, 1, p.view); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("limit 1: %d lookups", p.calls)
	}
	if err := s.Lookup(t0, LookupTTL, 1, p.view); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("second call: %d lookups, want the other PR looked up", p.calls)
	}
	p.states[prURL] = sidebar.PRView{State: "MERGED", Ended: t0}
	t1 := t0.Add(LookupTTL)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	delete(p.states, prURL)
	if err := s.Lookup(t1.Add(LookupTTL), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t1); got != "PR #412 merged 10m ago" {
		t.Errorf("after a failed lookup: check %q, want the last found kept", got)
	}
	facts, err := s.readChecks()
	if err != nil {
		t.Fatal(err)
	}
	if f := facts[(Item{ID: 1, Added: t0.Unix()}).key()]; !strings.Contains(f.Err, "no such PR") {
		t.Errorf("fact %+v, want the failure kept", f)
	}
	if err := s.Done([]int{1, 2}, t1); err != nil {
		t.Fatal(err)
	}
	id, err := s.Add(Item{Text: "e", Link: other}, t1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lookup(t1.Add(time.Hour), LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if facts, err = s.readChecks(); err != nil || len(facts) != 1 {
		t.Errorf("facts %v (%v), want only item %d's: the ticked items' dropped", facts, err, id)
	}
}

// An open item not replaced for two days is checked as unchanged; an older dev's item without a time isn't.
func TestUnchanged(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "Attach the signed DPA"}, t0); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t0.Add(47*time.Hour)); got != "" {
		t.Errorf("47h: check %q, want none", got)
	}
	if got, want := checked(t, s, 1, t0.Add(5*24*time.Hour)), "unchanged 5d: check it at its source"; got != want {
		t.Errorf("5d: check %q, want %q", got, want)
	}
	if c := check(Item{ID: 1, Text: "old"}, fact{}, t0); c != nil {
		t.Errorf("no Added: check %+v, want none", c)
	}
}

// Items to re-check lead the reminders, each with why; the rest follow.
func TestReminderFlagged(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := List{Items: []Shown{
		{ID: 1, Text: "Review the copy"},
		{ID: 2, Text: "PR #412 open", On: "reviewers", Check: &sidebar.Check{Kind: sidebar.CheckApproved, PR: 412, Since: now.Add(-2 * time.Hour).Unix()}},
		{ID: 3, Text: "Rework rounding", On: "fix", Gone: true},
	}}
	got := l.Reminder(nil, now)
	want := "[dev todo] Your open to do list:\nRe-check these first: tick it, replace it with what's true now, or confirm it holds (dev todo add --replaces <n>):" +
		"\n2. PR #412 open (on reviewers) ⚠ PR #412 approved 2h ago\n3. Rework rounding (on fix, session gone)\nThe rest:\n1. Review the copy\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("Reminder =\n%s\nwant it to start\n%s", got, want)
	}
	if got := (List{Items: []Shown{{ID: 1, Text: "a"}}}).Reminder(nil, now); strings.Contains(got, "Re-check") || strings.Contains(got, "The rest") {
		t.Errorf("nothing flagged: %q", got)
	}
}

// With closed, All adds the lists with open items of the bosses closed on the server, by name, each open item checked
// as boss closed unless its PR moved; an older dev's list without its name is its file's boss; a list under a session
// id, another server's, an older dev's whose file another socket may have written or with a hashed name, and a closed
// boss's emptied list stay out.
func TestAllClosed(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sessions := []string{"boss-live", "boss-kite", "boss-done", "boss-hash+x", "manager", "boss-bad", "boss-plain"}
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			var out string
			for _, n := range sessions {
				herd := "1"
				if n == "boss-plain" && len(sessions) < 3 {
					herd = ""
				}
				out += tmux.FormatRow(n, herd)
			}
			return out, nil
		case "display-message":
			return tmux.FormatRow("/tmp/tmux-1/default", strings.Trim(args[3], "=:"), "1", "$1", "1700000000"), nil
		}
		return "", nil
	})
	now := time.Unix(1_800_000_000, 0)
	for _, n := range sessions {
		s, err := ForPane(c, tmux.SessionTarget(n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(Item{Text: n + "'s"}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if n == "boss-done" {
			if err := s.Done([]int{1}, now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	old, err := open("/tmp/tmux-1/default-$7-1700000000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Add(Item{Text: "under a session id"}, now); err != nil {
		t.Fatal(err)
	}
	// Another server's socket starting alike, storing a name not live here
	other, err := open("/tmp/tmux-1/default-old-x")
	if err != nil {
		t.Fatal(err)
	}
	other.boss = "boss-other"
	if _, err := other.Add(Item{Text: "elsewhere"}, now); err != nil {
		t.Fatal(err)
	}
	bad, _ := ForPane(c, tmux.SessionTarget("boss-bad"))
	if err := os.WriteFile(bad.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Older dev's lists stored no name: their file's key names their boss, when it can only be a boss of this server
	unnamed := func(name string) {
		s, _ := ForPane(c, tmux.SessionTarget(name))
		data, err := os.ReadFile(s.path)
		if err != nil {
			s, _ = open("/tmp/tmux-1/default-" + name)
			s.boss = name
			if _, err := s.Add(Item{Text: name + "'s"}, now); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(s.path, []byte(strings.Replace(string(data), `,"boss":"`+name+`"`, "", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"boss-kite", "boss-hash+x", "boss-x-boss-y", "boss-x-manager", "boss-x-_7-1700000000"} {
		unnamed(name)
	}
	// boss-plain's session is open, though no boss any more
	sessions = []string{"boss-live", "boss-plain"}
	all, err := All(c, now, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range all {
		row := l.Boss
		if l.Closed {
			row += " closed"
		}
		if l.Closed && l.Err == nil && l.Boss == "boss-bad" {
			t.Errorf("boss-bad: no error")
		}
		for _, it := range l.Items {
			if it.Check != nil {
				row += " " + it.Check.Long(now)
			}
		}
		got = append(got, row)
	}
	want := []string{"boss-live", "boss-bad closed", "boss-kite closed boss closed", "manager closed boss closed"}
	if !slices.Equal(got, want) {
		t.Errorf("All = %q, want %q", got, want)
	}
	if all, err := All(c, now, nil, false); err != nil || len(all) != 1 {
		t.Errorf("without closed: %d lists, %v", len(all), err)
	}
}

// A list moved to a new key takes its checks along.
func TestTakeMovesChecks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	from, _ := Desktop("local_a")
	to, _ := Desktop("local_b")
	now := time.Unix(1_800_000_000, 0)
	if _, err := from.Add(Item{Text: "a", Link: prURL}, now); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "MERGED", Ended: now}}}
	if err := from.Lookup(now, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if taken, err := to.take(from); err != nil || !taken {
		t.Fatalf("take: %v %v", taken, err)
	}
	if got := checked(t, to, 1, now); got != "PR #412 merged 0m ago" {
		t.Errorf("check after the move: %q", got)
	}
}

// Each kind, in order: a merge beats a review's move, a move counts only against the first lookup, an ask and a dated
// item aren't unchanged, lookups failing for long say so.
func TestCheckKinds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	it := Item{ID: 1, Link: prURL, Added: now.Add(-time.Hour).Unix()}
	open := prSeen{State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIRunning}
	with := func(f func(*prSeen)) prSeen { p := open; f(&p); return p }
	tests := []struct {
		name string
		it   Item
		f    fact
		want string
	}{
		{"closed", it, fact{Link: prURL, First: open, Last: with(func(p *prSeen) { p.State = "CLOSED" })}, sidebar.CheckClosed},
		{"merged beats approved", it, fact{Link: prURL, First: open, Last: with(func(p *prSeen) { p.State, p.Review = "MERGED", "APPROVED" })}, sidebar.CheckMerged},
		{"changes requested", it, fact{Link: prURL, First: open, Last: with(func(p *prSeen) { p.Review = "CHANGES_REQUESTED" })}, sidebar.CheckChanges},
		{"checks failing", it, fact{Link: prURL, First: open, Last: with(func(p *prSeen) { p.Checks = sidebar.CIFailed })}, sidebar.CheckFailing},
		{"checks green", it, fact{Link: prURL, First: open, Last: with(func(p *prSeen) { p.Checks = sidebar.CIPassed })}, sidebar.CheckGreen},
		{"green from the start", it, fact{Link: prURL, First: with(func(p *prSeen) { p.Checks = sidebar.CIPassed }), Last: with(func(p *prSeen) { p.Checks = sidebar.CIPassed })}, ""},
		{"another link's facts", it, fact{Link: prURL + "0", First: open, Last: with(func(p *prSeen) { p.State = "MERGED" })}, ""},
		{"lookups failing 20m", it, fact{Link: prURL, Failing: now.Add(-20 * time.Minute).Unix(), Checked: now.Unix(), Err: "gh: auth"}, sidebar.CheckLookupFailing},
		{"lookups failing 10m", it, fact{Link: prURL, Failing: now.Add(-10 * time.Minute).Unix(), Checked: now.Unix(), Err: "gh: auth"}, ""},
		{"unchanged", Item{ID: 1, Added: now.Add(-49 * time.Hour).Unix()}, fact{}, sidebar.CheckUnchanged},
		{"an ask isn't unchanged", Item{ID: 1, Added: now.Add(-49 * time.Hour).Unix(), Options: []string{"a", "b"}}, fact{}, ""},
		{"dated, not yet", Item{ID: 1, Added: now.Add(-49 * time.Hour).Unix(), Until: now.Add(time.Hour).Unix()}, fact{}, ""},
	}
	for _, tt := range tests {
		got := ""
		if c := check(tt.it, tt.f, now); c != nil {
			got = c.Kind
		}
		if got != tt.want {
			t.Errorf("%s: kind %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A first lookup that fails sets no baseline and flags nothing; the first that works is the baseline, even of an
// approved PR.
func TestLookupFirstFails(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "a", On: "reviewers", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t0); got != "" {
		t.Errorf("failed first lookup: check %q", got)
	}
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIPassed}
	t1 := t0.Add(LookupTTL)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t1); got != "" {
		t.Errorf("first good lookup: check %q, want none (baseline)", got)
	}
	facts, _ := s.readChecks()
	for _, f := range facts {
		if f.Err != "" || f.Failing != 0 || f.First.Review != "APPROVED" {
			t.Errorf("fact %+v, want the failure cleared and the approval its baseline", f)
		}
	}
}

// An unreadable checks file is a cache gone: Read drops it and reads the list, the next Lookup starts afresh.
func TestBadChecksFile(t *testing.T) {
	s := store(t)
	now := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "a", Link: prURL}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.checksPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.checksPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := s.Read(now); err != nil || len(l.Items) != 1 {
		t.Fatalf("Read = %d items, %v", len(l.Items), err)
	}
	if _, err := os.Stat(s.checksPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bad checks file kept: %v", err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN"}}}
	if err := s.Lookup(now, LookupTTL, 0, p.view); err != nil || p.calls != 1 {
		t.Errorf("Lookup after it: %v, %d calls", err, p.calls)
	}
}

// The start reminder's cut counts the items to re-check and the rest alike, with no "The rest:" over nothing.
func TestStartReminderLimitFlagged(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var l List
	for i := 1; i <= startShown+2; i++ {
		it := Shown{ID: i, Text: "item " + strconv.Itoa(i)}
		if i <= 2 {
			it.Check = &sidebar.Check{Kind: sidebar.CheckBossClosed}
		}
		l.Items = append(l.Items, it)
	}
	got := l.StartReminder(false, "now", nil, now)
	if !strings.Contains(got, "\n2. item 2 ⚠ boss closed\nThe rest:\n3. item 3\n") || !strings.Contains(got, "\n+2 more: dev todo") {
		t.Errorf("StartReminder =\n%s", got)
	}
	l.Items = nil
	for i := 1; i <= startShown+1; i++ {
		l.Items = append(l.Items, Shown{ID: i, Text: "x", Check: &sidebar.Check{Kind: sidebar.CheckBossClosed}})
	}
	l.Items = append(l.Items, Shown{ID: 99, Text: "rest"})
	if got := l.StartReminder(false, "now", nil, now); strings.Contains(got, "The rest:") {
		t.Errorf("The rest: over no item:\n%s", got)
	}
}

// An approval keeps its age while CI moves on; a lookup older than one kept meanwhile is dropped; a GitLab link isn't
// looked up; a replace within the same second still starts its baseline afresh.
func TestLookupEdges(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_800_000_000, 0)
	if _, err := s.Add(Item{Text: "a", On: "reviewers", Link: prURL}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "mr", Link: "https://gitlab.com/acme/orbit/-/merge_requests/9"}, t0); err != nil {
		t.Fatal(err)
	}
	p := &prs{states: map[string]sidebar.PRView{prURL: {State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIPassed}}}
	if err := s.Lookup(t0, LookupTTL, 0, p.view); err != nil || p.calls != 1 {
		t.Fatalf("lookup: %v, %d calls (the MR looked up?)", err, p.calls)
	}
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIPassed}
	t1 := t0.Add(LookupTTL)
	if err := s.Lookup(t1, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIRunning}
	t2 := t1.Add(LookupTTL)
	if err := s.Lookup(t2, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got, want := checked(t, s, 1, t2.Add(time.Hour)), "PR #412 approved 1h 10m ago"; got != want {
		t.Errorf("after a CI re-run: %q, want %q (since the approval)", got, want)
	}
	// A lookup that started before the one kept: dropped
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "REVIEW_REQUIRED", Checks: sidebar.CIRunning}
	if err := s.Lookup(t2.Add(-time.Minute), 0, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t2); !strings.HasPrefix(got, "PR #412 approved") {
		t.Errorf("an older lookup overwrote the newer: %q", got)
	}
	if err := s.Replace(1, Item{}, t2); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(1, Item{}, t2); err != nil {
		t.Fatal(err)
	}
	p.states[prURL] = sidebar.PRView{State: "OPEN", Review: "APPROVED", Checks: sidebar.CIPassed}
	if err := s.Lookup(t2, LookupTTL, 0, p.view); err != nil {
		t.Fatal(err)
	}
	if got := checked(t, s, 1, t2); got != "" {
		t.Errorf("confirmed twice in a second: %q, want a fresh baseline", got)
	}
}
