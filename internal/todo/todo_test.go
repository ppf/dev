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
		`Tick what these notes closed (dev todo done <n>); an item they changed: dev todo add --replaces <n> "<new text>".`
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
