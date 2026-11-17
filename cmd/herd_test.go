package cmd

import (
	"errors"
	"testing"

	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/tmux"
)

func TestBossWithoutTerminalListsProjects(t *testing.T) {
	withConfig(t, projectsTOML)
	orig := hasTerminal
	t.Cleanup(func() { hasTerminal = orig })
	hasTerminal = func() bool { return false }
	for _, name := range []string{"boss", "herd"} {
		// Failing before tmux proves no boss was opened.
		out, errOut, err := execute(t, "", name, "--model", "opus")
		if !errors.Is(err, open.ErrShown) {
			t.Errorf("%s: err = %v, want ErrShown (already printed)", name, err)
		}
		want := "❌ No terminal for the project picker: run dev boss <project>\nAvailable:\n  app\n  notes\n"
		if out != "" || errOut != want {
			t.Errorf("%s: stdout %q, stderr %q; want stderr %q", name, out, errOut, want)
		}
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
