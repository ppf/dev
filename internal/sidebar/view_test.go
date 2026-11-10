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
				"    \x1b[38;2;114;113;105mworking · Fix login\x1b[0m",
				"",
				" \x1b[38;2;84;84;109m─\x1b[0m \x1b[38;2;114;113;105mweb\x1b[0m \x1b[38;2;84;84;109m──────────────────────────────\x1b[0m",
				"  \x1b[38;2;228;104;118m●\x1b[0m \x1b[1malpha\x1b[0m \x1b[38;2;114;113;105m feature/very-long-branc…\x1b[0m \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;228;104;118mwaiting\x1b[0m",
				"  \x1b[38;2;84;84;109m○\x1b[0m beta                            \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;84;84;109mno agent\x1b[0m",
				"\x1b[48;2;42;42;55m  \x1b[38;2;114;113;105m●\x1b[0m\x1b[48;2;42;42;55m delta                           \x1b[38;2;228;104;118m×\x1b[0m\x1b[48;2;42;42;55m   \x1b[0m",
				"\x1b[48;2;42;42;55m    \x1b[38;2;114;113;105mCodex · b on a\x1b[0m\x1b[48;2;42;42;55m                      \x1b[0m",
				"  \x1b[38;2;152;187;108m●\x1b[0m averyveryverylongsessionnameth… \x1b[38;2;84;84;109m×\x1b[0m",
				"    \x1b[38;2;114;113;105midle · a very long title that will…\x1b[0m",
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
				"    \x1b[38;2;114;113;105mworking …\x1b[0m",
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
	if !strings.Contains(f.lines[3], muted+" 機能/ブラ…"+reset) || !strings.Contains(f.lines[f.rows[2].line], "とても長いセッションの名前がこ…") {
		t.Errorf("branch or name not cut by width:\n%q\n%q", f.lines[3], f.lines[f.rows[2].line])
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

// A PR tag leads the meta line in its state's colour and is cut last; its columns are a link.
func TestViewPRTag(t *testing.T) {
	pr := PR{Number: 12, State: "open", URL: "https://x/pull/12"}
	m := Model{selected: "b", snap: snapshot{width: 40, height: 10, current: "a", sessions: []Session{
		{Name: "a", Group: "g", Branch: "feat", Agent: "Claude", State: "idle", Title: "a long title that gets cut at the end", PR: pr},
		{Name: "b", Group: "g", Branch: "fix", Agent: "Claude", PR: PR{Number: 3, State: "draft", URL: "u3"}},
		{Name: "c", Group: "g", Branch: "main", Agent: "Claude", State: "waiting", PR: PR{Number: 9, State: "merged", URL: "u9"}},
	}}}
	f := m.render()
	for i, want := range map[int]string{
		4: "    \x1b[38;2;152;187;108m#12 open\x1b[0m\x1b[38;2;114;113;105m · idle · a long title tha…\x1b[0m",
		6: "\x1b[48;2;42;42;55m    \x1b[38;2;84;84;109m#3 draft\x1b[0m\x1b[48;2;42;42;55m\x1b[38;2;114;113;105m · Claude\x1b[0m\x1b[48;2;42;42;55m                   \x1b[0m",
		8: "    \x1b[38;2;192;163;110m#9 merged\x1b[0m\x1b[38;2;228;104;118m · waiting\x1b[0m",
	} {
		if f.lines[i] != want {
			t.Errorf("line %d:\n got %q\nwant %q", i, f.lines[i], want)
		}
	}
	if w := ansi.StringWidth(f.lines[4]); w != 39 {
		t.Errorf("meta line width %d, want 4+maxMeta = 39", w)
	}
	want := []link{{4, 4, 12, "https://x/pull/12"}, {6, 4, 12, "u3"}, {8, 4, 13, "u9"}}
	if !slices.Equal(f.links, want) {
		t.Errorf("links = %v, want %v", f.links, want)
	}

	// 11 columns: the tag itself is cut, nothing follows it
	m.snap.width = 11
	f = m.render()
	if got, want := f.lines[4], "    \x1b[38;2;152;187;108m#12 o…\x1b[0m\x1b[38;2;114;113;105m\x1b[0m"; got != want {
		t.Errorf("narrow:\n got %q\nwant %q", got, want)
	}
	if f.links[0] != (link{4, 4, 10, "https://x/pull/12"}) {
		t.Errorf("narrow link = %v", f.links[0])
	}

	// 7 rows: b's meta line is the bottom row, c's is cut off
	m.snap.height = 7
	if got := m.render().links; !slices.Equal(got, []link{{4, 4, 10, "https://x/pull/12"}}) {
		t.Errorf("short pane: links %v, want a's only", got)
	}
}

func TestViewCost(t *testing.T) {
	session := Session{Name: "app", Group: "app", Agent: "Claude", State: "idle", Title: "Fix login", Cost: "$1.24"}
	line := func(width int, s Session) string {
		m := Model{snap: snapshot{width: width, height: 8, current: "x", sessions: []Session{s}}}
		return ansi.Strip(m.render().lines[4])
	}
	if got, want := line(40, session), "    idle · $1.24 · Fix login"; got != want {
		t.Errorf("cost line = %q, want %q", got, want)
	}
	if got, want := line(31, session), "    idle · $1.24 · Fix login"; got != want {
		t.Errorf("title cut after the cost = %q, want %q", got, want)
	}
	if got, want := line(30, session), "    idle · $1.24 · Fix login"; got != want {
		t.Errorf("no room for the title = %q, want %q", got, want)
	}
	// A cost that doesn't fit whole is left out, not cut
	if got, want := line(24, session), "    idle · $1.24 · Fix…"; got != want {
		t.Errorf("no room for the cost = %q, want %q", got, want)
	}
	if got := line(40, Session{Name: "app", Group: "app", Agent: "Claude", State: "idle"}); strings.Contains(got, "$") {
		t.Errorf("no cost: %q", got)
	}
}

// The selected row's title that doesn't fit whole wraps onto lines of its own; other rows keep the cut.
func TestViewSelectedTitleWraps(t *testing.T) {
	long := "v0.35.0 dev app startup and sidebar layout review"
	m := Model{selected: "a", snap: snapshot{width: 40, height: 14, focused: true, current: "x", sessions: []Session{
		{Name: "a", Group: "g", Agent: "Claude", State: "idle", Title: long, Cost: "$18.42"},
		{Name: "b", Group: "g", Agent: "Claude", State: "waiting", Title: long, Cost: "$0.85", Depth: 1,
			PR: PR{Number: 7, State: "open", URL: "u7"}},
		{Name: "c", Group: "g", Agent: "Claude", State: "idle", Title: "Fix login"},
	}}}
	strip := func(f frame, from, to int) []string {
		var got []string
		for _, l := range f.lines[from:to] {
			got = append(got, strings.TrimRight(ansi.Strip(l), " "))
		}
		return got
	}

	f := m.render()
	want := []string{
		"  ● a                               ×",
		"    idle · $18.42",
		"    v0.35.0 dev app startup and sidebar",
		"    layout review",
		"  └ ● b                             ×",
		"      #7 open · waiting · $0.85 · v0.3…",
		"  ● c                               ×",
		"    idle · Fix login",
	}
	if got := strip(f, 3, 11); !slices.Equal(got, want) {
		t.Errorf("a selected:\n got %q\nwant %q", got, want)
	}
	for i := 3; i <= 6; i++ {
		if !strings.HasPrefix(f.lines[i], selectedBG) || ansi.StringWidth(f.lines[i]) != 40 {
			t.Errorf("line %d not a full-width selected line: %q", i, f.lines[i])
		}
	}
	// Continuation lines keep the meta colour and select their session when clicked
	if !strings.Contains(f.lines[5], muted+"v0.35.0") {
		t.Errorf("wrapped line colour: %q", f.lines[5])
	}
	if s, exact, _ := rowAt(f.rows, 6); s != "a" || exact {
		t.Errorf("click on wrapped line = %q exact %v, want a, not exact", s, exact)
	}

	// A worker's wrap starts past its indent; the PR tag stays a link on the meta line
	m.selected = "b"
	f = m.render()
	want = []string{
		"  ● a                               ×",
		"    idle · $18.42 · v0.35.0 dev app st…",
		"  └ ● b                             ×",
		"      #7 open · waiting · $0.85",
		"      v0.35.0 dev app startup and",
		"      sidebar layout review",
		"  ● c                               ×",
	}
	if got := strip(f, 3, 10); !slices.Equal(got, want) {
		t.Errorf("b selected:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(f.lines[7], red+"v0.35.0") {
		t.Errorf("waiting row's wrapped line colour: %q", f.lines[7])
	}
	if !slices.Equal(f.links, []link{{6, 6, 13, "u7"}}) {
		t.Errorf("links = %v", f.links)
	}

	// A title that fits stays on the meta line
	m.selected = "c"
	if got := strip(m.render(), 7, 9); !slices.Equal(got, []string{"  ● c                               ×", "    idle · Fix login"}) {
		t.Errorf("c selected: %q", got)
	}

	// Wrapped lines that would push a session under the bottom row (no scrolling): the title stays cut
	m.selected, m.snap.height = "a", 11
	if got := strip(m.render(), 4, 11); !slices.Equal(got, []string{"    idle · $18.42 · v0.35.0 dev app st…",
		"  └ ● b                             ×", "      #7 open · waiting · $0.85 · v0.3…",
		"  ● c                               ×", "    idle · Fix login", "", ansi.Strip(hint)}) {
		t.Errorf("one line short: %q", got)
	}
	m.snap.height = 12
	if got := strip(m.render(), 4, 12); !slices.Equal(got, []string{"    idle · $18.42",
		"    v0.35.0 dev app startup and sidebar", "    layout review",
		"  └ ● b                             ×", "      #7 open · waiting · $0.85 · v0.3…",
		"  ● c                               ×", "    idle · Fix login", ansi.Strip(hint)}) {
		t.Errorf("tall enough: %q", got)
	}
}

// The wrap decision counts only what the meta line shows: an exact fit and a title beside a dropped cost stay.
func TestViewSelectedTitleFits(t *testing.T) {
	line := func(width int, s Session) []string {
		m := Model{selected: s.Name, snap: snapshot{width: width, height: 10, current: "x", sessions: []Session{s}}}
		f := m.render()
		return []string{strings.TrimRight(ansi.Strip(f.lines[4]), " "), strings.TrimRight(ansi.Strip(f.lines[5]), " ")}
	}
	s := Session{Name: "a", Group: "g", Agent: "Claude", State: "idle", Cost: "$18.42", Title: "0123456789"}
	if got, want := line(31, s), []string{"    idle · $18.42 · 0123456789", ""}; !slices.Equal(got, want) {
		t.Errorf("exact fit = %q, want %q", got, want)
	}
	s.Title += "x"
	if got, want := line(31, s), []string{"    idle · $18.42", "    0123456789x"}; !slices.Equal(got, want) {
		t.Errorf("one column over = %q, want %q", got, want)
	}
	s.Cost, s.Title = "$1234.56", "abc"
	if got, want := line(17, s), []string{"    idle · abc", ""}; !slices.Equal(got, want) {
		t.Errorf("cost dropped = %q, want %q", got, want)
	}
}

func TestFormatCost(t *testing.T) {
	for usd, want := range map[float64]string{0: "$0.00", 0.004: "$0.00", 1.236: "$1.24", 189.21: "$189.21"} {
		if got := FormatCost(usd); got != want {
			t.Errorf("FormatCost(%v) = %q, want %q", usd, got, want)
		}
	}
}

func TestViewIndentsWorkers(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 12, current: "x", sessions: []Session{
		{Name: "herd-app", Group: "app", Agent: "Claude", State: "idle"},
		{Name: "app-fix", Group: "app", Agent: "Claude", State: "working", Cost: "$0.42", Depth: 1, PR: PR{Number: 7, State: "open", URL: "u"}},
		{Name: "app-fix-sub", Group: "app", Depth: 2},
	}}}
	f := m.render()
	var got []string
	for _, l := range f.lines[3:9] {
		got = append(got, strings.TrimRight(ansi.Strip(l), " "))
	}
	want := []string{
		"  ● herd-app                        ×",
		"    idle",
		"  └ ● app-fix                       ×",
		"      #7 open · working · $0.42", // the cost doesn't fit whole past the indent
		"    └ ○ app-fix-sub                 ×",
		"        no agent",
	}
	if !slices.Equal(got, want) {
		t.Errorf("indented rows =\n%q\nwant\n%q", got, want)
	}
	// The PR tag's click zone moves with the indent
	if len(f.links) != 1 || f.links[0].from != 6 {
		t.Errorf("links = %+v, want the tag at column 6", f.links)
	}
}

func TestViewIndentStaysLeftOfButtons(t *testing.T) {
	for _, w := range []int{16, 20, 30} { // below 16 there are no buttons
		m := Model{snap: snapshot{width: w, height: 8, current: "x", sessions: []Session{{Name: "deep-worker", Group: "g", Depth: 6}}}}
		row := ansi.Strip(m.render().lines[3])
		x := strings.Index(row, "×")
		if x < 0 {
			t.Errorf("width %d: no × in %q", w, row)
			continue
		}
		col := ansi.StringWidth(row[:x])
		name := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(row[:x]), "└ ○"))
		if col != w-4 || name == "" {
			t.Errorf("width %d: %q puts × at %d (want %d) with name %q", w, row, col, w-4, name)
		}
	}
}

func TestViewHidesNamedBranch(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 20, current: "x", sessions: []Session{
		{Name: "dev", Group: "dev", Branch: "main", Agent: "Claude"},
		{Name: "dev-demo-ask", Group: "dev", Branch: "demo-ask", Named: true, Agent: "Claude", Depth: 1},
		{Name: "dev-old", Group: "dev", Branch: "new", Agent: "Claude"},
	}}}
	f := m.render()
	var got []string
	for _, r := range f.rows {
		got = append(got, strings.TrimRight(ansi.Strip(f.lines[r.line]), " "))
	}
	want := []string{
		"  ● dev                       main  ×",
		"  └ ● dev-demo-ask                  ×",
		"  ● dev-old                    new  ×",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows =\n%q\nwant\n%q", got, want)
	}
}

func TestListLines(t *testing.T) {
	sessions := []Session{{Name: "a", Group: "api"}, {Name: "b", Group: "web"}, {Name: "c", Group: "web"}}
	m := Model{snap: snapshot{width: 40, height: 20, current: "x", sessions: sessions}}
	lines := m.render().lines
	drawn := len(lines)
	for drawn > 0 && lines[drawn-1] == "" {
		drawn--
	}
	if got := listLines(sessions); got != drawn || got != 11 {
		t.Errorf("listLines = %d, render drew %d, want 11", got, drawn)
	}
}

func TestViewAgentName(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 14, current: "x", sessions: []Session{
		{Name: "a", Group: "g", Agent: "Claude", State: "idle", Title: "t"},
		{Name: "b", Group: "g", Agent: "Codex", State: "idle", Title: "t"},
		{Name: "c", Group: "g", Agent: "Grok", State: "working"},
		{Name: "d", Group: "g", Agent: "Claude"},
	}}}
	f := m.render()
	for i, want := range map[int]string{4: "    idle · t", 6: "    Codex · idle · t", 8: "    Grok · working", 10: "    Claude"} {
		if got := strings.TrimRight(ansi.Strip(f.lines[i]), " "); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
}

func rollupSessions() []Session {
	return []Session{
		{Name: "boss", Group: "g", Boss: true, ID: "$1", Agent: "Claude", State: "idle", Title: "t", Cost: "$1.00"},
		{Name: "w1", Group: "g", ID: "$2", StartedBy: "boss", Depth: 1, Agent: "Claude", State: "waiting", Deciding: true, Cost: "$2.50"},
		{Name: "w2", Group: "g", ID: "$3", StartedBy: "boss", Depth: 1, Agent: "Claude", State: "working"},
		{Name: "w3", Group: "g", ID: "$4", StartedBy: "$3", Depth: 2, Agent: "Claude", State: "working", Cost: "$0.50"},
		{Name: "solo", Group: "g", Boss: true, ID: "$5", Agent: "Claude", State: "idle"},
	}
}

func TestViewBossRollup(t *testing.T) {
	m := Model{snap: snapshot{width: 40, height: 20, current: "x", sessions: rollupSessions()}}
	f := m.render()
	var got []string
	for _, l := range f.lines[3:14] {
		got = append(got, strings.TrimRight(ansi.Strip(l), " "))
	}
	want := []string{
		"  ● boss                            ×",
		"    idle · $1.00 · t",
		"    1 need you · 2 working · $4.00",
		"  └ ● w1                            ×",
		"      waiting · $2.50",
		"  └ ● w2                            ×",
		"      working",
		"    └ ● w3                          ×",
		"        working · $0.50",
		"  ● solo                            ×",
		"    idle",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(f.lines[5], red+"1 need you") {
		t.Errorf("need-you colour: %q", f.lines[5])
	}
	wantRows := []rowStart{{3, "boss"}, {6, "w1"}, {8, "w2"}, {10, "w3"}, {12, "solo"}}
	if !slices.Equal(f.rows, wantRows) {
		t.Errorf("rows = %v, want %v", f.rows, wantRows)
	}
	// A click on the roll-up line selects its boss, not exact so × doesn't close it
	if s, exact, _ := rowAt(f.rows, 5); s != "boss" || exact {
		t.Errorf("click on roll-up = %q exact %v", s, exact)
	}
}

func TestViewBossRollupVariants(t *testing.T) {
	line := func(width int, ss []Session) string {
		m := Model{snap: snapshot{width: width, height: 20, current: "x", sessions: ss}}
		return strings.TrimRight(ansi.Strip(m.render().lines[5]), " ")
	}
	idle := []Session{
		{Name: "boss", Group: "g", Boss: true, ID: "$1", Agent: "Claude", State: "idle"},
		{Name: "w", Group: "g", ID: "$2", StartedBy: "boss", Depth: 1, Agent: "Claude", State: "idle"},
	}
	idle = append(idle, Session{Name: "r", Group: "g", ID: "$3", StartedBy: "boss", Depth: 1, Agent: "Claude", State: "waiting"})
	idle[1].State = "idle"
	if got, want := line(40, idle), "    2 idle"; got != want { // an idle reminder (waiting, not deciding) is idle
		t.Errorf("all idle = %q, want %q", got, want)
	}
	if !strings.Contains(newRollupFrame(idle).lines[5], muted+"2 idle") {
		t.Error("idle count not muted")
	}
	if got, want := line(27, rollupSessions()), "    1 need you · 2 working"; got != want {
		t.Errorf("cost cut before counts = %q, want %q", got, want)
	}
	if got, want := line(18, rollupSessions()), "    1 need you ·…"; got != want {
		t.Errorf("counts cut = %q, want %q", got, want)
	}
	// Selected: full-width selection background on the roll-up line
	m := Model{selected: "boss", snap: snapshot{width: 40, height: 20, current: "x", sessions: rollupSessions()}}
	if l := m.render().lines[5]; !strings.HasPrefix(l, selectedBG) || ansi.StringWidth(l) != 40 {
		t.Errorf("selected roll-up line: %q", l)
	}
}

func newRollupFrame(ss []Session) frame {
	return Model{snap: snapshot{width: 40, height: 20, current: "x", sessions: ss}}.render()
}

func TestViewBossRollupListLines(t *testing.T) {
	ss := rollupSessions()
	m := Model{snap: snapshot{width: 40, height: 40, current: "x", sessions: ss}}
	rows := m.render().rows
	if got, drawn := listLines(ss), rows[len(rows)-1].line+2; got != drawn || got != 14 {
		t.Errorf("listLines = %d, render drew %d, want 14", got, drawn)
	}
}
