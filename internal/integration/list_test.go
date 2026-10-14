//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dev import-projects, dev list and an unknown `dev <name>` against select.sh on the same projects.conf.
// Neither path reaches tmux.
func TestListParity(t *testing.T) {
	selectScript := filepath.Join(tmuxHome(), "sessions/select.sh")
	home := t.TempDir() // select.sh reads $HOME/.tmux/sessions/projects.conf
	conf := filepath.Join(home, ".tmux/sessions/projects.conf")
	writeFile(t, conf, "# name|path|description\napp|~/src/app|Main app\nnotes|/tmp/notes|\ndocs|~/docs|Docs | extra\n")
	cfgHome := t.TempDir()
	toml := filepath.Join(cfgHome, "dev/projects.toml")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+cfgHome)
	// run returns stdout, stderr and the exit status
	run := func(name string, args ...string) (string, string, string) {
		cmd := exec.Command(name, args...)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), errStatus(err)
	}

	out, errOut, status := run(devBin, "import-projects")
	check(t, "import-projects", out+errOut+status, "Imported 3 projects into "+toml+"\n0")

	devOut, _, devStatus := run(devBin, "list")
	bashOut, _, bashStatus := run(selectScript, "list")
	check(t, "list (dev = bash)", devOut+devStatus, bashOut+bashStatus)
	check(t, "dev list", devOut, "app\tMain app\t~/src/app\nnotes\t\t/tmp/notes\ndocs\tDocs \t~/docs\n")

	devOut, devErr, devStatus := run(devBin, "nope")
	bashOut, bashErr, bashStatus := run(selectScript, "nope")
	check(t, "unknown name (dev = bash, config path aside)", devOut+strings.ReplaceAll(devErr, toml, conf)+devStatus, bashOut+bashErr+bashStatus)
	check(t, "dev unknown name", devErr+devStatus, "❌ Project 'nope' not found in "+toml+"\nAvailable:\n  app\n  notes\n  docs\n1")
}
