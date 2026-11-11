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

// A dev boss session's panes share its list, keyed by socket, session id and creation time; another
// session's pane is refused.
func TestForPane(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	herd := map[string]string{"%1": "1", "%2": "1", "%3": ""}
	c := tmux.NewFake(func(args []string) (string, error) {
		return tmux.FormatRow("/tmp/tmux-1/default", "$1", "1700000000", "boss", herd[args[3]]), nil
	})
	a, err := ForPane(c, "%1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := ForPane(c, "%2"); b != a {
		t.Errorf("%%2's store %v, want %%1's %v", b, a)
	}
	if !strings.HasSuffix(a.path, "/todo/tmp_tmux-1_default-_1-1700000000.json") {
		t.Errorf("path = %s", a.path)
	}
	if _, err := ForPane(c, "%3"); err == nil || !errors.Is(err, ErrNotBoss) || !strings.HasPrefix(err.Error(), "boss is not a boss session") {
		t.Errorf("err = %v, want not a boss session", err)
	}
}

func TestClean(t *testing.T) {
	if got := Clean(" a\tb\n c\x1b[31m d  "); got != "a b c [31m d" {
		t.Errorf("Clean = %q", got)
	}
}
