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

// A note names the open item of a number first: a ticked one may share it (numbers restart); then a ticked or finished one.
func TestListItemState(t *testing.T) {
	l := List{Items: []Shown{{ID: 1, Text: "old", Done: true, Answer: "Merge"}, {ID: 1, Text: "new"}, {ID: 2, Done: true}},
		Finished: []Finished{{ID: 3, Text: "gone", Answer: "Wait"}}}
	for id, want := range map[int]string{1: "☐ on you", 2: "☑ done", 3: "☑ answered"} {
		if got, ok := l.ItemState(id); !ok || got != want {
			t.Errorf("#%d: %q %v, want %q", id, got, ok, want)
		}
	}
	if got, ok := l.ItemState(4); ok {
		t.Errorf("#4: %q, want none", got)
	}
}
