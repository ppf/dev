package sidebar

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestModelGroup(t *testing.T) {
	for _, tt := range []struct{ name, project, want string }{
		{"app", "", "app"},
		{"app-2", "", "app"},
		{"app-2-3", "", "app-2"},
		{"proj-12-fix", "", "proj"},
		{"a-1b", "", "a"},
		{"app-v1.2", "", "app-v1.2"},
		{"app-feature-x", "", "app-feature-x"},
		{"app-", "", "app-"},
		{"-1", "", ""},
		{"app-2", "web", "web"},
	} {
		if got := Group(tt.name, tt.project); got != tt.want {
			t.Errorf("Group(%q, %q) = %q, want %q", tt.name, tt.project, got, tt.want)
		}
	}
}

func TestModelOrder(t *testing.T) {
	in := []Session{
		{Name: "app2", Group: "app2"},
		{Name: "zeta", Group: "app"},
		{Name: "app-2", Group: "app"},
		{Name: "app", Group: "app"},
		{Name: "App", Group: "App"},
	}
	got := Order(in)
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if want := []string{"App", "app", "app-2", "zeta", "app2"}; !slices.Equal(names, want) {
		t.Errorf("Order = %q, want %q", names, want)
	}
	if in[0].Name != "app2" {
		t.Error("Order sorted its argument in place")
	}
}

func TestModelLoadOrder(t *testing.T) {
	c, calls := recorder(func([]string) (string, error) {
		return tmux.FormatRow("web", "", "$web", "", "") + tmux.FormatRow("app-2", "", "$app-2", "", "") + tmux.FormatRow("x", "app", "$x", "", "") + tmux.FormatRow("app", "", "$app", "", ""), nil
	})
	names, err := LoadOrder(c)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app", "app-2", "x", "web"}; !slices.Equal(names, want) {
		t.Errorf("LoadOrder = %q, want %q", names, want)
	}
	if want := []string{"list-sessions", "-F", "#{session_name}\x1f#{@project}\x1f#{session_id}\x1f#{@dev_started_by}\x1f#{@dev_herd}\x1f"}; !slices.Equal((*calls)[0], want) {
		t.Errorf("args = %q, want %q", (*calls)[0], want)
	}
}

func TestOrderNestsWorkers(t *testing.T) {
	in := []Session{
		{Name: "zeta", Group: "zeta", ID: "$1"},
		{Name: "herd-app", Group: "app", ID: "$2"},
		{Name: "app", Group: "app", ID: "$3"},
		{Name: "api-fix", Group: "api", ID: "$4", StartedBy: "$2"},     // another group: moves under its herd
		{Name: "app-fix", Group: "app", ID: "$5", StartedBy: "$2"},     // same group
		{Name: "app-fix-sub", Group: "app", ID: "$6", StartedBy: "$5"}, // a worker's worker
		{Name: "orphan", Group: "orphan", ID: "$7", StartedBy: "$99"},  // starter gone: a plain row
		{Name: "loop-a", Group: "loop", ID: "$8", StartedBy: "$9"},     // a cycle: both flat
		{Name: "loop-b", Group: "loop", ID: "$9", StartedBy: "$8"},
	}
	var got []string
	for _, s := range Order(in) {
		got = append(got, fmt.Sprintf("%s/%s/%d", s.Name, s.Group, s.Depth))
	}
	want := []string{
		"app/app/0", "herd-app/app/0", "api-fix/app/1", "app-fix/app/1", "app-fix-sub/app/2",
		"orphan/orphan/0", "zeta/zeta/0", "loop-a/loop/0", "loop-b/loop/1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Order =\n%q\nwant\n%q", got, want)
	}
}

// A worker names its boss (it outlives the boss's id), or holds an older version's session id; a name
// counts only for a session marked as a boss.
func TestOrderNestsByBossName(t *testing.T) {
	in := []Session{
		{Name: "boss-app", Group: "boss-app", ID: "$10", Boss: true},
		{Name: "app", Group: "app", ID: "$3"},
		{Name: "app-fix", Group: "app", ID: "$5", StartedBy: "boss-app"},
		{Name: "app-old", Group: "app", ID: "$6", StartedBy: "$10"},
		{Name: "app-other", Group: "app", ID: "$7", StartedBy: "app"}, // app isn't a boss
		{Name: "app-gone", Group: "app", ID: "$8", StartedBy: "boss-web"},
	}
	var got []string
	for _, s := range Order(in) {
		got = append(got, fmt.Sprintf("%s/%d", s.Name, s.Depth))
	}
	want := []string{"app/0", "app-gone/0", "app-other/0", "boss-app/0", "app-fix/1", "app-old/1"}
	if !slices.Equal(got, want) {
		t.Errorf("Order =\n%q\nwant\n%q", got, want)
	}
}

func TestStarter(t *testing.T) {
	if got := Starter("$4", "boss-app", true); got != "boss-app" {
		t.Errorf("Starter of a boss = %q, want its name", got)
	}
	if got := Starter("$4", "app", false); got != "$4" {
		t.Errorf("Starter of a plain session = %q, want its id", got)
	}
	for _, tt := range []struct {
		v    string
		boss bool
		want bool
	}{{"boss-app", true, true}, {"$4", true, true}, {"$4", false, true}, {"boss-app", false, false}, {"", true, false}, {"$5", true, false}} {
		if got := Names(tt.v, "$4", "boss-app", tt.boss); got != tt.want {
			t.Errorf("Names(%q, boss=%v) = %v, want %v", tt.v, tt.boss, got, tt.want)
		}
	}
}
