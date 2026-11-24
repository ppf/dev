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
