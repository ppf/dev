package sidebar

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The goldens are sidebar.sh's render output, captured by sourcing it into bash with tmux and tput
// stubbed and render_line recording each row.
func TestViewRenderGolden(t *testing.T) {
	tests := []struct {
		name  string
		m     Model
		lines []string
		rows  []rowStart
	}{
		{
			name: "groups, branches, agents, selection",
			m: Model{selected: "delta", snap: snapshot{width: 40, height: 24, focused: true, current: "alpha", sessions: []Session{
				{Name: "gamma", Group: "api", Branch: "main", Agent: "Claude", State: "working", Title: "Fix login"},
				{Name: "alpha", Group: "web", Branch: "feature/very-long-branch-name-here", Agent: "Claude", State: "waiting"},
				{Name: "beta", Group: "web"},
				{Name: "delta", Group: "web", Agent: "Codex", Title: "b on a"},
				// branch_max <= 3 drops the branch
				{Name: "averyveryverylongsessionnamethatgoeson", Group: "web", Branch: "x", Agent: "Claude", State: "idle", Title: "a very long title that will be cut short"},
			}}},
			lines: []string{
				" \x1b[38;2;192;163;110m\x1b[1mSESSIONS\x1b[0m                           \x1b[38;2;192;163;110m\x1b[1m+\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mapi\x1b[0m \x1b[38;2;84;84;109m──────────────────────────────\x1b[0m",
				"  \x1b[38;2;192;163;110m●\x1b[0m gamma                    \x1b[38;2;114;113;105m main\x1b[0m  \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;114;113;105mClaude · working · Fix login\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mweb\x1b[0m \x1b[38;2;84;84;109m──────────────────────────────\x1b[0m",
				"  \x1b[38;2;228;104;118m●\x1b[0m \x1b[1malpha\x1b[0m \x1b[38;2;114;113;105m feature/very-long-branc…\x1b[0m \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;228;104;118mClaude · waiting\x1b[0m",
				"  \x1b[38;2;84;84;109m○\x1b[0m beta                            \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;84;84;109mno agent\x1b[0m",
				"\x1b[48;2;42;42;55m  \x1b[38;2;114;113;105m●\x1b[0m\x1b[48;2;42;42;55m delta                           \x1b[38;2;228;104;118m×\x1b[0m\x1b[48;2;42;42;55m   \x1b[0m",
				"\x1b[48;2;42;42;55m    \x1b[38;2;114;113;105mCodex · b on a\x1b[0m\x1b[48;2;42;42;55m                      \x1b[0m",
				"  \x1b[38;2;152;187;108m●\x1b[0m averyveryverylongsessionnameth… \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;114;113;105mClaude · idle · a very long title …\x1b[0m",
				"", "", "", "", "", "", "", "",
				" \x1b[38;2;84;84;109mj/k ⏎  d new  x kill  r ren  q back\x1b[0m",
			},
			rows: []rowStart{{3, "gamma"}, {7, "alpha"}, {9, "beta"}, {11, "delta"}, {13, "averyveryverylongsessionnamethatgoeson"}},
		},
		{
			name: "14 columns: no buttons, selected current session",
			m: Model{selected: "beta", snap: snapshot{width: 14, height: 12, current: "beta", sessions: []Session{
				{Name: "alpha", Group: "web", Branch: "main", Agent: "Claude", State: "working", Title: "Fix"},
				{Name: "beta", Group: "web"},
			}}},
			lines: []string{
				" \x1b[38;2;192;163;110m\x1b[1mSESSIONS\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mweb\x1b[0m \x1b[38;2;84;84;109m────\x1b[0m",
				"  \x1b[38;2;192;163;110m●\x1b[0m alpha",
				"    \x1b[38;2;114;113;105mClaude ·…\x1b[0m",
				"\x1b[48;2;42;42;55m  \x1b[38;2;84;84;109m○\x1b[0m\x1b[48;2;42;42;55m \x1b[1mbeta\x1b[0m\x1b[48;2;42;42;55m      \x1b[0m",
				"\x1b[48;2;42;42;55m    \x1b[38;2;84;84;109mno agent\x1b[0m\x1b[48;2;42;42;55m  \x1b[0m",
				"", "", "", "", "",
			},
			rows: []rowStart{{3, "alpha"}, {5, "beta"}},
		},
		{
			name: "truncated group label gets one dash",
			m: Model{selected: "one", snap: snapshot{width: 20, height: 10, current: "x", sessions: []Session{
				{Name: "one", Group: "averylonggroupnamehere"},
				{Name: "two", Group: "short"},
			}}},
			lines: []string{
				" \x1b[38;2;192;163;110m\x1b[1mSESSIONS\x1b[0m       \x1b[38;2;192;163;110m\x1b[1m+\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105maverylonggrou…\x1b[0m \x1b[38;2;84;84;109m─\x1b[0m",
				"\x1b[48;2;42;42;55m  \x1b[38;2;84;84;109m○\x1b[0m\x1b[48;2;42;42;55m one         \x1b[38;2;228;104;118m×\x1b[0m\x1b[48;2;42;42;55m   \x1b[0m",
				"\x1b[48;2;42;42;55m    \x1b[38;2;84;84;109mno agent\x1b[0m\x1b[48;2;42;42;55m        \x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mshort\x1b[0m \x1b[38;2;84;84;109m────────\x1b[0m",
				"  \x1b[38;2;84;84;109m○\x1b[0m two         \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;84;84;109mno agent\x1b[0m",
				"",
			},
			rows: []rowStart{{3, "one"}, {7, "two"}},
		},
		{
			name: "no sessions: no hint even when focused",
			m:    Model{snap: snapshot{width: 40, height: 6, focused: true, current: "x"}},
			lines: []string{
				" \x1b[38;2;192;163;110m\x1b[1mSESSIONS\x1b[0m                           \x1b[38;2;192;163;110m\x1b[1m+\x1b[0m",
				"",
				" \x1b[38;2;114;113;105mNo sessions\x1b[0m",
				"", "", "",
			},
		},
		{
			name: "height below the content keeps the header and the hint",
			m: Model{selected: "alpha", snap: snapshot{width: 40, height: 5, focused: true, current: "alpha", sessions: []Session{
				{Name: "alpha", Group: "web"},
				{Name: "beta", Group: "web"},
			}}},
			lines: []string{
				" \x1b[38;2;192;163;110m\x1b[1mSESSIONS\x1b[0m                           \x1b[38;2;192;163;110m\x1b[1m+\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mweb\x1b[0m \x1b[38;2;84;84;109m──────────────────────────────\x1b[0m",
				"\x1b[48;2;42;42;55m  \x1b[38;2;84;84;109m○\x1b[0m\x1b[48;2;42;42;55m \x1b[1malpha\x1b[0m\x1b[48;2;42;42;55m                           \x1b[38;2;228;104;118m×\x1b[0m\x1b[48;2;42;42;55m   \x1b[0m",
				" \x1b[38;2;84;84;109mj/k ⏎  d new  x kill  r ren  q back\x1b[0m",
			},
			// Off-screen rows stay, as in bash's row map
			rows: []rowStart{{3, "alpha"}, {5, "beta"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.m.render()
			if len(f.lines) != len(tt.lines) {
				t.Errorf("%d lines, want %d", len(f.lines), len(tt.lines))
			}
			for i := range min(len(f.lines), len(tt.lines)) {
				if f.lines[i] != tt.lines[i] {
					t.Errorf("line %d:\n got %q\nwant %q", i, f.lines[i], tt.lines[i])
				}
			}
			if !slices.Equal(f.rows, tt.rows) {
				t.Errorf("rows = %v, want %v", f.rows, tt.rows)
			}
		})
	}
}

func TestViewUnfocusedHasNoHint(t *testing.T) {
	m := Model{selected: "a", snap: snapshot{width: 40, height: 8, current: "a", sessions: []Session{{Name: "a", Group: "g"}}}}
	if got := m.render().lines[7]; got != "" {
		t.Errorf("bottom row unfocused = %q, want empty", got)
	}
	m.snap.focused = true
	if got := m.render().lines[7]; got != hint {
		t.Errorf("bottom row focused = %q, want hint", got)
	}
}

func TestViewErrorRow(t *testing.T) {
	err := errors.New("tmux list-panes: exit status 1\nno server running on /tmp/tmux-501/x")
	for _, s := range [][]Session{nil, {{Name: "a", Group: "g"}}} {
		m := Model{err: err, selected: "a", snap: snapshot{width: 30, height: 6, focused: true, current: "a", sessions: s}}
		lines := m.render().lines
		if len(lines) != 6 {
			t.Fatalf("%d lines, want 6", len(lines))
		}
		// Newlines collapse so the error stays one row; cut to width-1 after the leading space
		if want := " " + red + "tmux list-panes: exit status…" + reset; lines[5] != want {
			t.Errorf("%d sessions: bottom row = %q, want %q", len(s), lines[5], want)
		}
	}
}

// Display-width math with wide runes: nothing overflows, a selected row fills the width exactly,
// and × stays in its column.
func TestViewWideRunes(t *testing.T) {
	m := Model{selected: "日本語のセッション", snap: snapshot{width: 40, height: 20, current: "x", sessions: []Session{
		{Name: "日本語のセッション", Group: "グループ名がとても長いグループですね", Branch: "機能/ブランチ😀😀", Agent: "Claude", State: "idle", Title: "タイトル😀がとても長くて切られるはずです"},
		{Name: "a😀b", Group: "グループ名がとても長いグループですね", Branch: "😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀", Agent: "Codex", Title: "x"},
		{Name: "とても長いセッションの名前がここにありますよ", Group: "z", Branch: "main"},
	}}}
	f := m.render()
	const btn = 36
	for _, r := range f.rows {
		line := f.lines[r.line]
		x := strings.LastIndex(line, "×")
		if col := ansi.StringWidth(line[:x]); col != btn {
			t.Errorf("%s: × at column %d, want %d: %q", r.session, col, btn, line)
		}
	}
	if !strings.Contains(f.lines[3], muted+" 機能/ブラ…"+reset) || !strings.Contains(f.lines[9], "とても長いセッションの名前がこ…") {
		t.Errorf("branch or name not cut by width:\n%q\n%q", f.lines[3], f.lines[9])
	}
	for i, line := range f.lines {
		w := ansi.StringWidth(line)
		if w > 40 {
			t.Errorf("line %d is %d columns wide: %q", i, w, line)
		}
		if strings.HasPrefix(line, selectedBG) && w != 40 {
			t.Errorf("selected line %d is %d columns wide, want 40: %q", i, w, line)
		}
	}
	// Cut at 33 of 34 columns, as a wide rune doesn't fit the last one: |40-7-33| = 0 dashes
	if want := " " + faint + "─" + reset + " " + muted + "グループ名がとても長いグループで…" + reset + " " + faint + reset; f.lines[2] != want {
		t.Errorf("rule = %q, want %q", f.lines[2], want)
	}
}

func TestViewTruncate(t *testing.T) {
	tests := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "…"},
		{"日本語", 6, "日本語"},
		{"日本語", 5, "日本…"},
		{"日本語", 4, "日…"},
		{"a😀b", 3, "a…"},
		{"", 3, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.s, tt.max)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
		}
		if w := ansi.StringWidth(got); tt.max > 0 && w > tt.max {
			t.Errorf("truncate(%q, %d) is %d columns", tt.s, tt.max, w)
		}
	}
}

func TestViewCardLine(t *testing.T) {
	content := "  " + bold + "name" + reset + " x"
	if got := cardLine(content, 8, 12, false); got != content {
		t.Errorf("unselected = %q, want unchanged", got)
	}
	want := selectedBG + "  " + bold + "name" + reset + selectedBG + " x" + "    " + reset
	if got := cardLine(content, 8, 12, true); got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
	if got := cardLine("abc", 5, 3, true); got != selectedBG+"abc"+reset {
		t.Errorf("overlong selected = %q, want no padding", got)
	}
}

func TestViewStatusDot(t *testing.T) {
	tests := []struct{ label, state, want string }{
		{"Claude", "working", gold + "●" + reset},
		{"Claude", "waiting", red + "●" + reset},
		{"Claude", "idle", green + "●" + reset},
		{"Codex", "", muted + "●" + reset},
		{"Claude", "bogus", muted + "●" + reset},
		{"", "", faint + "○" + reset},
	}
	for _, tt := range tests {
		if got := statusDot(tt.label, tt.state); got != tt.want {
			t.Errorf("statusDot(%q, %q) = %q, want %q", tt.label, tt.state, got, tt.want)
		}
	}
}

func TestViewBeforeFirstRefresh(t *testing.T) {
	if got := (Model{}).View(); got != "" {
		t.Errorf("View = %q, want empty", got)
	}
	// The actions read rows on input that can arrive before the first refresh
	if f := (Model{snap: snapshot{sessions: []Session{{Name: "a"}}}}).render(); len(f.lines) != 0 || len(f.rows) != 1 {
		t.Errorf("render at height 0 = %d lines, rows %v", len(f.lines), f.rows)
	}
}

// A first refresh that fails still shows its error, on the bottom row of the window size.
func TestViewErrorBeforeFirstRefresh(t *testing.T) {
	failed := refreshMsg{err: errors.New("tmux list-panes: 9 fields, want 14")}
	var m tea.Model = Model{}
	m, _ = m.Update(failed)
	if got := m.View(); got != "" {
		t.Errorf("without a window size: View = %q, want empty", got)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 4})
	if got, want := m.View(), "\n\n\n "+red+"tmux list-panes: 9 fields, w…"+reset; got != want {
		t.Errorf("View = %q, want %q", got, want)
	}
}

func TestViewHeight(t *testing.T) {
	var sessions []Session
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		sessions = append(sessions, Session{Name: n, Group: n})
	}
	for h := 1; h <= 30; h++ {
		m := Model{snap: snapshot{width: 30, height: h, focused: true, current: "a", sessions: sessions}}
		got := m.View()
		if n := strings.Count(got, "\n") + 1; n != h {
			t.Errorf("height %d: View has %d lines", h, n)
		}
		if h > 1 && !strings.HasPrefix(got, " "+gold+bold+"SESSIONS") {
			t.Errorf("height %d: header missing: %q", h, got)
		}
	}
}
