package todo

import "testing"

// A note names an item's mark and who it waits on as the list has them.
func TestShownState(t *testing.T) {
	for _, tt := range []struct {
		it   Shown
		want string
	}{
		{Shown{}, "☐ on you"},
		{Shown{On: Boss}, "☐ boss's own"},
		{Shown{On: "fix-ci", Session: &Session{ID: "fix-ci", Name: "fix-ci"}}, "◌ on fix-ci"},
		{Shown{On: "Ola"}, "◌ on Ola"},
		{Shown{Done: true, Answer: "Merge"}, "☑ answered"},
		{Shown{Done: true}, "☑ done"},
	} {
		if got := tt.it.State(); got != tt.want {
			t.Errorf("%+v: State = %q, want %q", tt.it, got, tt.want)
		}
	}
}

// A note names the open item of a number and text first: a ticked one may share both (numbers restart); then a ticked
// or finished one; another text is none.
func TestListItemState(t *testing.T) {
	l := List{Items: []Shown{{ID: 1, Text: "q", Done: true, Answer: "Merge"}, {ID: 1, Text: "q"}, {ID: 2, Text: "old", Done: true}},
		Finished: []Finished{{ID: 3, Text: "gone", Answer: "Wait"}}}
	for _, tt := range []struct {
		id         int
		text, want string
	}{{1, "q", "☐ on you"}, {2, "old", "☑ done"}, {3, "gone", "☑ answered"}} {
		if got, ok := l.ItemState(tt.id, tt.text); !ok || got != tt.want {
			t.Errorf("#%d %q: %q %v, want %q", tt.id, tt.text, got, ok, tt.want)
		}
	}
	for _, id := range []int{2, 4} {
		if got, ok := l.ItemState(id, "other"); ok {
			t.Errorf("#%d other: %q, want none", id, got)
		}
	}
}
