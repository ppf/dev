// Package config loads ~/.config/dev/projects.toml and imports the old projects.conf.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Project is one [[project]] entry of projects.toml, in file order.
type Project struct {
	Name        string `toml:"name"`
	Path        string `toml:"path"` // as written; may start with ~
	Description string `toml:"description"`
}

type file struct {
	Project []Project `toml:"project"`
}

// DefaultPath is $XDG_CONFIG_HOME/dev/projects.toml, else ~/.config/dev/projects.toml.
func DefaultPath() (string, error) {
	// Not os.UserConfigDir: on macOS that is ~/Library/Application Support.
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "dev", "projects.toml"), nil
}

// Load reads and validates projects.toml: names non-empty, unique and not in reserved (subcommand
// names); paths non-empty.
func Load(path string, reserved []string) ([]Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	seen := make(map[string]bool, len(f.Project))
	for i, p := range f.Project {
		if p.Name == "" {
			return nil, fmt.Errorf("config: %s: project #%d has no name", path, i+1)
		}
		if err := checkName(p.Name, seen, reserved); err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("config: %s: project %q has no path", path, p.Name)
		}
	}
	return f.Project, nil
}

// checkName rejects a name already seen or reserved, then records it as seen.
func checkName(name string, seen map[string]bool, reserved []string) error {
	switch {
	case seen[name]:
		return fmt.Errorf("duplicate project %q", name)
	case slices.Contains(reserved, name):
		return fmt.Errorf("project %q clashes with a subcommand", name)
	}
	seen[name] = true
	return nil
}

// Find returns the project called name.
func Find(projects []Project, name string) (Project, bool) {
	for _, p := range projects {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// Dir returns Path with a leading ~ or ~/ expanded to $HOME.
func (p Project) Dir() (string, error) {
	if p.Path != "~" && !strings.HasPrefix(p.Path, "~/") {
		return p.Path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	if p.Path == "~" {
		return home, nil
	}
	// Plain concatenation like project.sh's "$HOME/${PROJECT_PATH:2}", so "~/" keeps its trailing slash.
	return home + "/" + p.Path[2:], nil
}

// Import converts projects.conf (name|path|description lines, # comments) to projects.toml at
// tomlPath, creating parent dirs; it refuses to overwrite an existing file and, like Load, duplicate or
// reserved names. Returns the project count.
func Import(confPath, tomlPath string, reserved []string) (int, error) {
	projects, err := parseConf(confPath, reserved)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(tomlPath), 0o755); err != nil {
		return 0, fmt.Errorf("config: %w", err)
	}
	out, err := os.OpenFile(tomlPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, fmt.Errorf("config: %w", err)
	}
	err = toml.NewEncoder(out).Encode(file{Project: projects})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("config: write %s: %w", tomlPath, err)
	}
	return len(projects), nil
}

// parseConf reads projects.conf the way select.sh does: grep -v '^#', then fields 1-3 of awk -F'|'.
func parseConf(path string, reserved []string) ([]Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var projects []Project
	seen := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("config: %s:%d: want name|path|description, got %q", path, i+1, line)
		}
		if err := checkName(fields[0], seen, reserved); err != nil {
			return nil, fmt.Errorf("config: %s:%d: %w", path, i+1, err)
		}
		p := Project{Name: fields[0], Path: fields[1]}
		if len(fields) > 2 {
			p.Description = fields[2]
		}
		projects = append(projects, p)
	}
	return projects, nil
}
