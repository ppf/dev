package sidebar

import "testing"

// Goldens from sidebar.sh's normalize_pane_title (perl substitutions plus the bash =~) under en_US.UTF-8.
func TestModelCleanTitle(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"\x1b[1;32mgreen\x1b[0m", "green"},
		{"\x1b[?25lfoo", "foo"},
		{"^[[A up", " up"},
		{"[[1;5Ahi", "hi"},
		{"[OAx", "x"},
		{"a\x01b\x7fc\tq", "abcq"},
		{"✳ Claude", "Claude"},
		{"⠂ task", "task"},
		{"✳", "✳"},
		{"✳ x", "x"},
		{"✳  ", " "},
		{"✳ ✳ y", "✳ y"},
		{"[OK] done", "done"},
		{"é task", "é task"},
		{"1 task", "1 task"},
		{"٣ x", "٣ x"},
		{"中 x", "中 x"},
		{"² sq", "sq"},
		{"Ⅻ x", "x"},
		{"_ x", "x"},
		{"  two", "two"},
		{"- bar", "bar"},
		{"-bar", "-bar"},
		{"a | b | c", "a | b | c"},
	} {
		if got := cleanTitle(tt.in); got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestModelIsGenericTitle(t *testing.T) {
	for _, tt := range []struct {
		title string
		want  bool
	}{
		{"", true},
		{"tmux-session-sidebar", true},
		{"terminal", true},
		{"Agent", true},
		{"Claude Code", true},
		{"app", true},
		{"box.local", true},
		{"mac.home", false},
		{"app-2", false},
		{"Fix login", false},
	} {
		if got := isGenericTitle(tt.title, "app", "box.local"); got != tt.want {
			t.Errorf("isGenericTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestModelAgentTitle(t *testing.T) {
	for _, tt := range []struct{ label, title, want string }{
		{"Codex", "repo | task", "task on repo"},
		{"Codex", "a | b | c", "c on a | b"},
		{"Codex", "a|b", "a|b"},
		{"Codex", "", ""},
		{"Claude", "repo | task", "repo | task"},
	} {
		if got := agentTitle(tt.label, tt.title); got != tt.want {
			t.Errorf("agentTitle(%q, %q) = %q, want %q", tt.label, tt.title, got, tt.want)
		}
	}
}
