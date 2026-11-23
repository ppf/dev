package todo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// t3Store is T3 Code thread id's list, its boss in dir (WithDir).
func t3Store(t *testing.T, id, dir string) Store {
	t.Helper()
	s, err := T3(id)
	if err != nil {
		t.Fatal(err)
	}
	return s.WithDir(dir)
}

// tmuxStore is tmux boss name's list on the server at /tmp/tmux-1/default.
func tmuxStore(t *testing.T, name string) Store {
	t.Helper()
	s, err := open("/tmp/tmux-1/default-" + sessionKey(name))
	if err != nil {
		t.Fatal(err)
	}
	s.boss, s.name = name, name
	return s
}

// rawFile is s's file as stored.
func rawFile(t *testing.T, s Store) file {
	t.Helper()
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// A take-over into an empty list moves the list whole: items with every field, notes, PR checks and finished items,
// numbers kept; the destination's own ticked item stays among its finished ones; the source holds the moved marker.
func TestTakeOverIntoEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "49f62c8e-old", "/p/dev"), t3Store(t, "5f1c2d90-new", "/p/dev")
	prURL := "https://github.com/o/r/pull/7"
	full := Item{Text: "Build it", On: "branch-takeover", Session: &Session{ID: "branch-takeover", Name: "branch-takeover"}, Link: prURL,
		Until: now.Add(time.Hour).Unix(), DoneOn: "merged", Context: []string{"/tmp/contract.md"},
		Doing: &Doing{Text: "drafting", At: now.Unix()}, Started: &Started{At: now.Unix(), To: "boss-dev"}}
	for _, it := range []Item{{Text: "gone soon"}, full, {Text: "Merge?", Options: []string{"Merge (Recommended)", "Wait"}}} {
		if _, err := from.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := from.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.AddNote(Note{Kind: NoteDecided, Text: "picks A-G", Why: "user said"}, now); err != nil {
		t.Fatal(err)
	}
	if err := from.Lookup(now, LookupTTL, 0, func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }); err != nil {
		t.Fatal(err)
	}
	// The destination: its own ticked item, still shown
	if _, err := to.Add(Item{Text: "mine, ticked"}, now); err != nil {
		t.Fatal(err)
	}
	if err := to.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	before := read(t, from, later)

	taken, err := to.TakeOver(from, later)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{taken.Items[0].ID, taken.Items[1].ID}; len(taken.Items) != 2 || !slices.Equal(got, []int{2, 3}) || taken.Asks != 1 ||
		len(taken.Notes) != 1 || !slices.Equal(taken.OnWorkers, []string{"branch-takeover"}) {
		t.Errorf("taken = %+v", taken)
	}
	after := read(t, to, later)
	if !reflect.DeepEqual(after.Items, before.Items) {
		t.Errorf("items moved = %+v\nwant %+v", after.Items, before.Items)
	}
	if two := after.Items[slices.IndexFunc(after.Items, func(it Shown) bool { return it.ID == 2 })]; two.PRState != "open" || two.Doing == nil || two.Started == nil || two.Until != full.Until {
		t.Errorf("item 2 = %+v, want its PR check, doing, start and until kept", two)
	}
	var finished []string
	for _, f := range after.Finished {
		finished = append(finished, f.Text)
	}
	slices.Sort(finished)
	if !slices.Equal(finished, []string{"gone soon", "mine, ticked"}) {
		t.Errorf("finished = %q, want both lists'", finished)
	}
	if notes, err := to.Notes(later); err != nil || len(notes) != 1 || notes[0].ID != 1 || notes[0].Why != "user said" {
		t.Errorf("notes = %+v, %v", notes, err)
	}
	if f := rawFile(t, to); f.Dir != "/p/dev" || f.Boss != "" || f.MovedTo != "" || f.Next != 4 {
		t.Errorf("destination file = %+v, want its own dir, next 4", f)
	}
	marker := rawFile(t, from)
	if len(marker.Items) != 0 || marker.MovedTo != "t3-5f1c2d90-new" || marker.MovedToName != "t3:5f1c2d90" || marker.MovedAt != later.Unix() {
		t.Errorf("marker = %+v", marker)
	}
	if _, err := os.Stat(from.checksPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("source checks left: %v", err)
	}
	if l := read(t, from, later); len(l.Items) != 0 || len(l.Finished) != 0 || l.Moved != "list moved to t3:5f1c2d90 at "+later.Format("15:04")+" (dev todo take-over)" {
		t.Errorf("source read = %+v", l)
	}
	if _, err := to.TakeOver(from, later); err == nil || !strings.Contains(err.Error(), "t3:49f62c8e moved to t3:5f1c2d90 at") {
		t.Errorf("second take: %v, want moved", err)
	}
}

// A take-over is refused, nothing moved, from its own list and an empty one.
func TestTakeOverRefused(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to, empty := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev"), t3Store(t, "cccccccc-3", "/p/dev")
	if _, err := from.Add(Item{Text: "theirs"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Add(Item{Text: "ticked"}, now); err != nil {
		t.Fatal(err)
	}
	if err := empty.Done([]int{1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(to, now); err == nil || err.Error() != "t3:bbbbbbbb is this boss's own list" {
		t.Errorf("own: %v", err)
	}
	if _, err := to.TakeOver(empty, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "t3:cccccccc is empty (no open items, no notes)") {
		t.Errorf("empty: %v", err)
	}
	if got := ids(read(t, from, now)); !slices.Equal(got, []string{"1 theirs"}) {
		t.Errorf("source after refusals = %q, want it kept", got)
	}
}

// Writes to a moved list are refused, naming where it went: add, done, replace, ask's answer, notes, reopen. A tmux
// boss session created after the move writes a fresh list over the marker; one created before is refused.
func TestMovedListRefusesWrites(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := tmuxStore(t, "boss-dev"), t3Store(t, "5f1c2d90-new", "/p/dev")
	if _, err := from.Add(Item{Text: "pick?", Options: []string{"A", "B"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "ticked"}, now); err != nil {
		t.Fatal(err)
	}
	if err := from.Done([]int{2}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, now); err != nil {
		t.Fatal(err)
	}
	if f := rawFile(t, from); f.Boss != "boss-dev" {
		t.Errorf("marker's boss = %q, want boss-dev kept", f.Boss)
	}
	for name, write := range map[string]func() error{
		"add":     func() error { _, err := from.Add(Item{Text: "Merge #282"}, now); return err },
		"done":    func() error { return from.Done([]int{1}, now) },
		"replace": func() error { return from.Replace(1, Item{Text: "x"}, now) },
		"answer":  func() error { _, err := from.Answer(1, "pick?", "A", 0, now); return err },
		"note":    func() error { _, err := from.AddNote(Note{Kind: NoteRisk, Text: "x"}, now); return err },
		"drop":    func() error { return from.DropNote(1, now) },
		"reopen":  func() error { _, err := from.Reopen(2, "ticked", now); return err },
		"clear":   func() error { return from.Clear(now) },
	} {
		err := write()
		want := "list moved to t3:5f1c2d90 at " + atTime(now.Unix(), time.Now()) + " (dev todo take-over): this boss's to do list is there now; take it back there with dev todo take-over t3:5f1c2d90"
		if err == nil || err.Error() != want || !errors.As(err, new(Moved)) || !errors.As(err, new(Refused)) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	reopened := func(created int64) Store {
		s, err := ForPane(tmux.NewFake(func([]string) (string, error) {
			return tmux.FormatRow("/tmp/tmux-1/default", "boss-dev", "1", "$9", strconv.FormatInt(created, 10)), nil
		}), "%1")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, err := reopened(now.Unix()-1).Add(Item{Text: "x"}, now); !errors.As(err, new(Moved)) {
		t.Errorf("a session older than the move: %v, want refused", err)
	}
	// The same second counts as after: the integration test closes, takes and reopens within one
	fresh := reopened(now.Unix())
	if l := read(t, fresh, now); len(l.Items) != 0 || l.Moved != "" {
		t.Errorf("reopened boss reads %+v, want a fresh empty list", l)
	}
	if id, err := fresh.Add(Item{Text: "new start"}, now); err != nil || id != 1 {
		t.Errorf("reopened boss's add = %d, %v", id, err)
	}
	if f := rawFile(t, fresh); f.MovedTo != "" || len(f.Items) != 1 {
		t.Errorf("reopened boss's file = %+v, want the marker gone", f)
	}
	if got := ids(read(t, to, now.Add(time.Minute))); !slices.Equal(got, []string{"1 pick?"}) {
		t.Errorf("taken list = %q", got)
	}
}

// Candidates: each kind's project (tmux by name, desktop by its folder, T3 Code by its dir), the boss's state, why
// one can't be taken; newest change first. The listing leaves out its own list, moved and empty ones; another
// project's and one of no known project only with anyProject.
func TestCandidates(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	own := t3Store(t, "5f1c2d90-own", "/p/dev")
	add := func(s Store, text string, when time.Time) {
		t.Helper()
		if _, err := s.Add(Item{Text: text}, when); err != nil {
			t.Fatal(err)
		}
	}
	old := t3Store(t, "49f62c8e-old", "/p/dev")
	add(old, "a", at(1))
	add(old, "b?", at(1))
	if _, err := old.AddNote(Note{Kind: NoteRisk, Text: "r"}, at(9)); err != nil {
		t.Fatal(err)
	}
	add(tmuxStore(t, "boss-dev"), "running's", at(2))
	add(tmuxStore(t, "boss-vitay"), "vitay's", at(3))
	add(tmuxStore(t, sidebar.Manager), "manager's", at(4))
	desk, _ := Desktop("local_a6eb4b8b-773e")
	add(desk, "desktop's", at(5))
	running, _ := Desktop("local_running1")
	add(running, "busy", at(6))
	noDir, _ := T3("0a8cec22-nodir")
	add(noDir, "older dev's", at(7))
	add(t3Store(t, "deadbeef-other", "/p/vitay"), "other project", at(8))
	emptied := t3Store(t, "eeeeeeee-empty", "/p/dev")
	add(emptied, "x", at(1))
	if err := emptied.Done([]int{1}, at(1)); err != nil {
		t.Fatal(err)
	}
	moved, into := t3Store(t, "11111111-moved", "/p/dev"), t3Store(t, "22222222-into", "/p/dev")
	add(moved, "taken", at(1))
	if _, err := into.TakeOver(moved, at(1)); err != nil {
		t.Fatal(err)
	}
	// Not this server's: another socket's, a worker session's name
	add(Store{path: filepath.Join(filepath.Dir(own.path), "tmp_tmux-1_other-boss-dev.json"), boss: "boss-dev"}, "other server", at(1))
	add(Store{path: filepath.Join(filepath.Dir(own.path), "tmp_tmux-1_default-worker.json"), boss: "worker"}, "a worker's", at(1))

	looks := Looks{
		Socket:   "/tmp/tmux-1/default",
		Sessions: func() ([]string, error) { return []string{"boss-dev", "worker"}, nil },
		Desktop: func(ids []string) (map[string]DesktopState, error) {
			return map[string]DesktopState{"local_a6eb4b8b-773e": {Dir: "/p/dev/sub"}, "local_running1": {Dir: "/p/dev", Running: true}}, nil
		},
		Project: func(dir string) string { return strings.Split(strings.TrimPrefix(dir, "/p/"), "/")[0] },
	}
	type row struct{ list, kind, project, state, why string }
	got := func(anyProject bool) (rows []row, others []string) {
		t.Helper()
		cands, err := Candidates(own, "dev", looks, anyProject, at(10))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cands {
			r := row{c.Short(), c.Kind, c.Project, c.State, c.WhyNot}
			if c.Takeable != (c.WhyNot == "") || c.Takeable != (c.Refusal() == nil) {
				t.Errorf("%s: takeable %v, why %q, refusal %v", c.List, c.Takeable, c.WhyNot, c.Refusal())
			}
			switch {
			case c.Listed || anyProject && c.Other:
				rows = append(rows, r)
			case c.Other:
				others = append(others, c.List)
			}
		}
		return rows, others
	}
	rows, others := got(false)
	want := []row{
		{"t3:49f62c8e", "t3", "dev", "unknown", ""},
		{"local_running1", "desktop", "dev", "running", "running: close it first"},
		{"local_a6eb4b8b", "desktop", "dev", "stopped", ""},
		{"boss-dev", "tmux", "dev", "running", "running: close it first"},
		{"t3:22222222", "t3", "dev", "unknown", ""},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v\nwant %+v", rows, want)
	}
	if want := []string{"t3:deadbeef", "t3:0a8cec22", "manager", "boss-vitay"}; !slices.Equal(others, want) {
		t.Errorf("others = %q, want %q", others, want)
	}
	rows, _ = got(true)
	if len(rows) != 9 || rows[1] != (row{"t3:deadbeef", "t3", "vitay", "unknown", ""}) || rows[2] != (row{"t3:0a8cec22", "t3", "", "unknown", ""}) {
		t.Errorf("any project rows = %+v", rows)
	}

	cands, err := Candidates(own, "dev", looks, false, at(10))
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	for _, c := range cands {
		if c.Refusal() != nil {
			why[c.List] = c.Refusal().Error()
		}
	}
	for list, want := range map[string]string{
		"t3:5f1c2d90":    "t3:5f1c2d90 is this boss's own list",
		"t3:11111111":    "t3:11111111 moved to t3:22222222 at " + at(1).Format("15:04") + ": nothing left to take",
		"t3:eeeeeeee":    "t3:eeeeeeee is empty (no open items, no notes): nothing to take",
		"boss-dev":       "boss-dev is running: close it first (its manager's ×, or /exit in it), then take it over",
		"local_running1": "local_running1 is running: close it first (/exit in it), then take it over",
		"boss-vitay":     "boss-vitay is project vitay, not dev: --any-project takes it",
		"t3:0a8cec22":    "t3:0a8cec22 is of no known project, not dev: --any-project takes it",
		sidebar.Manager:  "manager is the manager's list, not dev's: --any-project takes it",
	} {
		if why[list] != want {
			t.Errorf("%s refused %q, want %q", list, why[list], want)
		}
	}
	if i := slices.IndexFunc(cands, func(c Candidate) bool { return c.List == "t3:49f62c8e" }); cands[i].Updated != at(9).Unix() || cands[i].Open != 2 || cands[i].Notes != 1 {
		t.Errorf("old list = %+v, want its note's time as its change", cands[i])
	}
	// Its own list without a file yet is still known as its own
	fresh := t3Store(t, "99999999-fresh", "/p/dev")
	cands, err = Candidates(fresh, "dev", looks, false, at(10))
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(cands, func(c Candidate) bool { return c.List == "t3:99999999" }); i < 0 || cands[i].WhyNot != "this boss's own list" {
		t.Errorf("own list with no file missing: %+v", cands)
	}
}

// An older dev's item on its boss's own name stays the
// user's once moved; a desktop app's adopt leaves a moved marker where it is.
func TestTakeOverEdges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := tmuxStore(t, "boss-dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	if _, err := from.Add(Item{Text: "legacy", On: "boss-dev"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if _, err := to.TakeOver(from, later); err != nil {
		t.Fatal(err)
	}
	if l := read(t, to, later); len(l.Items) != 2 || l.Items[1].Text != "legacy" || l.Items[1].On != "" {
		t.Errorf("legacy item = %+v, want the user's", l.Items)
	}

	desk, _ := Desktop("local_new")
	if adopted, err := desk.take(from); err != nil || adopted {
		t.Errorf("adopt of a marker: %v %v, want none", adopted, err)
	}
	if _, err := os.Stat(desk.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("adopting session got a file: %v", err)
	}
	if got := T3Name("mcp:0c638555-1111"); got != "t3:mcp_0c63" {
		t.Errorf("T3Name of an mcp id = %q", got)
	}
}

// A list not read is listed with why; a boss state not read refuses, listed; this boss's own list not empty refuses
// every row.
func TestCandidatesRefusals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	own := t3Store(t, "5f1c2d90-own", "/p/dev")
	if _, err := tmuxStore(t, "boss-dev").Add(Item{Text: "x"}, now); err != nil {
		t.Fatal(err)
	}
	desk, _ := Desktop("local_a")
	if _, err := desk.Add(Item{Text: "y"}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(own.path), "t3-broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	failing := errors.New("boom")
	looks := Looks{
		Socket:   "/tmp/tmux-1/default",
		Sessions: func() ([]string, error) { return nil, failing },
		Desktop:  func([]string) (map[string]DesktopState, error) { return nil, failing },
		Project:  filepath.Base,
	}
	why := func() map[string]string {
		t.Helper()
		cands, err := Candidates(own, "dev", looks, false, now)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range cands {
			if c.Listed {
				out[c.List] = c.WhyNot
				if c.Takeable {
					t.Errorf("%s takeable", c.List)
				}
			}
		}
		return out
	}
	got := why()
	if got["boss-dev"] != "state not read: boom" || got["local_a"] != "state not read: boom" || !strings.HasPrefix(got["t3:broken"], "not read: ") {
		t.Errorf("rows = %q", got)
	}
}

// A take-over into a list with items or notes merges: the taken items after its own, renumbered, the link between two
// halves, a note's item and a PR check following; notes after its own, renumbered, the older of two state notes dropped;
// finished items joined; the source holds the marker, the destination the taken stamp.
func TestTakeOverMerge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
		t.Fatal(err)
	}
	for _, n := range []Note{{Kind: NoteState, Text: "my state"}, {Kind: NoteRisk, Text: "my risk"}} {
		if _, err := to.AddNote(n, now); err != nil {
			t.Fatal(err)
		}
	}
	prURL := "https://github.com/o/r/pull/7"
	for _, it := range []Item{{Text: "a", Link: prURL}, {Text: "b", PartOf: 1, On: "w1", Session: &Session{ID: "w1", Name: "w1"}}, {Text: "pick?", Options: []string{"A", "B"}, For: 1}, {Text: "t"}} {
		if _, err := from.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := from.Done([]int{4}, now); err != nil {
		t.Fatal(err)
	}
	if err := from.Lookup(now, LookupTTL, 0, func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := from.AddNote(Note{Kind: NoteDecided, Text: "b's way", Item: 2}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.AddNote(Note{Kind: NoteState, Text: "their state"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)

	taken, err := to.TakeOver(from, later)
	if err != nil {
		t.Fatal(err)
	}
	var moves []string
	for _, it := range taken.Items {
		moves = append(moves, strconv.Itoa(it.From)+">"+strconv.Itoa(it.ID))
	}
	for _, n := range taken.Notes {
		moves = append(moves, "n"+strconv.Itoa(n.From)+">n"+strconv.Itoa(n.ID))
	}
	if !slices.Equal(moves, []string{"1>2", "2>3", "3>4", "n1>n3", "n2>n4"}) || !taken.Merged || taken.OwnItems != 1 || taken.OwnNotes != 2 ||
		taken.Asks != 1 || !slices.Equal(taken.OnWorkers, []string{"w1"}) || taken.DroppedState == nil || taken.DroppedState.Text != "my state" {
		t.Errorf("taken = %v %+v", moves, taken)
	}
	l := read(t, to, later)
	byText := map[string]Shown{}
	for _, it := range l.Items {
		byText[it.Text] = it
	}
	if got := []int{byText["mine"].ID, byText["a"].ID, byText["b"].ID, byText["pick?"].ID}; !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("ids = %v", got)
	}
	if byText["b"].PartOf != 2 || byText["a"].PartOf != 3 || byText["a"].PRState != "open" || len(byText["pick?"].Options) != 2 ||
		byText["pick?"].For != 2 || byText["a"].WaitsOn == nil || byText["a"].WaitsOn.ID != 4 || byText["mine"].WaitsOn != nil {
		t.Errorf("merged items = %+v", l.Items)
	}
	if len(l.Finished) != 1 || l.Finished[0].Text != "t" {
		t.Errorf("finished = %+v", l.Finished)
	}
	notes, err := to.Notes(later)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range notes {
		got = append(got, "n"+strconv.Itoa(n.ID)+" "+n.Text+" #"+strconv.Itoa(n.Item))
	}
	if !slices.Equal(got, []string{"n2 my risk #0", "n3 b's way #3", "n4 their state #0"}) {
		t.Errorf("notes = %q", got)
	}
	if f := rawFile(t, to); f.Next != 5 || f.NextNote != 5 || f.TakenFrom != "t3-aaaaaaaa-1" || f.TakenAt != later.Unix() {
		t.Errorf("destination file = %+v", f)
	}
	if f := rawFile(t, from); f.MovedTo != "t3-bbbbbbbb-2" || len(f.Items) != 0 || len(f.Notes) != 0 {
		t.Errorf("marker = %+v", f)
	}
	for _, path := range []string{from.checksPath(), from.donePath()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left: %v", path, err)
		}
	}
	if id, err := to.Add(Item{Text: "next"}, later); err != nil || id != 5 {
		t.Errorf("next add = %d, %v, want 5", id, err)
	}
}

// Over MaxNotes together is refused, both files as they were; two state notes count once, the destination's kept
// when not older.
func TestTakeOverMergeNoteCap(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	add := func(s Store, n int) {
		for i := range n {
			if _, err := s.AddNote(Note{Kind: NoteRisk, Text: "r" + strconv.Itoa(i)}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(to, 7)
	add(from, 6)
	before := [][]byte{}
	for _, s := range []Store{from, to} {
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, data)
	}
	_, err := to.TakeOver(from, now)
	if err == nil || err.Error() != "taking t3:aaaaaaaa makes 13 notes, at most 12: drop 1 here first (dev todo note drop <n>); nothing moved" || !errors.As(err, new(Refused)) {
		t.Errorf("over the cap: %v", err)
	}
	for i, s := range []Store{from, to} {
		if data, _ := os.ReadFile(s.path); string(data) != string(before[i]) {
			t.Errorf("%s changed after the refusal", s.Name())
		}
	}
	for _, s := range []Store{from, to} {
		if err := s.DropNote(1, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddNote(Note{Kind: NoteState, Text: s.Name()}, now); err != nil {
			t.Fatal(err)
		}
	}
	if taken, err := to.TakeOver(from, now); err != nil || len(taken.Notes) != 5 || taken.DroppedState == nil || taken.DroppedState.Text != from.Name() {
		t.Errorf("12, the source's state note dropped (not newer): %+v, %v", taken, err)
	}
}

// A take that stopped before the marker, a merge or a move: its retry writes only the marker, nothing taken twice, the
// PR checks and finished items already beside the destination. One changed since is refused; a fresh list under the
// same name since is taken again.
func TestTakeOverRetry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	later := now.Add(time.Minute)
	prURL := "https://github.com/o/r/pull/7"
	for _, merge := range []bool{true, false} {
		from, to := tmuxStore(t, "boss-dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
		for _, s := range []Store{from, to} {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		want := []string{"1 theirs"}
		if merge {
			if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
				t.Fatal(err)
			}
			want = []string{"1 mine", "2 theirs"}
		}
		for _, it := range []Item{{Text: "theirs", Link: prURL}, {Text: "finished"}} {
			if _, err := from.Add(it, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := from.Done([]int{2}, now); err != nil {
			t.Fatal(err)
		}
		if err := from.Lookup(now, LookupTTL, 0, func(string) (sidebar.PRView, error) { return sidebar.PRView{State: "OPEN"}, nil }); err != nil {
			t.Fatal(err)
		}
		saved := map[string][]byte{}
		for _, path := range []string{from.path, from.checksPath(), from.donePath()} {
			data, err := os.ReadFile(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				t.Fatal(err)
			default:
				saved[path] = data
			}
		}
		if _, ok := saved[from.checksPath()]; !ok {
			t.Fatal("no PR checks beside the source")
		}
		if _, err := to.TakeOver(from, later); err != nil {
			t.Fatal(err)
		}
		// As if it stopped before the marker: the list and what lives beside it still there
		for path, data := range saved {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := to.TakeOver(from, later.Add(time.Minute))
		if err == nil || err.Error() != "boss-dev taken at "+later.Format("15:04")+": dev todo shows it" {
			t.Errorf("merge %v: retry: %v", merge, err)
		}
		l := read(t, to, later)
		if got := ids(l); !slices.Equal(got, want) {
			t.Errorf("merge %v: destination = %q, want each once", merge, got)
		}
		if i := slices.IndexFunc(l.Items, func(it Shown) bool { return it.Text == "theirs" }); i < 0 || l.Items[i].PRState != "open" || len(l.Finished) != 1 || l.Finished[0].Text != "finished" {
			t.Errorf("merge %v: destination = %+v, want the PR check and the finished item", merge, l)
		}
		if f := rawFile(t, from); f.MovedTo != "t3-bbbbbbbb-2" {
			t.Errorf("merge %v: source = %+v, want the marker", merge, f)
		}
		for _, path := range []string{from.checksPath(), from.donePath()} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("merge %v: %s left after the retry: %v", merge, path, err)
			}
		}
	}

	// Changed before its marker: refused, both kept
	from, to := tmuxStore(t, "boss-dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	// Its list as it was, the marker not written
	f := rawFile(t, from)
	f.movedMarker = movedMarker{}
	f.Items, f.Next = []Item{{ID: 1, Text: "theirs", Added: now.Unix()}}, 2
	if err := from.write(f); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "since"}, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, later.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "changed before its marker was written") {
		t.Errorf("changed since: %v", err)
	}
	if got := ids(read(t, from, later)); !slices.Equal(got, []string{"1 theirs", "2 since"}) {
		t.Errorf("source after the refusal = %q", got)
	}

	// A dev boss reopened under the name writes a fresh list over the marker
	if err := os.WriteFile(from.path, []byte(`{"moved_to":"x","moved_to_name":"x","moved_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := from
	fresh.created = later.Add(time.Hour).Unix()
	again := later.Add(2 * time.Hour)
	if _, err := fresh.Add(Item{Text: "new"}, again); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, again); err != nil {
		t.Fatal(err)
	}
	if got := ids(read(t, to, again)); !slices.Equal(got, []string{"1 theirs", "3 new"}) {
		t.Errorf("after the second take = %q", got)
	}
}

// A note on, and a tick of, an item that left the source's list lose its number on a merge: it may be one of the
// destination's.
func TestTakeOverMergeGoneItem(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "dated", Until: now.Add(time.Minute).Unix()}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.AddNote(Note{Kind: NoteDecided, Text: "on dated", Item: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "open"}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if _, err := to.TakeOver(from, later); err != nil {
		t.Fatal(err)
	}
	notes, err := to.Notes(later)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Item != 0 || !notes[0].ItemDone || !strings.Contains(NotesText(notes, later), "on dated (its item done)") {
		t.Errorf("notes = %+v", notes)
	}
	ticked, err := to.TakeTicked(later, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticked) != 1 || ticked[0].String() != `"dated" ticked by itself: its --until time came` {
		t.Errorf("ticked = %+v", ticked)
	}
}

// Empty: no open items, no notes, not moved.
func TestEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	empty := func(s Store) bool {
		t.Helper()
		ok, err := s.Empty(now)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !empty(to) {
		t.Error("no file: not empty")
	}
	if _, err := from.AddNote(Note{Kind: NoteRisk, Text: "r"}, now); err != nil {
		t.Fatal(err)
	}
	if empty(from) {
		t.Error("a note: empty")
	}
	if _, err := to.TakeOver(from, now); err != nil {
		t.Fatal(err)
	}
	if empty(from) {
		t.Error("moved: empty")
	}
}

// A retry is refused once the source changed since the take with nothing added (a tick); a source emptied since is
// refused as empty, no marker written over it.
func TestTakeOverRetryChanged(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	later := now.Add(time.Minute)
	from, to := tmuxStore(t, "boss-dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"a", "b"} {
		if _, err := from.Add(Item{Text: text}, now); err != nil {
			t.Fatal(err)
		}
	}
	src, err := os.ReadFile(from.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, later); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(from.path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := from.Done([]int{1}, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, later.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "changed before its marker was written") {
		t.Errorf("ticked since: %v", err)
	}
	if err := from.Done([]int{2}, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := to.TakeOver(from, later.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("emptied since: %v", err)
	}
	if f := rawFile(t, from); f.MovedTo != "" {
		t.Errorf("emptied source got a marker: %+v", f)
	}
}

// A tick of an item whose number an item added since has, on a merge: the tick loses the number, the new item keeps it.
func TestTakeOverMergeReusedNumber(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	from, to := t3Store(t, "aaaaaaaa-1", "/p/dev"), t3Store(t, "bbbbbbbb-2", "/p/dev")
	if _, err := to.Add(Item{Text: "mine"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := from.Add(Item{Text: "dated", Until: now.Add(time.Minute).Unix()}, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if id, err := from.Add(Item{Text: "reused"}, later); err != nil || id != 1 {
		t.Fatalf("add after the list emptied = %d, %v, want 1", id, err)
	}
	if _, err := to.TakeOver(from, later); err != nil {
		t.Fatal(err)
	}
	ticked, err := to.TakeTicked(later, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticked) != 1 || ticked[0].ID != 0 || ticked[0].Text != "dated" {
		t.Errorf("ticked = %+v, want dated with no number", ticked)
	}
}
