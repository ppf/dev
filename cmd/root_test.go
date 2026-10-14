package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/tmux"
)

const projectsTOML = `[[project]]
name = "app"
path = "~/src/app"
description = "Main app"

[[project]]
name = "notes"
path = "/tmp/notes"
`

// execute runs dev with args and stdin in a clean env and returns stdout, stderr and the error.
func execute(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("TMUX", "")
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// withConfig points XDG_CONFIG_HOME at a temp dir holding content as projects.toml and returns its path.
func withConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "dev", "projects.toml")
	if content == "" {
		return path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRootPrintsHelp(t *testing.T) {
	out, _, err := execute(t, "", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tmux workspace launcher") || strings.Contains(out, "completion") {
		t.Errorf("help output:\n%s", out)
	}
}

func TestList(t *testing.T) {
	withConfig(t, projectsTOML)
	out, _, err := execute(t, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "app\tMain app\t~/src/app\nnotes\t\t/tmp/notes\n"; out != want {
		t.Errorf("list = %q, want %q", out, want)
	}
}

func TestUnknownProject(t *testing.T) {
	path := withConfig(t, projectsTOML)
	out, errOut, err := execute(t, "", "nope")
	if !errors.Is(err, open.ErrShown) {
		t.Errorf("err = %v, want ErrShown (already printed)", err)
	}
	want := "❌ Project 'nope' not found in " + path + "\nAvailable:\n  app\n  notes\n"
	if out != "" || errOut != want {
		t.Errorf("stdout %q, stderr %q; want stderr %q", out, errOut, want)
	}
}

func TestKnownProjectOpens(t *testing.T) {
	withConfig(t, projectsTOML)
	// Reaching tmux proves the project was handed to open.Run; under test tmux.New refuses to run.
	if _, _, err := execute(t, "", "app"); !errors.Is(err, tmux.ErrNoSocket) {
		t.Errorf("err = %v, want ErrNoSocket", err)
	}
}

func TestReservedNames(t *testing.T) {
	for _, name := range []string{"list", "import-projects", "state", "sidebar", "goto", "new", "resurrect", "compact", "help"} {
		withConfig(t, "[[project]]\nname = \""+name+"\"\npath = \"/x\"\n")
		_, _, err := execute(t, "", "list")
		if err == nil || !strings.Contains(err.Error(), "clashes with a subcommand") {
			t.Errorf("project %q: err = %v, want subcommand clash", name, err)
		}
	}
}

func TestMissingConfig(t *testing.T) {
	withConfig(t, "")
	_, _, err := execute(t, "", "list")
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "dev import-projects") {
		t.Errorf("err = %v, want not-exist with import hint", err)
	}
}

func TestImportProjects(t *testing.T) {
	path := withConfig(t, "")
	conf := filepath.Join(t.TempDir(), "projects.conf")
	if err := os.WriteFile(conf, []byte("# comment\napp|~/src/app|Main app\nnotes|/tmp/notes|\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, "", "import-projects", "--from", conf)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Imported 2 projects into " + path + "\n"; out != want {
		t.Errorf("import = %q, want %q", out, want)
	}
	out, _, err = execute(t, "", "list")
	if want := "app\tMain app\t~/src/app\nnotes\t\t/tmp/notes\n"; err != nil || out != want {
		t.Errorf("list after import = %q, %v; want %q", out, err, want)
	}
	if _, _, err := execute(t, "", "import-projects", "--from", conf); !errors.Is(err, os.ErrExist) {
		t.Errorf("second import: err = %v, want ErrExist", err)
	}
}

func TestImportRejectsReservedName(t *testing.T) {
	path := withConfig(t, "")
	conf := filepath.Join(t.TempDir(), "projects.conf")
	if err := os.WriteFile(conf, []byte("app|/a\nstate|/s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := execute(t, "", "import-projects", "--from", conf)
	if err == nil || !strings.Contains(err.Error(), `:2: project "state" clashes with a subcommand`) {
		t.Errorf("err = %v, want the clash on line 2", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("projects.toml written despite the clash: %v", err)
	}
}

func TestImportDefaultSource(t *testing.T) {
	withConfig(t, "")
	t.Setenv("HOME", t.TempDir())
	_, _, err := execute(t, "", "import-projects")
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), filepath.Join(os.Getenv("HOME"), ".tmux/sessions/projects.conf")) {
		t.Errorf("err = %v, want missing ~/.tmux/sessions/projects.conf", err)
	}
}

func TestState(t *testing.T) {
	tests := []struct {
		name, pane, stdin string
		args              []string
		wantErr           error // nil = success
		wantArgErr        bool
		wantStderr        string
	}{
		{name: "no TMUX_PANE is a no-op", args: []string{"working"}, stdin: "not json"},
		{name: "malformed JSON reported, exit 0", pane: "%1", args: []string{"working"}, stdin: "{",
			wantStderr: "❌ dev state: agent: hook input: unexpected end of JSON input\n"},
		{name: "subagent skipped", pane: "%1", args: []string{"working"}, stdin: `{"agent_id":"a1"}`},
		{name: "records on the pane", pane: "%1", args: []string{"idle"}, stdin: `{}`, wantErr: tmux.ErrNoSocket},
		{name: "unknown state", args: []string{"busy"}, wantArgErr: true},
		{name: "no state", args: nil, wantArgErr: true},
		{name: "two states", args: []string{"idle", "working"}, wantArgErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX_PANE", tt.pane)
			_, errOut, err := execute(t, tt.stdin, append([]string{"state"}, tt.args...)...)
			switch {
			case tt.wantArgErr:
				if err == nil {
					t.Error("want an argument error")
				}
			case !errors.Is(err, tt.wantErr):
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if errOut != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", errOut, tt.wantStderr)
			}
		})
	}
}
