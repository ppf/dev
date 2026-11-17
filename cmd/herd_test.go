package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// Without a terminal, bare dev boss starts the manager detached in its own folder: never the picker.
func TestBossWithoutTerminalStartsManager(t *testing.T) {
	path := withConfig(t, projectsTOML)
	orig := hasTerminal
	t.Cleanup(func() { hasTerminal = orig })
	hasTerminal = func() bool { return false }
	for _, name := range []string{"boss", "herd"} {
		// No tmux server here: reaching tmux proves it went for the manager
		if _, _, err := execute(t, "", name, "--model", "opus"); !errors.Is(err, tmux.ErrNoSocket) {
			t.Errorf("%s: err = %v, want ErrNoSocket", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(path), "manager")); err != nil || !info.IsDir() {
		t.Errorf("manager folder not made: %v", err)
	}
}

func TestBossProject(t *testing.T) {
	withConfig(t, projectsTOML)
	if _, _, err := execute(t, "", "boss", "app"); !errors.Is(err, tmux.ErrNoSocket) {
		t.Errorf("boss app: err = %v, want ErrNoSocket", err)
	}
	if _, _, err := execute(t, "", "boss", "nope"); err == nil || err.Error() != "project nope not found" {
		t.Errorf("boss nope: err = %v, want project nope not found", err)
	}
}
