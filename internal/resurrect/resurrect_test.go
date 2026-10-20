package resurrect

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/ptywrap"
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
	// boss-p's workers: w-name names it, w-old holds its id as older versions wrote; w-gone's starter is gone
	sessions := tmux.FormatRow("devt", "devt", "%1", "$1", "", "") + tmux.FormatRow("plain", "", "", "$2", "", "") +
		tmux.FormatRow("hidden", "proj", "", "$3", "", "") + tmux.FormatRow("stale", "p", "%99", "$4", "", "") +
		tmux.FormatRow("boss-p", "", "", "$9", "", "1") + tmux.FormatRow("w-name", "p", "", "$10", "boss-p", "") +
		tmux.FormatRow("w-old", "", "", "$11", "$9", "") + tmux.FormatRow("w-gone", "", "", "$12", "$50", "") +
		tmux.FormatRow("boss-old", "", "", "$13", "", "1")
	// devt's window 1 is also linked into linked as window 5, listed after devt. %2 runs Claude with a
	// recorded session, %3 holds a crashed one's leftover, %4 a Claude without a session yet. boss-p's
	// panes carry their roles, plain's previews-marked pane doesn't make it a boss, and boss-old (made
	// before the roles) has none.
	pane := func(session, id, window, index, command, agentCmd, agentID, role string) string {
		return tmux.FormatRow(session, id, window, index, command, "/repo", agentCmd, agentID, "/t/"+agentID+".jsonl", role)
	}
	panes := pane("devt", "%1", "1", "1", "dev", "", "", "") + pane("devt", "%2", "1", "2", "2.1.3", "2.1.3", "s1", "") +
		pane("devt", "%3", "1", "3", "zsh", "2.1.3", "s0", "") + pane("plain", "%4", "1", "1", "2.1.3", "2.1.3", "", "previews") +
		pane("linked", "%1", "5", "1", "dev", "", "", "") + pane("linked", "%2", "5", "2", "2.1.3", "2.1.3", "s1", "") +
		pane("boss-p", "%7", "1", "2", "2.1.3", "2.1.3", "b1", "coordinator") + pane("boss-p", "%8", "1", "3", "dev", "", "", "previews") +
		pane("boss-old", "%9", "1", "1", "2.1.3", "2.1.3", "", "")
	tests := []struct {
		name      string
		replies   map[string]any
		blockTemp bool // a directory where the temp file goes, so writing fails
		want      string
		wantWarn  string
	}{
		{
			name:    "one line per session with a project or a boss",
			replies: map[string]any{"list-sessions": sessions, "list-panes -a": panes},
			want: "devt\tdevt\t1.1\nhidden\tproj\t\nstale\tp\t\nboss-p\t\t\nw-name\tp\t\nboss-old\t\t\n" +
				":started_by\tw-name\tboss-p\n:started_by\tw-old\tboss-p\n:boss\tboss-p\t1.2\t1.3\n" +
				":agent\tdevt\t1.2\tclaude\ts1\t/t/s1.jsonl\t/repo\n:agent\tboss-p\t1.2\tclaude\tb1\t/t/b1.jsonl\t/repo\n",
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

// save.sh deletes a new snapshot identical to the last one, so its state replaces the last one's.
func TestSaveIdenticalSnapshot(t *testing.T) {
	replies := map[string]any{"list-sessions": tmux.FormatRow("devt", "devt", "", "$1", "", ""), "list-panes -a": ""}
	tests := []struct {
		name, last, link string
		wantDev          string // the file that gets the state
	}{
		{"identical to last", "pane\n", "old.txt", "old.txt.dev"},
		{"identical to last, absolute link", "pane\n", "<dir>/old.txt", "old.txt.dev"},
		{"differs from last", "other\n", "old.txt", "new.txt.dev"},
		{"last is the snapshot", "", "new.txt", "new.txt.dev"},
		{"last link dangling", "", "gone.txt", "new.txt.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			snap := filepath.Join(dir, "new.txt")
			writeFile(t, snap, "pane\n")
			if tt.last != "" {
				writeFile(t, filepath.Join(dir, "old.txt"), tt.last)
			}
			if err := os.Symlink(strings.ReplaceAll(tt.link, "<dir>", dir), filepath.Join(dir, "last")); err != nil {
				t.Fatal(err)
			}
			f := &fakeServer{replies: replies}
			if err := Save(f.client(), snap); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"old.txt.dev", "new.txt.dev"} {
				_, err := os.Stat(filepath.Join(dir, name))
				if got := err == nil; got != (name == tt.wantDev) {
					t.Errorf("%s written = %v", name, got)
				}
			}
			if got := readFile(t, filepath.Join(dir, tt.wantDev)); got != "devt\tdevt\t\n" {
				t.Errorf("state = %q", got)
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
			name:  "sets the boss that started a restored worker",
			state: "devt\tp\t\n:started_by\tdevt\tboss-p\n:started_by\tlive\tboss-p\n:started_by\tgone\tboss-p\n",
			replies: with(map[string]any{"set-option -t =devt: @dev_started_by boss-p": "",
				"has-session -t =gone": errors.New("no session")}),
			want: concat(setup, []string{"has-session -t =devt", "set-option -t =devt: @dev_started_by boss-p", "has-session -t =gone"}),
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

func TestRestoreAgents(t *testing.T) {
	stagger = 0
	t.Cleanup(func() { stagger = 100 * time.Millisecond })
	transcript := filepath.Join(t.TempDir(), "s1.jsonl")
	writeFile(t, transcript, "{}\n")
	line := func(session, kind, id, transcript, path string) string {
		return strings.Join([]string{agentPrefix, session, "1.2", kind, id, transcript, path}, "\t") + "\n"
	}
	slotQuery := func(session string) string {
		return "display-message -t =" + session + ":1.2 -p #{pane_current_command}" + tmux.Sep + "#{pane_id}" + tmux.Sep + "#{pane_current_path}"
	}
	procs := "show-option -gqv @resurrect-default-processes ; show-option -gqv @resurrect-processes"
	fresh := func(session, id string) []string {
		return warned(session + ":1.2: conversation " + id + " not resumed (directory changed or transcript gone), fresh agent")
	}
	argv, err := ptywrap.Argv()
	if err != nil {
		t.Fatal(err)
	}
	wrap := tmux.ShellJoin(argv...)
	send := func(pane, cmd string) string {
		return "send-keys -t " + pane + " -l " + wrap + " " + cmd + " ; send-keys -t " + pane + " Enter"
	}
	resumeT, resumeU := `'claude' '--resume' 's1' '--remote-control=devt'`, `'claude' '--resume' 's1' '--remote-control=devu'`
	freshT, freshU := `'claude' '--remote-control=devt'`, `'claude' '--remote-control=devu'`
	slots := map[string]any{
		procs:               "\"~^([^ ]*/)?lazygit( |$)\"\n",
		slotQuery("devt"):   "zsh" + tmux.Sep + "%2" + tmux.Sep + "/repo\n",
		slotQuery("devu"):   "zsh" + tmux.Sep + "%5" + tmux.Sep + "/repo\n",
		send("%2", resumeT): "",
		send("%2", freshT):  "",
		send("%5", freshU):  "",
	}
	with := func(overrides map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range slots {
			m[k] = v
		}
		for k, v := range overrides {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name    string
		state   string
		replies map[string]any
		want    []string // after the default-shell query
		wantErr bool
	}{
		{
			name:    "resumes each agent's conversation, else starts a fresh one",
			state:   line("devt", "claude", "s1", transcript, "/repo") + line("devu", "claude", "s9", transcript+".gone", "/repo"),
			replies: slots,
			want: slices.Concat([]string{procs, slotQuery("devt"), send("%2", resumeT), slotQuery("devu")},
				fresh("devu", "s9"), []string{send("%5", freshU)}),
		},
		{
			name:    "a pane back in another directory gets a fresh agent",
			state:   line("devt", "claude", "s1", transcript, "/elsewhere"),
			replies: slots,
			want:    slices.Concat([]string{procs, slotQuery("devt")}, fresh("devt", "s1"), []string{send("%2", freshT)}),
		},
		{
			name:    "agents of sessions live before the restore are left alone",
			state:   line("live", "claude", "s1", transcript, "/repo"),
			replies: slots,
			want:    []string{procs},
		},
		{
			name:    "agents restart before the sidebar is rebuilt",
			state:   "devt\tp\t\n" + line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{"has-session -t =devt": "", "set-option -t =devt: @project p": "", "list-windows -t =devt:": ""}),
			want: []string{procs, slotQuery("devt"), send("%2", resumeT),
				"has-session -t =devt", "set-option -t =devt: @project p", "list-windows -t =devt:"},
		},
		{
			name:    "resurrect restarting claude itself",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: "\"~^([^ ]*/)?claude( |$)->claude --continue\"\n"}),
			want:    slices.Concat([]string{procs}, warned("@resurrect-processes restarts claude, agents not resumed: remove its claude entry")),
		},
		{
			name:    "resurrect restarting every process",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: ":all:\n"}),
			want:    slices.Concat([]string{procs}, warned("@resurrect-processes restarts claude, agents not resumed: remove its claude entry")),
		},
		{
			name:    "another claude-named process doesn't count",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: "\"~claude-monitor\"\n"}),
			want:    []string{procs, slotQuery("devt"), send("%2", resumeT)},
		},
		{
			name:    "claude in another process's restore command doesn't count",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: "\n\"~^([^ ]*/)?nvim( |$)->nvim ~/.claude/settings.json\" 'claude-monitor'\n"}),
			want:    []string{procs, slotQuery("devt"), send("%2", resumeT)},
		},
		{
			name:    "claude in the default process list",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: "vi claude\n\n"}),
			want:    slices.Concat([]string{procs}, warned("@resurrect-processes restarts claude, agents not resumed: remove its claude entry")),
		},
		{
			name:    "@resurrect-processes unknown",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{procs: errors.New("server exited")}),
			want:    slices.Concat([]string{procs}, warned("@resurrect-processes unknown, agents not restarted")),
			wantErr: true,
		},
		{
			name:    "slot not running the default shell",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{slotQuery("devt"): "2.1.3" + tmux.Sep + "%2" + tmux.Sep + "/repo\n"}),
			want:    slices.Concat([]string{procs, slotQuery("devt")}, warned("devt:1.2 is not the agent's bare shell, agent not restarted")),
		},
		{
			name:    "slot gone",
			state:   line("devt", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{slotQuery("devt"): errors.New("can't find pane")}),
			want:    slices.Concat([]string{procs, slotQuery("devt")}, warned("devt:1.2 is not the agent's bare shell, agent not restarted")),
		},
		{
			name:    "unknown agent kind",
			state:   line("devt", "codex", "s1", transcript, "/repo"),
			replies: slots,
			want:    slices.Concat([]string{procs, slotQuery("devt")}, warned("devt:1.2: unknown agent codex, not restarted")),
		},
		{
			name:    "send-keys failure is warned and returned, the next agent still starts",
			state:   line("devt", "claude", "s1", transcript, "/repo") + line("devu", "claude", "s1", transcript, "/repo"),
			replies: with(map[string]any{send("%2", resumeT): &tmux.Error{Stderr: "can't find pane: %2", Err: errors.New("exit status 1")}, send("%5", resumeU): ""}),
			want: slices.Concat([]string{procs, slotQuery("devt"), send("%2", resumeT)},
				warned("send-keys devt:1.2 failed: can't find pane: %2"), []string{slotQuery("devu"), send("%5", resumeU)}),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreFixture(t, tt.state)
			replies := map[string]any{"show-option -gv default-shell": "/bin/zsh\n"}
			for k, v := range tt.replies {
				replies[k] = v
			}
			f := &fakeServer{replies: replies}
			stubEnsure(t, f, nil)
			if err := Restore(f.client()); (err != nil) != tt.wantErr {
				t.Fatalf("Restore error = %v", err)
			}
			want := append([]string{"show-option -gv default-shell"}, tt.want...)
			if !slices.Equal(f.calls, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func TestRestoreBoss(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "b1.jsonl")
	writeFile(t, transcript, "{}\n")
	boss := ":boss\tboss-p\t1.2\t1.3\n"
	coordinator := func(transcript, path string) string {
		return strings.Join([]string{agentPrefix, "boss-p", "1.2", "claude", "b1", transcript, path}, "\t") + "\n"
	}
	slotQuery := func(pos string) string {
		return "display-message -t =boss-p:" + pos + " -p #{pane_current_command}" + tmux.Sep + "#{pane_id}" + tmux.Sep + "#{pane_current_path}"
	}
	slots := map[string]any{
		"has-session -t =boss-p": "",
		slotQuery("1.2"):         "zsh" + tmux.Sep + "%7" + tmux.Sep + "/repo\n",
		slotQuery("1.3"):         "zsh" + tmux.Sep + "%8" + tmux.Sep + "/repo\n",
	}
	with := func(overrides map[string]any) map[string]any {
		m := maps.Clone(slots)
		maps.Copy(m, overrides)
		return m
	}
	queried := []string{"has-session -t =boss-p", slotQuery("1.2"), slotQuery("1.3")}
	procs := "show-option -gqv @resurrect-default-processes ; show-option -gqv @resurrect-processes"
	devtSlot := "display-message -t =devt:1.2 -p #{pane_current_command}" + tmux.Sep + "#{pane_id}" + tmux.Sep + "#{pane_current_path}"
	argv, err := ptywrap.Argv()
	if err != nil {
		t.Fatal(err)
	}
	send := func(pane, cmd string) string {
		return "send-keys -t " + pane + " -l " + tmux.ShellJoin(argv...) + " " + cmd + " ; send-keys -t " + pane + " Enter"
	}
	devtResume := `'claude' '--resume' 'b1' '--remote-control=devt'`
	fresh := warned("boss-p:1.2: conversation b1 not resumed (directory changed or transcript gone), fresh coordinator")
	tests := []struct {
		name     string
		state    string
		replies  map[string]any
		adoptErr error
		want     []string // after the default-shell query
		wantErr  bool
	}{
		{
			name:  "the coordinator resumes its conversation with the boss, not as a plain agent; an agent at its position elsewhere does",
			state: boss + coordinator(transcript, "/repo") + strings.Replace(coordinator(transcript, "/repo"), "boss-p", "devt", 1),
			replies: with(map[string]any{
				procs:                  "\n",
				devtSlot:               "zsh" + tmux.Sep + "%2" + tmux.Sep + "/repo\n",
				send("%2", devtResume): "",
			}),
			want: append(queried, "adopt boss-p %7 %8 [--resume b1]", procs, devtSlot, send("%2", devtResume)),
		},
		{
			name:    "a coordinator without a recorded conversation starts fresh",
			state:   boss,
			replies: slots,
			want:    append(queried, "adopt boss-p %7 %8 []"),
		},
		{
			name:    "its transcript gone",
			state:   boss + coordinator(transcript+".gone", "/repo"),
			replies: slots,
			want:    slices.Concat(queried, fresh, []string{"adopt boss-p %7 %8 []"}),
		},
		{
			name:    "its pane back in another directory",
			state:   boss + coordinator(transcript, "/elsewhere"),
			replies: slots,
			want:    slices.Concat(queried, fresh, []string{"adopt boss-p %7 %8 []"}),
		},
		{
			name:  "the previews slot not a bare shell: the coordinator resumes as a plain agent",
			state: boss + coordinator(transcript, "/repo"),
			replies: with(map[string]any{slotQuery("1.3"): "dev" + tmux.Sep + "%8" + tmux.Sep + "/repo\n", procs: "\n",
				send("%7", `'claude' '--resume' 'b1' '--remote-control=boss-p'`): ""}),
			want: slices.Concat(queried, warned("boss-p:1.3 is not the boss's bare shell, boss not restored"),
				[]string{procs, slotQuery("1.2"), send("%7", `'claude' '--resume' 'b1' '--remote-control=boss-p'`)}),
		},
		{
			name:    "a boss live before the restore is left alone",
			state:   ":boss\tlive\t1.2\t1.3\n",
			replies: slots,
		},
		{
			name:    "a boss resurrect didn't bring back",
			state:   boss,
			replies: with(map[string]any{"has-session -t =boss-p": errors.New("no session")}),
			want:    []string{"has-session -t =boss-p"},
		},
		{
			name:     "a failure is warned and returned, the sessions are still restored",
			state:    "devt\tp\t\n" + boss,
			replies:  with(map[string]any{"has-session -t =devt": "", "set-option -t =devt: @project p": "", "list-windows -t =devt:": ""}),
			adoptErr: &tmux.Error{Stderr: "can't find pane: %7", Err: errors.New("exit status 1")},
			want: slices.Concat(queried, []string{"adopt boss-p %7 %8 []"}, warned("boss-p: boss failed: can't find pane: %7"),
				[]string{"has-session -t =devt", "set-option -t =devt: @project p", "list-windows -t =devt:"}),
			wantErr: true,
		},
		{
			name:    "a boss with no project keeps none",
			state:   "boss-p\t\t\n" + boss,
			replies: with(map[string]any{"list-windows -t =boss-p:": ""}),
			want:    append(queried, "adopt boss-p %7 %8 []", "has-session -t =boss-p", "list-windows -t =boss-p:"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreFixture(t, tt.state)
			replies := map[string]any{"show-option -gv default-shell": "/bin/zsh\n"}
			maps.Copy(replies, tt.replies)
			f := &fakeServer{replies: replies}
			stubEnsure(t, f, nil)
			adoptBoss = func(_ *tmux.Client, session, claude, previews string, resume []string) error {
				f.calls = append(f.calls, fmt.Sprintf("adopt %s %s %s %v", session, claude, previews, resume))
				return tt.adoptErr
			}
			t.Cleanup(func() { adoptBoss = herd.Adopt })
			if err := Restore(f.client()); (err != nil) != tt.wantErr {
				t.Fatalf("Restore error = %v", err)
			}
			want := append([]string{"show-option -gv default-shell"}, tt.want...)
			if !slices.Equal(f.calls, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
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
	devtAgent := agentEntry{Session: "devt", Pos: "1.2", Kind: "claude", ID: "s1", Transcript: "/t/s1.jsonl", Path: "/repo"}
	tests := []struct {
		name       string
		data       string
		want       []entry
		wantAgents []agentEntry
		wantBy     []startedByEntry
		wantBosses []bossEntry
	}{
		{"empty", "", nil, nil, nil, nil},
		{"lines", "devt\tdevt\t1.1\nhidden\tproj\t\n", []entry{{"devt", "devt", "1.1"}, {"hidden", "proj", ""}}, nil, nil, nil},
		{"blank lines skipped", "\ndevt\tp\t2.1\n\n", []entry{{"devt", "p", "2.1"}}, nil, nil, nil},
		{"no trailing newline", "devt\tp\t1.1", []entry{{"devt", "p", "1.1"}}, nil, nil, nil},
		{"missing fields", "devt\n", []entry{{"devt", "", ""}}, nil, nil, nil},
		{"agent lines", "devt\tp\t1.1\n:agent\tdevt\t1.2\tclaude\ts1\t/t/s1.jsonl\t/repo\n", []entry{{"devt", "p", "1.1"}},
			[]agentEntry{devtAgent}, nil, nil},
		{"agent line with missing fields skipped", ":agent\tdevt\t1.2\tclaude\n", nil, nil, nil, nil},
		{"started-by lines", ":started_by\tw\tboss-p\n:started_by\tbad\n", nil, nil, []startedByEntry{{"w", "boss-p"}}, nil},
		{"boss lines", ":boss\tboss-p\t1.2\t1.3\n:boss\tbad\t1.2\n:boss\tbad\t\t1.3\n", nil, nil, nil, []bossEntry{{"boss-p", "1.2", "1.3"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, agents, by, bosses := parseState(tt.data)
			if !reflect.DeepEqual(got, tt.want) || !reflect.DeepEqual(agents, tt.wantAgents) || !reflect.DeepEqual(by, tt.wantBy) ||
				!reflect.DeepEqual(bosses, tt.wantBosses) {
				t.Errorf("parseState(%q) = %v, %v, %v, %v; want %v, %v, %v, %v", tt.data, got, agents, by, bosses,
					tt.want, tt.wantAgents, tt.wantBy, tt.wantBosses)
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
