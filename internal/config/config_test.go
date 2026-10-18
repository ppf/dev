package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultPath(t *testing.T) {
	tests := []struct{ xdg, want string }{
		{"/xdg", "/xdg/dev/projects.toml"},
		{"", "/home/u/.config/dev/projects.toml"},
	}
	for _, tt := range tests {
		t.Setenv("HOME", "/home/u")
		t.Setenv("XDG_CONFIG_HOME", tt.xdg)
		got, err := DefaultPath()
		if err != nil || got != tt.want {
			t.Errorf("XDG_CONFIG_HOME=%q: DefaultPath = %q, %v; want %q", tt.xdg, got, err, tt.want)
		}
	}
}

func TestStateDir(t *testing.T) {
	for xdg, want := range map[string]string{"/xdg": "/xdg/dev", "": "/home/u/.local/state/dev"} {
		t.Setenv("HOME", "/home/u")
		t.Setenv("XDG_STATE_HOME", xdg)
		if got, err := StateDir(); err != nil || got != want {
			t.Errorf("XDG_STATE_HOME=%q: StateDir = %q, %v; want %q", xdg, got, err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	reserved := []string{"list", "sidebar"}
	tests := []struct {
		name    string
		toml    string
		want    []Project
		wantErr string
	}{
		{
			name: "file order kept",
			toml: `
[[project]]
name = "zeta"
path = "~/z"
description = "Zeta"

[[project]]
name = "alpha"
path = "/a"
`,
			want: []Project{{"zeta", "~/z", "Zeta"}, {"alpha", "/a", ""}},
		},
		{name: "empty file", toml: "", want: nil},
		{name: "missing name", toml: "[[project]]\npath = \"/a\"\n", wantErr: "project #1 has no name"},
		{name: "missing path", toml: "[[project]]\nname = \"a\"\n", wantErr: `project "a" has no path`},
		{
			name:    "duplicate name",
			toml:    "[[project]]\nname = \"a\"\npath = \"/a\"\n[[project]]\nname = \"a\"\npath = \"/b\"\n",
			wantErr: `duplicate project "a"`,
		},
		{
			name:    "reserved name",
			toml:    "[[project]]\nname = \"list\"\npath = \"/a\"\n",
			wantErr: `project "list" clashes with a subcommand`,
		},
		{name: "invalid toml", toml: "[[project]\n", wantErr: "projects.toml:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(write(t, "projects.toml", tt.toml), reserved)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Load = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml"), nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestFind(t *testing.T) {
	projects := []Project{{Name: "a", Path: "/1"}, {Name: "b", Path: "/2"}}
	if p, ok := Find(projects, "b"); !ok || p.Path != "/2" {
		t.Errorf("Find(b) = %+v, %v", p, ok)
	}
	if _, ok := Find(projects, "c"); ok {
		t.Error("Find(c) found a project")
	}
}

func TestDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	tests := []struct{ path, want string }{
		{"~", "/home/u"},
		{"~/", "/home/u/"},
		{"~/code/x", "/home/u/code/x"},
		{"~other/x", "~other/x"},
		{"/abs/x", "/abs/x"},
		{"rel/x", "rel/x"},
	}
	for _, tt := range tests {
		got, err := Project{Path: tt.path}.Dir()
		if err != nil || got != tt.want {
			t.Errorf("Dir(%q) = %q, %v; want %q", tt.path, got, err, tt.want)
		}
	}
}

func TestImport(t *testing.T) {
	tests := []struct {
		name    string
		conf    string
		want    []Project
		wantErr string
	}{
		{
			name: "example format",
			conf: "# format: name|path|description\n" +
				"zeta|~/z|Zeta Project\n" +
				"\n" +
				"alpha|/a|\n" +
				"beta|/b\n",
			want: []Project{{"zeta", "~/z", "Zeta Project"}, {"alpha", "/a", ""}, {"beta", "/b", ""}},
		},
		{
			name: "description stops at the third field like awk $3",
			conf: "a|/a|one|two\n",
			want: []Project{{"a", "/a", "one"}},
		},
		{
			name: "fields not trimmed",
			conf: "a | /a |  d \n",
			want: []Project{{"a ", " /a ", "  d "}},
		},
		{name: "indented hash is not a comment", conf: "a|/a\n #x|/x\n", want: []Project{{"a", "/a", ""}, {" #x", "/x", ""}}},
		{name: "only comments", conf: "# nothing\n", want: nil},
		{name: "no path", conf: "# c\na|/a\nb\n", wantErr: ":3: want name|path|description"},
		{name: "empty path", conf: "b||desc\n", wantErr: ":1:"},
		{name: "empty name", conf: "\n\n|/a|desc\n", wantErr: ":3:"},
		{name: "whitespace-only line", conf: "a|/a\n   \n", wantErr: ":2:"},
		{name: "duplicate name", conf: "a|/a\n# c\na|/b\n", wantErr: `:3: duplicate project "a"`},
		{name: "reserved name", conf: "a|/a\nlist|/l\n", wantErr: `:2: project "list" clashes with a subcommand`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := write(t, "projects.conf", tt.conf)
			tomlPath := filepath.Join(t.TempDir(), "nested", "dev", "projects.toml")
			n, err := Import(conf, tomlPath, []string{"list"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if _, err := os.Stat(tomlPath); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("toml written despite parse error: %v", err)
				}
				return
			}
			if err != nil || n != len(tt.want) {
				t.Fatalf("Import = %d, %v; want %d", n, err, len(tt.want))
			}
			got, err := Load(tomlPath, nil)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("round trip Load = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestImportRefusesOverwrite(t *testing.T) {
	conf := write(t, "projects.conf", "a|/a|A\n")
	tomlPath := write(t, "projects.toml", "keep me\n")
	if _, err := Import(conf, tomlPath, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want ErrExist", err)
	}
	if data, _ := os.ReadFile(tomlPath); string(data) != "keep me\n" {
		t.Fatalf("existing file changed: %q", data)
	}
}
