package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

func store(t *testing.T) Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := Desktop("local_boss")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ids is l's items as "<id> <text>", with " on <who>" and " done" when set.
func ids(l List) []string {
	var out []string
	for _, it := range l.Items {
		s := []string{strconv.Itoa(it.ID), it.Text}
		if it.On != "" {
			s = append(s, "on", it.On)
		}
		if it.Done {
			s = append(s, "done")
		}
		out = append(out, strings.Join(s, " "))
	}
	return out
}

func read(t *testing.T, s Store, now time.Time) List {
	t.Helper()
	l, err := s.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// set numbers from 1; add takes the next number; waiting-on items show last; a ticked item shows done for
// doneShown, then leaves; numbers start again from 1 only once the list is empty.
func TestList(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if l := read(t, s, now); len(l.Items) != 0 || l.Updated != 0 || l.Items == nil {
		t.Fatalf("missing file: %+v, want empty items", l)
	}
	if err := s.Set([]string{"a", "b", "c"}, now); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Add(Item{Text: "d", On: "Marek"}, now); err != nil || id != 4 {
		t.Fatalf("add = %d, %v; want 4", id, err)
	}
	if id, _ := s.Add(Item{Text: "e", Link: "https://github.com/o/r/pull/87"}, now); id != 5 {
		t.Fatalf("add = %d, want 5", id)
	}
	if got, want := ids(read(t, s, now)), []string{"1 a", "2 b", "3 c", "5 e", "4 d on Marek"}; !slices.Equal(got, want) {
		t.Errorf("list = %q, want %q", got, want)
	}
	if err := s.Done([]int{5}, now); err != nil {
		t.Fatal(err)
	}
	if got := ids(read(t, s, now.Add(doneShown-time.Second))); !slices.Contains(got, "5 e done") {
		t.Errorf("just ticked: %q, want 5 done", got)
	}
	later := now.Add(doneShown)
	if got := ids(read(t, s, later)); slices.ContainsFunc(got, func(s string) bool { return s[0] == '5' }) {
		t.Errorf("after doneShown: %q, want 5 gone", got)
	}
	if id, _ := s.Add(Item{Text: "f"}, later); id != 6 {
		t.Errorf("add after a tick = %d, want 6: no reuse while items are open", id)
	}
	if err := s.Done([]int{1, 2, 3, 4, 6}, later); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Add(Item{Text: "g"}, later); id != 7 {
		t.Errorf("add while ticked items show = %d, want 7: no number shown twice", id)
	}
	if err := s.Done([]int{7}, later); err != nil {
		t.Fatal(err)
	}
	later = later.Add(doneShown)
	if id, _ := s.Add(Item{Text: "h"}, later); id != 1 {
		t.Errorf("add once the list is empty = %d, want 1", id)
	}
	if err := s.Clear(later); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, later); len(l.Items) != 0 || l.Updated != later.Unix() {
		t.Errorf("cleared: %+v", l)
	}
}

// done refuses a number that isn't an open item, changing nothing.
func TestDoneUnknown(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if err := s.Set([]string{"a", "b"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Done([]int{1, 9}, now); err == nil || err.Error() != "no open item 9" {
		t.Fatalf("err = %v, want no open item 9", err)
	}
	if err := s.Done([]int{1}, now); err != nil {
		t.Errorf("1 was ticked by the refused call: %v", err)
	}
	if err := s.Done([]int{1}, now); err == nil {
		t.Error("ticking 1 twice: want an error")
	}
}

// A list unchanged for an hour is stale.
func TestStale(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if err := s.Set([]string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	if read(t, s, now.Add(59*time.Minute)).Stale {
		t.Error("stale at 59m")
	}
	if !read(t, s, now.Add(time.Hour)).Stale {
		t.Error("not stale at 1h")
	}
}

// A file that doesn't parse is an error naming it.
func TestReadBroken(t *testing.T) {
	s := store(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(time.Now()); err == nil || !strings.Contains(err.Error(), s.path) {
		t.Errorf("err = %v, want it to name %s", err, s.path)
	}
}

func TestPR(t *testing.T) {
	for link, want := range map[string]int{
		"https://github.com/o/r/pull/87":             87,
		"https://github.com/o/r/pull/87/files":       87,
		"https://gitlab.com/g/p/-/merge_requests/12": 12,
		"https://slack.com/archives/C1/p2":           0,
		"https://github.com/o/r/pull/87x":            0,
		"":                                           0,
	} {
		if got := (Shown{Link: link}).PR(); got != want {
			t.Errorf("PR(%q) = %d, want %d", link, got, want)
		}
	}
}

// A dev boss session's panes share its list, keyed by socket and session name, so a boss reopened under its
// name (a new session id and creation time) gets it back; another session's pane is refused.
func TestForPane(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	herd := map[string]string{"%1": "1", "%2": "1", "%3": ""}
	c := tmux.NewFake(func(args []string) (string, error) {
		return tmux.FormatRow("/tmp/tmux-1/default", "boss-p", herd[args[3]], "$1", "1700000000"), nil
	})
	a, err := ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := ForPane(c, "%2"); b != a {
		t.Errorf("%%2's store %v, want %%1's %v", b, a)
	}
	if !strings.HasSuffix(a.path, "/todo/tmp_tmux-1_default-boss-p.json") {
		t.Errorf("path = %s", a.path)
	}
	if _, err := ForPane(c, "%3"); err == nil || !errors.Is(err, ErrNotBoss) || !strings.HasPrefix(err.Error(), "boss-p is not a boss session") {
		t.Errorf("err = %v, want not a boss session", err)
	}
}

// Session names a file name would make alike keep lists of their own.
func TestForPaneUnsafeNames(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := func(name string) Store {
		s, err := ForPane(tmux.NewFake(func([]string) (string, error) {
			return tmux.FormatRow("/tmp/tmux-1/default", name, "1", "$1", "1700000000"), nil
		}), "%1")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if a, b := store("boss-app+api"), store("boss-app_api"); a == b {
		t.Errorf("boss-app+api and boss-app_api share %s", a.path)
	}
}

// A boss's list kept under the earlier key (session id and creation time) moves to its name's key, once.
func TestForPaneMovesEarlierKey(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func([]string) (string, error) {
		return tmux.FormatRow("/tmp/tmux-1/default", "boss-p", "1", "$4", "1700000000"), nil
	})
	old, err := open("/tmp/tmux-1/default-$4-1700000000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Add(Item{Text: "kept"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s, err := ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Read(time.Now()); !slices.Equal(ids(l), []string{"1 kept"}) {
		t.Errorf("list = %q, want the earlier key's", ids(l))
	}
	if _, err := os.Stat(old.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("earlier key's file still there (%v)", err)
	}
}

func TestClean(t *testing.T) {
	if got := Clean(" a\tb\n c\x1b[31m d  "); got != "a b c [31m d" {
		t.Errorf("Clean = %q", got)
	}
}

// An item with Until ticks by itself at that time, shows ticked for doneShown, then leaves; it is no longer open
// for done or Replace.
func TestUntil(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "meeting", Until: now.Unix() + 60}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "b"}, now); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now.Add(59*time.Second)); !slices.Equal(ids(l), []string{"1 meeting", "2 b"}) || l.Items[0].Until != now.Unix()+60 {
		t.Errorf("before until: %+v", l)
	}
	if got := ids(read(t, s, now.Add(time.Minute))); !slices.Equal(got, []string{"1 meeting done", "2 b"}) {
		t.Errorf("at until: %v", got)
	}
	if got := ids(read(t, s, now.Add(time.Minute+doneShown))); !slices.Equal(got, []string{"2 b"}) {
		t.Errorf("doneShown after until: %v", got)
	}
	if err := s.Done([]int{1}, now.Add(time.Minute)); err == nil {
		t.Error("done on an expired item: want an error")
	}
	if err := s.Replace(1, Item{Text: "x"}, now.Add(time.Minute)); err == nil {
		t.Error("replace of an expired item: want an error")
	}
}

// Replace puts the new item in the old one's place and number, keeping the On, Link and Until it doesn't set;
// a number that isn't open changes nothing.
func TestReplace(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if err := s.Set([]string{"a", "b", "c"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(2, Item{Text: "b2"}, now); err != nil {
		t.Fatal(err)
	}
	if got := ids(read(t, s, now)); !slices.Equal(got, []string{"1 a", "2 b2", "3 c"}) {
		t.Errorf("after replace: %v", got)
	}
	if _, err := s.Add(Item{Text: "d", On: "Marek", Link: "https://x/pull/1", Until: now.Unix() + 60}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(3, Item{}, now); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, now).Items[2].Text; got != "c" {
		t.Errorf("replace without text: %q, want c kept", got)
	}
	if err := s.Replace(4, Item{Text: "d2", On: "Ola"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(9, Item{Text: "z"}, now); err == nil || err.Error() != "no open item 9" {
		t.Errorf("err = %v, want no open item 9", err)
	}
	l := read(t, s, now)
	if got := ids(l); !slices.Equal(got, []string{"1 a", "2 b2", "3 c", "4 d2 on Ola"}) || l.Items[3].Link != "https://x/pull/1" || l.Items[3].Until != now.Unix()+60 {
		t.Errorf("after replace keeping link and until: %+v", l)
	}
	if id, err := s.Add(Item{Text: "e"}, now); err != nil || id != 5 {
		t.Errorf("next add = %d, %v; want 5", id, err)
	}
}

// A worker's item keeps its session through a replace that doesn't name who does it; --on another takes its own
// (none for a person); --on me gives it back to the user, as does an add with it.
func TestReplaceSession(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	w := &Session{ID: "local_w", Name: "fix-login"}
	if _, err := s.Add(Item{Text: "a", On: "fix-login", Session: w}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(1, Item{Text: "a2"}, now); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, now).Items[0]; it.On != "fix-login" || it.Session == nil || *it.Session != *w {
		t.Errorf("replace without --on: %+v, want the worker kept", it)
	}
	if err := s.Replace(1, Item{Text: "a3", On: "Marek"}, now); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, now).Items[0]; it.On != "Marek" || it.Session != nil {
		t.Errorf("replace --on a person: %+v, want no session", it)
	}
	if err := s.Replace(1, Item{Text: "a4", On: Me}, now); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, now).Items[0]; it.On != "" || it.Session != nil {
		t.Errorf("replace --on me: %+v, want the user's", it)
	}
	if _, err := s.Add(Item{Text: "b", On: Me}, now); err != nil {
		t.Fatal(err)
	}
	if it := read(t, s, now).Items[0]; it.Text != "a4" || read(t, s, now).Items[1].On != "" {
		t.Errorf("add --on me: %+v, want the user's", read(t, s, now).Items)
	}
}

// The session goes out in --json as {id, name}, and only for a worker's item.
func TestSessionJSON(t *testing.T) {
	got, err := json.Marshal([]Shown{{ID: 1, Text: "a", On: "w", Session: &Session{ID: "local_1", Name: "w"}}, {ID: 2, Text: "b", On: "Ola"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"id":1,"text":"a","on":"w","session":{"id":"local_1","name":"w"},"done":false},{"id":2,"text":"b","on":"Ola","done":false}]`; string(got) != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

// Match finds a worker by id, then by its unique name; a shared name or anything else is a person.
func TestMatch(t *testing.T) {
	ws := []Session{{ID: "local_1", Name: "Fix login"}, {ID: "local_2", Name: "local_1"}, {ID: "local_3", Name: "twin"}, {ID: "local_5", Name: "Twin"}, {ID: "local_4", Name: " Spaced "}}
	for _, tt := range []struct {
		on, want string // want: the id, "" for none
	}{
		{"local_1", "local_1"},
		{" fix LOGIN ", "local_1"},
		{"Fix login", "local_1"},
		{"local_2", "local_2"},
		{"Marek", ""},
		{"fix", ""},
		{"Twin", ""},
		{"spaced", "local_4"},
	} {
		w, ok := Match(tt.on, ws)
		if ok != (tt.want != "") || w.ID != tt.want {
			t.Errorf("Match(%q) = %+v, %t; want %q", tt.on, w, ok, tt.want)
		}
	}
}

// Workers lists the sessions the pane's boss started, by name, leaving out other bosses and other sessions'
// workers; a boss names them by its name, an older dev by its session id.
func TestWorkers(t *testing.T) {
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "display-message" {
			return tmux.FormatRow("/tmp/tmux-1/default", "boss-p", "1", "$1", "1700000000"), nil
		}
		return strings.Join([]string{
			tmux.FormatRow("boss-p", "", "1"),
			tmux.FormatRow("fix-login", "boss-p", ""),
			tmux.FormatRow("old-worker", "$1", ""),
			tmux.FormatRow("sub-boss", "boss-p", "1"),
			tmux.FormatRow("theirs", "boss-q", ""),
		}, ""), nil
	})
	got, err := Workers(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Session{{"fix-login", "fix-login"}, {"old-worker", "old-worker"}}; !slices.Equal(got, want) {
		t.Errorf("Workers = %+v, want %+v", got, want)
	}
}

func TestParseUntil(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 14, 0, 0, 0, warsaw)
	for _, tt := range []struct {
		in, want string // want "" for an error
	}{
		{"15:30", "2026-11-13 15:30"},
		{"2026-11-16", "2026-11-17 00:00"},
		{"2026-11-16 10:00", "2026-11-16 10:00"},
		{"13:59", ""},
		{"2026-11-12", ""},
		{"tomorrow", ""},
		{"25:00", ""},
	} {
		got, err := ParseUntil(tt.in, now)
		if tt.want == "" {
			if err == nil {
				t.Errorf("ParseUntil(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got.Format("2006-01-02 15:04") != tt.want || got.Location() != warsaw {
			t.Errorf("ParseUntil(%q) = %v, %v; want %s in Warsaw", tt.in, got, err, tt.want)
		}
	}
}

// Reminder lists the open items with their number, who they wait on and until when; "" with none open.
func TestReminder(t *testing.T) {
	l := List{Items: []Shown{{ID: 1, Text: "a"}, {ID: 2, Text: "b", Done: true}, {ID: 3, Text: "c", On: "Marek", Until: time.Date(2026, 11, 16, 10, 0, 0, 0, time.Local).Unix()}}}
	want := "[dev todo] Your open to do list:\n1. a\n3. c (on Marek) (until 2026-11-16 10:00)\n" +
		`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".` + whoLine
	if got := l.Reminder(); got != want {
		t.Errorf("Reminder = %q, want %q", got, want)
	}
	if got := (List{Items: []Shown{{ID: 2, Text: "b", Done: true}}}).Reminder(); got != "" {
		t.Errorf("all done: %q, want none", got)
	}
}

// The list a boss gets at its start names at most startShown open items, then how many more.
func TestStartReminder(t *testing.T) {
	var l List
	for i := 1; i <= startShown+3; i++ {
		l.Items = append(l.Items, Shown{ID: i, Text: "t" + strconv.Itoa(i), Done: i == 1})
	}
	got := l.StartReminder(true, "2h ago")
	if !strings.HasPrefix(got, "[dev todo] Your open to do list (kept from your last boss session, updated 2h ago):\n2. t2\n") ||
		!strings.Contains(got, "\n21. t21\n+2 more: dev todo\nCheck each still holds") || strings.Contains(got, "t22") {
		t.Errorf("StartReminder = %q", got)
	}
	if got := (List{Items: []Shown{{ID: 1, Text: "a", Done: true}}}).StartReminder(false, "now"); got != "" {
		t.Errorf("all done: %q, want none", got)
	}
}

// A desktop boss without a list takes over the most recently changed list with open items of an earlier boss,
// once: the file moves, and a list it has already keeps it from asking again.
func TestAdoptDesktop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1700000000, 0)
	for i, id := range []string{"local_a", "local_b", "local_live", "local_emptied"} {
		s, err := Desktop(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(Item{Text: id}, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// Newest, but emptied: not taken
	emptied, _ := Desktop("local_emptied")
	if err := emptied.Clear(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	asked := 0
	earlier := func() (func(string) bool, error) {
		asked++
		return func(id string) bool { return id != "local_live" }, nil
	}
	s, err := Desktop("local_new")
	if err != nil {
		t.Fatal(err)
	}
	if adopted, err := s.AdoptDesktop(earlier); err != nil || !adopted {
		t.Fatalf("AdoptDesktop = %v, %v; want adopted", adopted, err)
	}
	if l, _ := s.Read(now); !slices.Equal(ids(l), []string{"1 local_b"}) {
		t.Errorf("adopted %q, want local_b's list (newest of the earlier ones)", ids(l))
	}
	b, _ := Desktop("local_b")
	if _, err := os.Stat(b.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("local_b's file still there (%v): adopted twice", err)
	}
	if adopted, err := s.AdoptDesktop(earlier); err != nil || adopted || asked != 1 {
		t.Errorf("again: adopted %v, err %v, asked %d; want nothing, earlier not asked", adopted, err, asked)
	}
	none, _ := Desktop("local_none")
	if adopted, err := none.AdoptDesktop(func() (func(string) bool, error) {
		return func(string) bool { return false }, nil
	}); err != nil || adopted {
		t.Errorf("no earlier list: adopted %v, err %v", adopted, err)
	}
	if _, err := os.Stat(none.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no earlier list made a file (%v)", err)
	}
}

// A list it can't read is skipped and reported; the newest readable one is still taken over.
func TestAdoptDesktopUnreadable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	good, _ := Desktop("local_good")
	if _, err := good.Add(Item{Text: "kept"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(good.path), "desktop-local_bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := Desktop("local_new")
	adopted, err := s.AdoptDesktop(func() (func(string) bool, error) { return func(string) bool { return true }, nil })
	if !adopted || err == nil || !strings.Contains(err.Error(), "desktop-local_bad.json") {
		t.Errorf("AdoptDesktop = %v, %v; want local_good's list and local_bad's error", adopted, err)
	}
}

// MarkGone reads the workers only when an open item has one, and the reminders show gone items.
func TestMarkGone(t *testing.T) {
	read := 0
	workers := func() ([]Session, error) { read++; return []Session{{ID: "live", Name: "live"}}, nil }
	l := List{Items: []Shown{{ID: 1, Text: "a", On: "Ola"}}}
	if err := l.MarkGone(workers); err != nil || read != 0 {
		t.Errorf("no linked item: %v, %d reads; want none", err, read)
	}
	l.Items = append(l.Items,
		Shown{ID: 2, Text: "b", On: "live", Session: &Session{ID: "live", Name: "live"}},
		Shown{ID: 3, Text: "c", On: "old", Session: &Session{ID: "old", Name: "old"}},
		Shown{ID: 4, Text: "d", On: "old", Session: &Session{ID: "old", Name: "old"}, Done: true})
	if err := l.MarkGone(workers); err != nil || read != 1 {
		t.Fatalf("MarkGone: %v, %d reads", err, read)
	}
	if got := []bool{l.Items[0].Gone, l.Items[1].Gone, l.Items[2].Gone, l.Items[3].Gone}; !slices.Equal(got, []bool{false, false, true, false}) {
		t.Errorf("gone = %v, want only the open item on old", got)
	}
	if got := l.Reminder(); !strings.Contains(got, "\n2. b (on live)\n3. c (on old, session gone)\n") {
		t.Errorf("Reminder = %q", got)
	}
}

// An answer picks an option by number or label, "(Recommended)" and case aside, else is the user's own words.
func TestPick(t *testing.T) {
	opts := []string{"Merge (Recommended)", "Wait for review"}
	for _, tt := range []struct{ arg, want, err string }{
		{"1", "Merge", ""},
		{"2", "Wait for review", ""},
		{" wait FOR review ", "Wait for review", ""},
		{"merge (recommended)", "Merge", ""},
		{"after the release", "after the release", ""},
		{"3", "", "option 3: it has 2 (1-2)"},
		{"0", "", "option 0: it has 2 (1-2)"},
		{"  ", "", "empty answer"},
	} {
		got, err := Pick(opts, tt.arg)
		if got != tt.want || (err == nil) != (tt.err == "") || err != nil && err.Error() != tt.err {
			t.Errorf("Pick(%q) = %q, %v; want %q, %q", tt.arg, got, err, tt.want, tt.err)
		}
	}
	if got, err := Pick([]string{"30", "60 (Recommended)"}, "60"); got != "60" || err != nil {
		t.Errorf("a number that is a label: Pick(60) = %q, %v; want 60", got, err)
	}
	if got, err := Pick(nil, "2"); got != "2" || err != nil {
		t.Errorf("no options: Pick(2) = %q, %v; want the text", got, err)
	}
}

// An ask keeps its options through a replace; answering ticks it with the answer, once; an item on someone else
// isn't the user's to answer.
func TestAnswer(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	id, err := s.Add(Item{Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Item{Text: "Staging", On: "Marek"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(id, Item{Text: "Merge #148 now?"}, now); err != nil {
		t.Fatal(err)
	}
	if l := read(t, s, now); !slices.Equal(l.Items[0].Options, []string{"Merge (Recommended)", "Wait"}) {
		t.Errorf("options after replace: %+v", l.Items[0])
	}
	if _, err := s.Answer(2, "", "ok", 0, now); err == nil || err.Error() != "item 2 is on Marek, not the user's to answer" {
		t.Errorf("answer an item on Marek: %v", err)
	}
	if _, err := s.Answer(id, "", "5", 0, now); err == nil {
		t.Error("option 5 of 2 answered")
	}
	if _, err := s.Answer(id, "Another question?", "2", 0, now); err == nil || !strings.Contains(err.Error(), `is "Merge #148 now?" now, not "Another question?"`) {
		t.Errorf("answer with another question: %v", err)
	}
	it, err := s.Answer(id, "Merge #148 now?", "2", 0, now)
	if err != nil || it.Text != "Merge #148 now?" || it.Answer != "Wait" || it.Done != now.Unix() {
		t.Errorf("Answer = %+v, %v", it, err)
	}
	if l := read(t, s, now); !l.Items[0].Done || l.Items[0].Answer != "Wait" {
		t.Errorf("after answer: %+v", l.Items[0])
	}
	if _, err := s.Answer(id, "", "1", 0, now); err == nil || err.Error() != "item 1 is answered already: Wait" {
		t.Errorf("second answer: %v, want answered already", err)
	}
	plain, _ := s.Add(Item{Text: "Answer Vince"}, now)
	if _, err := s.Answer(plain, "", "ok", 0, now); err == nil || !strings.Contains(err.Error(), "is no question") {
		t.Errorf("answer a plain item: %v, want refused", err)
	}
}

// The open list a boss gets names an ask's options, so a cleared boss knows it still waits on the user.
func TestReminderAsk(t *testing.T) {
	l := List{Items: []Shown{{ID: 2, Text: "Merge?", Options: []string{"Merge (Recommended)", "Wait"}}}}
	if got := l.lines(0); got != "\n2. Merge? (asked, options: Merge (Recommended) · Wait)" {
		t.Errorf("lines = %q", got)
	}
}

// A board's answer carries the question it showed: numbers restart once a list empties, so a stale board's #1 may be a newer ask.
func TestAnswerQuestionGuard(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	if _, err := s.Add(Item{Text: "Merge #148?", Options: []string{"Merge", "Wait"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(now); err != nil {
		t.Fatal(err)
	}
	id, err := s.Add(Item{Text: "Release now?", Options: []string{"Merge", "Wait"}}, now)
	if err != nil || id != 1 {
		t.Fatalf("new ask: %d, %v, want #1 again", id, err)
	}
	if _, err := s.Answer(1, "Merge #148?", "Merge", 0, now); err == nil || !strings.Contains(err.Error(), "look at the list again") {
		t.Errorf("answer by the old question: %v, want refused", err)
	}
	if l := read(t, s, now); l.Items[0].Done {
		t.Error("refused answer ticked the item")
	}
	if it, err := s.Answer(1, "Release now?", "Wait", 0, now); err != nil || it.Answer != "Wait" {
		t.Errorf("answer by its own question: %+v, %v", it, err)
	}
}

// An answer relayed by the manager needs an item the user saw: added, or last replaced, before their latest prompt.
// A pick answers only with the option it picked, by label or number, case and "(Recommended)" aside.
func TestAnswerPick(t *testing.T) {
	s := store(t)
	now := time.Unix(1700000000, 0)
	id, err := s.Add(Item{Text: "Merge #148?", Options: []string{"Merge (Recommended)", "Wait"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerPick(id, "", "Wait", "Merge (Recommended)", 0, now); err == nil || !strings.Contains(err.Error(), `the user picked "Merge (Recommended)", not "Wait"`) {
		t.Errorf("another option: %v, want refused", err)
	}
	if it, err := s.AnswerPick(id, "", "1", "merge (recommended)", 0, now); err != nil || it.Answer != "Merge" {
		t.Errorf("AnswerPick = %+v, %v", it, err)
	}
	id, err = s.Add(Item{Text: "Branch?", Options: []string{"main"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerPick(id, "", "release/abc", "Release/ABC", 0, now); err == nil {
		t.Error("free text with another case: answered, want refused")
	}
}

func TestAnswerBefore(t *testing.T) {
	s := store(t)
	added := time.Unix(1700000000, 0)
	id, err := s.Add(Item{Text: "Merge #148?", Options: []string{"Merge", "Wait"}}, added)
	if err != nil {
		t.Fatal(err)
	}
	for _, before := range []int64{added.Unix() - 5, added.Unix()} {
		if _, err := s.Answer(id, "", "Merge", before, added); err == nil || !strings.Contains(err.Error(), "newer than the user's latest prompt") {
			t.Errorf("before %d: %v, want refused", before, err)
		}
	}
	if err := s.Replace(id, Item{Text: "Merge #148 now?"}, added.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(id, "", "Merge", added.Unix()+30, added); err == nil {
		t.Error("an item replaced after the prompt answered")
	}
	if it, err := s.Answer(id, "", "Merge", added.Unix()+61, added); err != nil || it.Answer != "Merge" {
		t.Errorf("Answer = %+v, %v", it, err)
	}
	// A list from before items were stamped can't show the user saw it
	legacy, _ := s.Add(Item{Text: "Old?", Options: []string{"Yes", "No"}}, added)
	if err := s.update(added, func(f *file) error {
		f.Items[len(f.Items)-1].Added = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(legacy, "", "Yes", added.Unix()+100, added); err == nil {
		t.Error("an unstamped item answered via the manager")
	}
}

// All reads each boss session's list, by name: a session that isn't a boss and an empty list are left out, a list
// not read comes with why, a boss closed since list-sessions is left out.
func TestAll(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return tmux.FormatRow("boss-z", "1") + tmux.FormatRow("w", "") + tmux.FormatRow("boss-a", "1") +
				tmux.FormatRow("boss-empty", "1") + tmux.FormatRow("boss-gone", "1") + tmux.FormatRow("boss-bad", "1"), nil
		case "display-message":
			name := strings.Trim(args[3], "=:")
			if name == "boss-gone" {
				return "", errors.New("can't find session")
			}
			return tmux.FormatRow("/tmp/tmux-1/default", name, "1", "$1", "1700000000"), nil
		case "has-session":
			if strings.Contains(args[2], "boss-gone") {
				return "", errors.New("can't find session")
			}
		}
		return "", nil
	})
	now := time.Now()
	for _, name := range []string{"boss-a", "boss-z", "boss-bad"} {
		s, err := ForPane(c, tmux.SessionTarget(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(Item{Text: name + "'s"}, now); err != nil {
			t.Fatal(err)
		}
		if name == "boss-bad" {
			if err := os.WriteFile(s.path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	all, err := All(c, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range all {
		got = append(got, fmt.Sprintf("%s %q %v", l.Boss, ids(l.List), l.Err != nil))
	}
	want := []string{`boss-a ["1 boss-a's"] false`, `boss-bad [] true`, `boss-z ["1 boss-z's"] false`}
	if !slices.Equal(got, want) {
		t.Errorf("All = %q, want %q", got, want)
	}
}
