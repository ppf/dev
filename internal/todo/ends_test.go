package todo

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

func TestParseEnds(t *testing.T) {
	for _, ok := range []string{"restart:v0.93.0", "test:v1.0.12"} {
		if _, _, err := ParseEnds(ok); err != nil {
			t.Errorf("❌ %s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"restart", "reboot:v0.93.0", "test:0.93.0", "test:v0.93", "test:v0.93.0-rc1", "restart:v0.93.0+x", "test:v01.2.3", ""} {
		if _, _, err := ParseEnds(bad); err == nil {
			t.Errorf("❌ %q passed", bad)
		}
	}
}

func TestAtLeast(t *testing.T) {
	for _, c := range []struct {
		v, want string
		ok      bool
	}{
		{"v0.93.0", "v0.93.0", true},
		{"v0.94.0", "v0.93.0", true},
		{"v1.0.0", "v0.93.0", true},
		{"v0.92.9", "v0.93.0", false},
		{"v0.93.1-0.20261125101500-abcdef123456+dirty", "v0.93.0", true}, // a branch build after v0.93.0
		{"v0.93.0-0.20261125101500-abcdef123456", "v0.93.0", false},      // before v0.93.0
		{"(devel) abcdef", "v0.93.0", false},
		{"", "v0.93.0", false},
	} {
		if got := atLeast(c.v, c.want); got != c.ok {
			t.Errorf("❌ atLeast(%q, %q) = %v, want %v", c.v, c.want, got, c.ok)
		}
	}
}

func TestEndsAddReplace(t *testing.T) {
	s := store(t)
	now := time.Unix(1_000_000, 0)
	if _, err := s.Add(Item{Text: "Bad", Ends: "reboot:v1.0.0"}, now); err == nil {
		t.Error("❌ add with a bad --ends passed")
	}
	if _, err := s.Add(Item{Text: "Restart?", Options: []string{"Yes", "No"}, Ends: "restart:v1.0.0"}, now); err == nil {
		t.Error("❌ an ask with --ends passed")
	}
	id, err := s.Add(Item{Text: "Restart for v1.0.0", Ends: "restart:v1.0.0"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(id, Item{Text: "Restart the bosses for v1.0.0"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now); l.Items[0].Ends != "restart:v1.0.0" {
		t.Errorf("❌ replace without --ends dropped it: %+v", l.Items[0])
	}
	if err := s.Replace(id, Item{Ends: "test:v1.0.0"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now); l.Items[0].Ends != "test:v1.0.0" {
		t.Errorf("❌ replace with --ends kept the old one: %+v", l.Items[0])
	}
}

// expired runs Expire at now with starts, failing the test on an error.
func expired(t *testing.T, s Store, now time.Time, starts ...Start) int {
	t.Helper()
	n, err := s.Expire(now, func() ([]Start, error) { return starts, nil })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// item is open or ticked item id of s at now.
func item(t *testing.T, s Store, id int, now time.Time) Shown {
	t.Helper()
	for _, it := range read(t, s, now).Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %d", id)
	return Shown{}
}

// noAnswer fails the test on a tally event or an answer: dev's tick is never the user's.
func noAnswer(t *testing.T, it Shown) {
	t.Helper()
	if it.Answer != "" {
		t.Errorf("❌ answered %q", it.Answer)
	}
	events, err := ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("❌ tally events %+v", events)
	}
}

// told is what the boss of s is told of its ticks (TakeTicked), taking them.
func told(t *testing.T, s Store, now time.Time) []string {
	t.Helper()
	ticked, err := s.TakeTicked(now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tk := range ticked {
		out = append(out, tk.String())
	}
	return out
}

func TestExpireRestart(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_000_000, 0)
	id, err := s.Add(Item{Text: "Restart for v0.93.0", Ends: "restart:v0.93.0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	stay, err := s.Add(Item{Text: "Restart the router"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	updated := read(t, s, t0).Updated
	t1 := t0.Add(time.Minute)
	fresh := Start{Session: "manager", At: t1.Unix(), Version: "v0.93.0"}
	for name, starts := range map[string][]Start{
		"no coordinator":     nil,
		"one on v0.92.0":     {fresh, {Session: "boss-dev", At: t1.Unix(), Version: "v0.92.0"}},
		"one never recorded": {fresh, {Session: "boss-dev"}},
	} {
		if n := expired(t, s, t1, starts...); n != 0 {
			t.Errorf("❌ %s: ticked %d", name, n)
		}
	}
	if n := expired(t, s, t1, fresh, Start{Session: "boss-dev", At: t1.Unix(), Version: "v0.94.0"}); n != 1 {
		t.Fatalf("❌ every coordinator on v0.93.0 or later: ticked %d, want 1", n)
	}
	it := item(t, s, id, t1)
	if !it.Done || it.DoneBy != DoneByDev || it.Result != "closed by dev: restarted on v0.93.0" {
		t.Errorf("❌ tick: %+v", it)
	}
	if item(t, s, stay, t1).Done {
		t.Error("❌ an item with no --ends ticked")
	}
	if l := read(t, s, t1); l.Updated != updated {
		t.Errorf("❌ Updated moved: %d, was %d", l.Updated, updated)
	}
	noAnswer(t, it)
	want := `#1 "Restart for v0.93.0" closed by dev: restarted on v0.93.0 (--ends restart:v0.93.0)`
	if got := told(t, s, t1); len(got) != 1 || got[0] != want {
		t.Errorf("❌ told %q, want %q", got, want)
	}

	// Reopened, the same starts don't tick it again; the next fresh starts do
	t2 := t1.Add(time.Minute)
	if _, err := s.Reopen(id, "Restart for v0.93.0", t2); err != nil {
		t.Fatal(err)
	}
	if n := expired(t, s, t2.Add(time.Minute), fresh, Start{Session: "boss-dev", At: t1.Unix(), Version: "v0.94.0"}); n != 0 {
		t.Errorf("❌ reopened, ticked again by the same restart: %d", n)
	}
	if it := item(t, s, id, t2); it.Done || it.Ends != "restart:v0.93.0" {
		t.Errorf("❌ reopened: %+v", it)
	}
	if err := s.Replace(id, Item{Text: "Restart the bosses for v0.93.0"}, t2.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := expired(t, s, t2.Add(time.Minute), fresh, Start{Session: "boss-dev", At: t1.Unix(), Version: "v0.94.0"}); n != 0 {
		t.Errorf("❌ reopened and replaced, ticked again by the same restart: %d", n)
	}
	t3 := t2.Add(time.Hour)
	if n := expired(t, s, t3, Start{Session: "manager", At: t3.Unix(), Version: "v0.93.0"}); n != 1 {
		t.Errorf("❌ a restart after the reopen: ticked %d, want 1", n)
	}
}

func TestExpireTest(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_000_000, 0)
	old, err := s.Add(Item{Text: "Check v0.83.0 on the iPad", Ends: "test:v0.83.0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Check v0.85.0 by hand"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Restart for v0.93.0", Ends: "restart:v0.93.0"}, t0); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t0, func() ([]Start, error) { return nil, errors.New("tmux down") }); err == nil || !strings.Contains(err.Error(), "tmux down") || n != 0 {
		t.Errorf("❌ starts failing: %d %v", n, err)
	}
	// Only a test item replaces it: a restart item of a newer version, an item with none, an equal version don't
	if _, err := s.Add(Item{Text: "Same version", Ends: "test:v0.83.0"}, t0); err != nil {
		t.Fatal(err)
	}
	starts := func() ([]Start, error) { return nil, nil }
	if n, err := s.Expire(t0, starts); err != nil || n != 0 {
		t.Fatalf("❌ no newer test: ticked %d %v", n, err)
	}
	t1 := t0.Add(time.Minute)
	newer, err := s.Add(Item{Text: "Test v0.93.0 by hand", Ends: "test:v0.93.0"}, t1)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t1, starts); err != nil || n != 2 {
		t.Fatalf("❌ a newer test added: ticked %d %v, want both v0.83.0 tests", n, err)
	}
	it := item(t, s, old, t1)
	if !it.Done || it.DoneBy != DoneByDev || it.Result != "closed by dev: v0.93.0 replaced v0.83.0 (#5)" || newer != 5 {
		t.Errorf("❌ tick: %+v (newer #%d)", it, newer)
	}
	if item(t, s, newer, t1).Done {
		t.Error("❌ the newer test ticked")
	}
	noAnswer(t, it)
	if got := told(t, s, t1); len(got) != 2 || !strings.HasPrefix(got[0], `#1 "Check v0.83.0 on the iPad" closed by dev: v0.93.0 replaced v0.83.0 (#5) (--ends test:v0.83.0)`) {
		t.Errorf("❌ told %q", got)
	}

	// Reopened, the same newer test doesn't tick it again; a test newer still, added later, does
	t2 := t1.Add(time.Minute)
	if _, err := s.Reopen(old, "Check v0.83.0 on the iPad", t2); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t2.Add(time.Minute), starts); err != nil || n != 0 {
		t.Errorf("❌ reopened, ticked again by #5: %d %v", n, err)
	}
	if err := s.Replace(newer, Item{Text: "Test v0.93.0 by hand, all sections"}, t2.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t2.Add(3*time.Minute), starts); err != nil || n != 0 {
		t.Errorf("❌ reopened, ticked again by #5 replaced: %d %v", n, err)
	}
	t3 := t2.Add(time.Hour)
	if _, err := s.Add(Item{Text: "Test v0.94.0 by hand", Ends: "test:v0.94.0"}, t3); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t3, starts); err != nil || n != 2 {
		t.Errorf("❌ a test newer still: ticked %d %v, want the reopened one and #5", n, err)
	}
	if it := item(t, s, old, t3); it.Result != "closed by dev: v0.94.0 replaced v0.83.0 (#6)" {
		t.Errorf("❌ the newest test not named: %q", it.Result)
	}
}

func TestExpireNoStartsRead(t *testing.T) {
	s := store(t)
	now := time.Unix(1_000_000, 0)
	if _, err := s.Add(Item{Text: "Test v1.0.0", Ends: "test:v1.0.0"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Expire(now, func() ([]Start, error) { t.Error("❌ starts read with no restart item open"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWithdraw(t *testing.T) {
	s := store(t)
	now := time.Unix(1_000_000, 0)
	id, err := s.Add(Item{Text: "Clear it?", Options: []string{"Clear it", "Not now"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(id, "Another?", "withdrawn by dev: x", now); err == nil {
		t.Error("❌ withdraw of another text passed")
	}
	if err := s.Withdraw(id, "Clear it?", "withdrawn by dev: the manager worked on", now); err != nil {
		t.Fatal(err)
	}
	it := item(t, s, id, now)
	if !it.Done || it.DoneBy != DoneByDev || it.Result != "withdrawn by dev: the manager worked on" {
		t.Errorf("❌ withdrawn: %+v", it)
	}
	noAnswer(t, it)
	if got := told(t, s, now); len(got) != 1 || got[0] != `#1 "Clear it?" withdrawn by dev: the manager worked on` {
		t.Errorf("❌ told %q", got)
	}
	if err := s.Withdraw(id, "Clear it?", "again", now); err == nil {
		t.Error("❌ a second withdraw passed")
	}
}

// A starts failure skips the restart items only: a test item still ticks, the failure returned.
func TestExpireStartsFailing(t *testing.T) {
	s := store(t)
	now := time.Unix(1_000_000, 0)
	for _, it := range []Item{{Text: "Restart", Ends: "restart:v1.0.0"}, {Text: "Test v1.0.0", Ends: "test:v1.0.0"}, {Text: "Test v1.1.0", Ends: "test:v1.1.0"}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Expire(now, func() ([]Start, error) { return nil, errors.New("tmux down") })
	if n != 1 || err == nil || !item(t, s, 2, now).Done || item(t, s, 1, now).Done {
		t.Errorf("❌ ticked %d, %v: want only the older test", n, err)
	}
}

// An ask put in place of an --ends item doesn't take its --ends over.
func TestEndsAskReplace(t *testing.T) {
	s := store(t)
	now := time.Unix(1_000_000, 0)
	id, err := s.Add(Item{Text: "Restart for v1.0.0", Ends: "restart:v1.0.0"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceAsk(id, Item{Text: "Restart now?", Options: []string{"Yes", "No"}}, now); err == nil {
		t.Fatal("❌ ReplaceAsk of a non-ask passed")
	}
	if err := s.Replace(id, Item{Text: "Restart now?", Options: []string{"Yes", "No"}}, now.Add(time.Second)); err != nil {
		t.Fatalf("❌ an ask in place of an --ends item: %v", err)
	}
	if it := item(t, s, id, now); it.Ends != "" {
		t.Errorf("❌ the ask kept --ends %q", it.Ends)
	}
}

func TestStarts(t *testing.T) {
	rows := tmux.FormatRow("manager", "coordinator", "100 v0.93.0", "0") + tmux.FormatRow("manager", "previews", "", "0") +
		tmux.FormatRow("boss-dev", "coordinator", "", "0") + tmux.FormatRow("fix-login", "", "", "0") +
		tmux.FormatRow("boss-gone", "coordinator", "50 v0.90.0", "1")
	c := tmux.NewFake(func([]string) (string, error) { return rows, nil })
	got, err := Starts(c)
	want := []Start{{Session: "manager", At: 100, Version: "v0.93.0"}, {Session: "boss-dev"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("❌ Starts = %+v, %v; want %+v", got, err, want)
	}
	rows = tmux.FormatRow("manager", "coordinator", "x v1", "0")
	if _, err := Starts(c); err == nil {
		t.Error("❌ a bad @dev_version read")
	}
	none := tmux.NewFake(func([]string) (string, error) {
		return "", &tmux.Error{Err: errors.New("exit 1"), Stderr: "no server running on /tmp/tmux-1/default"}
	})
	if got, err := Starts(none); err != nil || got != nil {
		t.Errorf("❌ no server: %+v, %v; want none", got, err)
	}
}

// A newer test done since still replaces it; a reopened one waives only the test that ticked it, not one added after.
func TestExpireTestFinished(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_000_000, 0)
	starts := func() ([]Start, error) { return nil, nil }
	old, err := s.Add(Item{Text: "Test v0.93.0", Ends: "test:v0.93.0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := s.Add(Item{Text: "Test v0.94.0", Ends: "test:v0.94.0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{newer}, t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Hour)
	if n, err := s.Expire(t1, starts); err != nil || n != 1 {
		t.Fatalf("❌ the newer test done an hour ago: ticked %d %v", n, err)
	}
	// v0.95.0 comes before the reopen: it never ticked it, so it may
	// The list emptied, so numbers start again: v0.95.0 is #1, the reopened one #2
	v95, err := s.Add(Item{Text: "Test v0.95.0", Ends: "test:v0.95.0"}, t1.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t2 := t1.Add(2 * time.Minute)
	re, err := s.Reopen(old, "Test v0.93.0", t2)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(t2, starts); err != nil || n != 1 {
		t.Errorf("❌ v0.95.0 after the v0.94.0 tick: ticked %d %v", n, err)
	}
	if it := item(t, s, re.ID, t2); it.Result != fmt.Sprintf("closed by dev: v0.95.0 replaced v0.93.0 (#%d)", v95) {
		t.Errorf("❌ %q", it.Result)
	}
}

// A test with the number of an open one, from before the list's numbers restarted, still replaces it.
func TestExpireTestSameNumber(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_000_000, 0)
	if _, err := s.Add(Item{Text: "Test v0.95.0", Ends: "test:v0.95.0"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1}, t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Minute)
	id, err := s.Add(Item{Text: "Test v0.93.0", Ends: "test:v0.93.0"}, t1)
	if err != nil || id != 1 {
		t.Fatalf("add: #%d %v, want #1 again", id, err)
	}
	if n, err := s.Expire(t1, func() ([]Start, error) { return nil, nil }); err != nil || n != 1 {
		t.Errorf("❌ ticked %d %v: the finished #1 is another item", n, err)
	}
}

// A new --ends drops the waivers of the old one.
func TestEndsReplaceDropsWaivers(t *testing.T) {
	s := store(t)
	t0 := time.Unix(1_000_000, 0)
	id, err := s.Add(Item{Text: "Restart", Ends: "restart:v0.94.0"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	fresh := Start{Session: "manager", At: t0.Add(time.Minute).Unix(), Version: "v0.95.0"}
	if n := expired(t, s, t0.Add(2*time.Minute), fresh); n != 1 {
		t.Fatalf("ticked %d", n)
	}
	t1 := t0.Add(3 * time.Minute)
	if _, err := s.Reopen(id, "Restart", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(id, Item{Ends: "restart:v0.95.0"}, t1.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := expired(t, s, t1.Add(time.Minute), fresh); n != 1 {
		t.Errorf("❌ restart:v0.95.0 after its restart: ticked %d, want 1", n)
	}
}
