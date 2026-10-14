package resurrect

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// fakeServer answers tmux commands from replies, keyed by the space-joined argv without a Query's
// trailing -F format; an error value fails the command. Warnings always succeed; anything else
// unknown fails, so a missed step shows up in the call log.
type fakeServer struct {
	replies map[string]any // string output or error
	calls   []string
}

func (f *fakeServer) client() *tmux.Client {
	return tmux.NewFake(func(args []string) (string, error) {
		if n := len(args); n >= 2 && args[n-2] == "-F" && strings.Contains(args[n-1], tmux.Sep) {
			args = args[:n-2]
		}
		key := strings.Join(args, " ")
		f.calls = append(f.calls, key)
		switch reply := f.replies[key].(type) {
		case string:
			return reply, nil
		case error:
			return "", reply
		}
		if args[0] == "display-message" && len(args) == 2 || args[0] == "set-option" && args[1] == "-ga" {
			return "", nil
		}
		return "", errors.New("unexpected command")
	})
}

// warned is the call log of warn(msg).
func warned(msg string) []string {
	return []string{"display-message ⚠ dev resurrect: " + msg, "set-option -ga @dev_resurrect_warnings " + msg + "; "}
}

func stubHelper(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	helper = fn
	t.Cleanup(func() { helper = runHelper })
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSave(t *testing.T) {
	sessions := tmux.FormatRow("devt", "devt", "%1") + tmux.FormatRow("plain", "", "") +
		tmux.FormatRow("hidden", "proj", "") + tmux.FormatRow("stale", "p", "%99")
	panes := tmux.FormatRow("%1", "1", "1") + tmux.FormatRow("%2", "1", "2")
	tests := []struct {
		name      string
		replies   map[string]any
		blockTemp bool // a directory where the temp file goes, so writing fails
		want      string
		wantWarn  string
	}{
		{
			name:    "one line per session with a project",
			replies: map[string]any{"list-sessions": sessions, "list-panes -a": panes},
			want:    "devt\tdevt\t1.1\nhidden\tproj\t\nstale\tp\t\n",
		},
		{
			name:     "list-sessions failure keeps the old file",
			replies:  map[string]any{"list-sessions": errors.New("boom")},
			want:     "old\n",
			wantWarn: "list-sessions failed, dev state not saved",
		},
		{
			name:     "list-panes failure keeps the old file",
			replies:  map[string]any{"list-sessions": sessions, "list-panes -a": errors.New("boom")},
			want:     "old\n",
			wantWarn: "list-panes failed, dev state not saved",
		},
		{
			name:      "write failure keeps the old file",
			replies:   map[string]any{"list-sessions": sessions, "list-panes -a": panes},
			blockTemp: true,
			want:      "old\n",
			wantWarn:  "writing <snap>.dev failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := filepath.Join(t.TempDir(), "snap.txt")
			writeFile(t, snap, "pane\n")
			writeFile(t, snap+".dev", "old\n")
			if tt.blockTemp {
				writeFile(t, snap+".dev.tmp/x", "")
			}
			f := &fakeServer{replies: tt.replies}
			err := Save(f.client(), snap)
			if (err != nil) != (tt.wantWarn != "") {
				t.Fatalf("Save error = %v", err)
			}
			if got := readFile(t, snap+".dev"); got != tt.want {
				t.Errorf("state = %q, want %q", got, tt.want)
			}
			var gotWarn []string
			for _, call := range f.calls {
				if strings.HasPrefix(call, "display-message") || strings.HasPrefix(call, "set-option") {
					gotWarn = append(gotWarn, call)
				}
			}
			var wantWarn []string
			if tt.wantWarn != "" {
				wantWarn = warned(strings.ReplaceAll(tt.wantWarn, "<snap>", snap))
			}
			if !slices.Equal(gotWarn, wantWarn) {
				t.Errorf("warnings = %q, want %q", gotWarn, wantWarn)
			}
		})
	}
}

func TestSaveMissingSnapshot(t *testing.T) {
	snap := filepath.Join(t.TempDir(), "gone.txt")
	f := &fakeServer{}
	if err := Save(f.client(), snap); err == nil {
		t.Fatal("Save succeeded without a snapshot")
	}
	want := []string{
		"display-message ⚠ dev resurrect: snapshot '" + snap + "' missing, dev state not saved",
		"set-option -ga @dev_resurrect_warnings snapshot '" + snap + "' missing, dev state not saved; ",
	}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	if _, err := os.Stat(snap + ".dev"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s.dev written: %v", snap, err)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"kept.txt", "kept.txt.dev", "orphan.txt.dev", "other.txt"} {
		writeFile(t, filepath.Join(dir, name), "")
	}
	stubHelper(t, func(fn string) (string, error) {
		if fn != "resurrect_dir" {
			t.Fatalf("helper(%q)", fn)
		}
		return dir, nil
	})
	f := &fakeServer{}
	if err := Prune(f.client()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"kept.txt", "kept.txt.dev", "other.txt"}; !slices.Equal(names, want) {
		t.Errorf("files = %q, want %q", names, want)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %q, want none", f.calls)
	}
}

func TestPruneHelperFailure(t *testing.T) {
	stubHelper(t, func(string) (string, error) { return "", errors.New("no plugin") })
	f := &fakeServer{}
	if err := Prune(f.client()); err == nil {
		t.Fatal("Prune succeeded")
	}
	if want := warned("prune: no plugin"); !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestPreRestore(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	live := filepath.Join(state, "dev/live-before-restore")

	f := &fakeServer{replies: map[string]any{"list-sessions -F #{session_name}": "boot\nplain\n"}}
	if err := PreRestore(f.client()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, live); got != "boot\nplain\n" {
		t.Errorf("live list = %q", got)
	}

	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	f = &fakeServer{replies: map[string]any{"list-sessions -F #{session_name}": errors.New("boom")}}
	if err := PreRestore(f.client()); err == nil {
		t.Fatal("PreRestore succeeded")
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("live list written after a failed query: %v", err)
	}
	if want := append([]string{"list-sessions -F #{session_name}"}, warned("list-sessions failed, sidebars will not be rebuilt")...); !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

// restoreFixture writes a snapshot with .dev content state behind a relative `last` link and a live
// list holding session "live", stubs the helper, and returns the live list's path.
func restoreFixture(t *testing.T, state string) string {
	t.Helper()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "resurrect")
	writeFile(t, filepath.Join(dir, "snap.txt"), "")
	if state != "" {
		writeFile(t, filepath.Join(dir, "snap.txt.dev"), state)
	}
	if err := os.Symlink("snap.txt", filepath.Join(dir, "last")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	live := filepath.Join(tmp, "state/dev/live-before-restore")
	writeFile(t, live, "live\n")
	stubHelper(t, func(fn string) (string, error) {
		if fn != "last_resurrect_file" {
			t.Fatalf("helper(%q)", fn)
		}
		return filepath.Join(dir, "last"), nil
	})
	return live
}

// stubEnsure records ensure calls in f's log and resolves every origin to boot's pane %0.
func stubEnsure(t *testing.T, f *fakeServer, ensureErr error) {
	t.Helper()
	ensureSidebar = func(_ *tmux.Client, target string, from sidebar.Origin) error {
		f.calls = append(f.calls, "ensure "+target+" from "+from.Session+" "+from.Pane)
		return ensureErr
	}
	resolveOrigin = func(*tmux.Client, string) (sidebar.Origin, error) {
		return sidebar.Origin{Session: "boot", Pane: "%0"}, nil
	}
	t.Cleanup(func() { ensureSidebar, resolveOrigin = sidebar.Ensure, sidebar.ResolveOrigin })
}

func TestRestore(t *testing.T) {
	slotQuery := "display-message -t =devt:1.1 -p #{pane_current_command}" + tmux.Sep + "#{pane_id}"
	// devt restored with its sidebar slot at 1.1, window 2 zoomed and window 2 active, pane %3 focused in 1
	devt := map[string]any{
		"has-session -t =devt":                         "",
		"set-option -t =devt: @project p":              "",
		"list-windows -t =devt:":                       tmux.FormatRow("1", "0") + tmux.FormatRow("2", "1"),
		"resize-pane -Z -t =devt:2":                    "",
		slotQuery:                                      "zsh" + tmux.Sep + "%1\n",
		"display-message -t =devt: -p #{window_index}": "2\n",
		"display-message -t =devt:1 -p #{pane_id}":     "%3\n",
		"kill-pane -t =devt:1.1":                       "",
		"select-window -t =devt:1":                     "",
		"select-pane -t %3":                            "",
		"select-window -t =devt:2":                     "",
	}
	with := func(overrides map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range devt {
			m[k] = v
		}
		for k, v := range overrides {
			m[k] = v
		}
		return m
	}
	setup := []string{"has-session -t =devt", "set-option -t =devt: @project p", "list-windows -t =devt:", "resize-pane -Z -t =devt:2"}
	rebuild := []string{slotQuery, "display-message -t =devt: -p #{window_index}", "display-message -t =devt:1 -p #{pane_id}",
		"kill-pane -t =devt:1.1", "select-window -t =devt:1", "ensure devt from boot %0"}
	focus := []string{"select-pane -t %3", "select-window -t =devt:2"}
	concat := func(parts ...[]string) []string { return slices.Concat(parts...) }

	tests := []struct {
		name      string
		state     string
		replies   map[string]any
		ensureErr error
		want      []string // after the default-shell query
		wantErr   bool
	}{
		{
			name:    "rebuilds the sidebar, keeping active window and focus",
			state:   "devt\tp\t1.1\n",
			replies: devt,
			want:    concat(setup, rebuild, focus),
		},
		{
			name:  "skips sessions live before the restore",
			state: "live\tp\t1.1\n",
			want:  nil,
		},
		{
			name:    "skips sessions resurrect didn't create",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{"has-session -t =devt": errors.New("no session")}),
			want:    []string{"has-session -t =devt"},
		},
		{
			name:    "warns when @project can't be set",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{"set-option -t =devt: @project p": errors.New("boom")}),
			want:    concat(setup[:2], warned("devt: @project not set")),
		},
		{
			name:    "hidden sidebar: zooms undone only",
			state:   "devt\tp\t\n",
			replies: devt,
			want:    setup,
		},
		{
			name:    "slot not running the default shell",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{slotQuery: "nvim" + tmux.Sep + "%1\n"}),
			want:    concat(setup, []string{slotQuery}, warned("devt:1.1 is not the old sidebar's bare shell, sidebar not rebuilt")),
		},
		{
			name:    "slot query failure",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{slotQuery: errors.New("can't find pane")}),
			want:    concat(setup, []string{slotQuery}, warned("devt:1.1 is not the old sidebar's bare shell, sidebar not rebuilt")),
		},
		{
			name:      "ensure failure still restores focus",
			state:     "devt\tp\t1.1\n",
			replies:   devt,
			ensureErr: errors.New("boom"),
			want:      concat(setup, rebuild, warned("devt: sidebar not created"), focus),
		},
		{
			name:    "kill-pane failure is warned with tmux's stderr",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{"kill-pane -t =devt:1.1": &tmux.Error{Stderr: "can't find pane: =devt:1.1", Err: errors.New("exit status 1")}}),
			want:    concat(setup, rebuild[:4], warned("devt: kill-pane =devt:1.1 failed: can't find pane: =devt:1.1")),
			wantErr: true,
		},
		{
			name:    "a failure without stderr gives the exit status",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{"select-window -t =devt:2": &tmux.Error{Err: errors.New("exit status 1")}}),
			want:    concat(setup, rebuild, focus, warned("devt: select-window =devt:2 failed: exit status 1")),
			wantErr: true,
		},
		{
			name:    "focus on the slot isn't restored",
			state:   "devt\tp\t1.1\n",
			replies: with(map[string]any{"display-message -t =devt:1 -p #{pane_id}": "%1\n"}),
			want:    concat(setup, rebuild, focus[1:]),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := restoreFixture(t, tt.state)
			replies := map[string]any{"show-option -gv default-shell": "/bin/zsh\n"}
			for k, v := range tt.replies {
				replies[k] = v
			}
			f := &fakeServer{replies: replies}
			stubEnsure(t, f, tt.ensureErr)
			if err := Restore(f.client()); (err != nil) != tt.wantErr {
				t.Fatalf("Restore error = %v", err)
			}
			want := append([]string{"show-option -gv default-shell"}, tt.want...)
			if !slices.Equal(f.calls, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
			}
			if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("live list not removed: %v", err)
			}
		})
	}
}

// A tmux failure bash didn't check is warned and returned, and the next session is still restored.
func TestRestoreUncheckedFailure(t *testing.T) {
	live := restoreFixture(t, "devt\tp\t\nother\tq\t\n")
	f := &fakeServer{replies: map[string]any{
		"show-option -gv default-shell":    "/bin/zsh\n",
		"has-session -t =devt":             "",
		"set-option -t =devt: @project p":  "",
		"list-windows -t =devt:":           &tmux.Error{Stderr: "can't find session: devt", Err: errors.New("exit status 1")},
		"has-session -t =other":            "",
		"set-option -t =other: @project q": "",
		"list-windows -t =other:":          tmux.FormatRow("1", "0"),
	}}
	stubEnsure(t, f, nil)
	if err := Restore(f.client()); err == nil {
		t.Fatal("Restore succeeded")
	}
	// tmux's stderr, not the argv with Query's \x1f-separated format
	if want := warned("devt: list-windows failed: can't find session: devt"); !slices.Contains(f.calls, want[0]) ||
		!slices.Contains(f.calls, want[1]) {
		t.Errorf("no warning %q for the failed list-windows: %q", want, f.calls)
	}
	if !slices.Contains(f.calls, "list-windows -t =other:") {
		t.Errorf("other session not restored: %q", f.calls)
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("live list not removed: %v", err)
	}
}

func TestRestoreMissingLiveList(t *testing.T) {
	live := restoreFixture(t, "devt\tp\t1.1\n")
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{}
	if err := Restore(f.client()); err == nil {
		t.Fatal("Restore succeeded")
	}
	if want := warned("pre-restore hook did not run, sidebars not rebuilt"); !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestRestoreWithoutDevState(t *testing.T) {
	t.Run("no .dev file", func(t *testing.T) {
		live := restoreFixture(t, "")
		f := &fakeServer{}
		if err := Restore(f.client()); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 {
			t.Errorf("calls = %q, want none", f.calls)
		}
		if _, err := os.Stat(live); err != nil {
			t.Errorf("live list touched: %v", err)
		}
	})
	t.Run("no last link", func(t *testing.T) {
		stubHelper(t, func(string) (string, error) { return filepath.Join(t.TempDir(), "last"), nil })
		f := &fakeServer{}
		if err := Restore(f.client()); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 {
			t.Errorf("calls = %q, want none", f.calls)
		}
	})
}

func TestParseState(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []entry
	}{
		{"empty", "", nil},
		{"lines", "devt\tdevt\t1.1\nhidden\tproj\t\n", []entry{{"devt", "devt", "1.1"}, {"hidden", "proj", ""}}},
		{"blank lines skipped", "\ndevt\tp\t2.1\n\n", []entry{{"devt", "p", "2.1"}}},
		{"no trailing newline", "devt\tp\t1.1", []entry{{"devt", "p", "1.1"}}},
		{"missing fields", "devt\n", []entry{{"devt", "", ""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseState(tt.data); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseState(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestLivePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, err := livePath(); err != nil || got != "/state/dev/live-before-restore" {
		t.Errorf("livePath() = %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, err := livePath(); err != nil || got != "/home/u/.local/state/dev/live-before-restore" {
		t.Errorf("livePath() = %q, %v", got, err)
	}
}

func TestRunHelperRefusedUnderTest(t *testing.T) {
	if _, err := runHelper("resurrect_dir"); err == nil {
		t.Fatal("runHelper ran bash under test")
	}
}
