//go:build integration

package integration

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// bashRef is the last bash version in ppf/terminal; history keeps it after the scripts are deleted.
const bashRef = "ac2cc87fda9ee3e14b08dd574badfca8fc5838e2"

var (
	// devBin is the dev binary built once for the whole suite.
	devBin string
	// checkout is this repo's root, resolved before TestMain leaves the package dir.
	checkout string
	// bashHome is every test server's HOME: the bash reference at bashRef with .tmux/plugins linked to
	// the real plugins.
	bashHome string
)

// TestMain pins a UTF-8 locale, builds dev and bashHome, kills servers left by an earlier run that died
// without cleanup (go test -timeout panics skip t.Cleanup) and kills this run's servers on SIGINT/SIGTERM.
func TestMain(m *testing.M) {
	// Every server, client and script inherits it: tmux draws UTF-8 only in a UTF-8 locale, and bash's
	// ${#x} counts bytes otherwise, so "·" and "●" lines would pad differently from dev's.
	os.Setenv("LC_ALL", "en_US.UTF-8")
	killServers(func(pid int) bool { return !alive(pid) })
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		killServers(func(pid int) bool { return pid == os.Getpid() })
		os.Exit(1)
	}()
	dir, err := os.MkdirTemp("", "dev-it-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	devBin = filepath.Join(dir, "dev")
	build := exec.Command("go", "build", "-o", devBin, "github.com/ppf/dev")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "build dev:", err)
		os.Exit(1)
	}
	bashHome = filepath.Join(dir, "home")
	if err := newBashHome(bashHome); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "bash home:", err)
		os.Exit(1)
	}
	if checkout, err = filepath.Abs("../.."); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Sessions created without -c start in this cwd; a checkout under .claude/worktrees would make the
	// sidebar treat every such session as a worktree and ask before killing it.
	if err := os.Chdir(dir); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newBashHome lays out the bash reference from the ppf/terminal clone ($DEV_TERMINAL_REPO, default
// ~/PhpstormProjects/ppf/terminal) at bashRef in home, as the export installs it, and links
// home/.tmux/plugins to the real ~/.tmux/plugins.
func newBashHome(home string) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	repo := os.Getenv("DEV_TERMINAL_REPO")
	if repo == "" {
		repo = filepath.Join(userHome, "PhpstormProjects/ppf/terminal")
	}
	files := map[string]string{
		"tmux/.tmux.conf":                          ".tmux.conf",
		"configs/claude/hooks/tmux-agent-state.sh": ".claude/hooks/tmux-agent-state.sh",
	}
	for _, name := range []string{"select", "project", "sidebar", "toggle-sidebar", "sidebar-action", "install-sidebar-keys", "dev-resurrect", "compact"} {
		files["tmux/sessions/"+name+".sh"] = ".tmux/sessions/" + name + ".sh"
	}
	for src, dst := range files {
		out, err := exec.Command("git", "-C", repo, "show", bashRef+":"+src).Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return fmt.Errorf("%s at %s from the ppf/terminal clone %s (DEV_TERMINAL_REPO overrides it): %w", src, bashRef, repo, err)
		}
		mode := os.FileMode(0o755)
		if !strings.HasSuffix(dst, ".sh") {
			mode = 0o644
		}
		dst = filepath.Join(home, dst)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, out, mode); err != nil {
			return err
		}
	}
	return os.Symlink(filepath.Join(userHome, ".tmux/plugins"), filepath.Join(home, ".tmux/plugins"))
}
