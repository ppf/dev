package open

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestSessionName(t *testing.T) {
	if got := sessionName("app", "feature/one/two"); got != "app-feature-one-two" {
		t.Errorf("sessionName = %q", got)
	}
}

func fixed(w, h string, ok bool) sizeSource {
	return func() (string, string, bool) { return w, h, ok }
}

func TestTerminalSize(t *testing.T) {
	unused := func() (string, string, bool) {
		t.Fatal("source consulted after the chain stopped")
		return "", "", false
	}
	tests := []struct {
		name    string
		sources []sizeSource
		w, h    int
	}{
		{"first answer wins", []sizeSource{fixed("", "", false), fixed("120", "40", true), unused}, 120, 40},
		{"empty answer stops the chain", []sizeSource{fixed("", "", true), unused}, 80, 24},
		{"non-numeric per dimension", []sizeSource{fixed("12x", "50", true)}, 80, 50},
		{"negative is non-numeric", []sizeSource{fixed("200", "-1", true)}, 200, 24},
		{"no source answers", []sizeSource{fixed("1", "1", false)}, 80, 24},
	}
	for _, tt := range tests {
		if w, h := terminalSize(tt.sources...); w != tt.w || h != tt.h {
			t.Errorf("%s: %dx%d, want %dx%d", tt.name, w, h, tt.w, tt.h)
		}
	}
}

func TestEnvSizeNeedsBoth(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	if w, h, ok := envSize(env(map[string]string{"COLUMNS": "100", "LINES": "30"}))(); !ok || w != "100" || h != "30" {
		t.Errorf("both set: %q %q %v", w, h, ok)
	}
	if _, _, ok := envSize(env(map[string]string{"COLUMNS": "100"}))(); ok {
		t.Error("COLUMNS alone answered")
	}
}

func TestSizeSourcesOrder(t *testing.T) {
	origTTY, origTput := ttySize, tputSize
	t.Cleanup(func() { ttySize, tputSize = origTTY, origTput })
	tputSize = fixed("3", "3", true)
	env := map[string]string{"COLUMNS": "2", "LINES": "2"}
	size := func() string {
		w, h := terminalSize(sizeSources(nil, false, func(k string) string { return env[k] })...)
		return fmt.Sprintf("%dx%d", w, h)
	}

	ttySize = fixed("1", "1", true)
	if got := size(); got != "1x1" {
		t.Errorf("tty answering: %s, want stty to beat COLUMNS/LINES", got)
	}
	ttySize = fixed("", "", false)
	if got := size(); got != "2x2" {
		t.Errorf("no tty: %s, want COLUMNS/LINES to beat tput", got)
	}
	clear(env)
	if got := size(); got != "3x3" {
		t.Errorf("no tty or env: %s, want tput", got)
	}
}

func TestTmuxSizeOutsideTmuxDefers(t *testing.T) {
	// The client is never used: outside tmux the source must not query it.
	if _, _, ok := tmuxSize(nil, false)(); ok {
		t.Error("tmux source answered outside tmux")
	}
}

func TestUniqueName(t *testing.T) {
	taken := []string{"app", "app-2", "app-3", "app-10"}
	var asked []string
	exists := func(name string) (bool, error) {
		asked = append(asked, name)
		return slices.Contains(taken, name), nil
	}
	got, err := uniqueName("app", exists)
	if err != nil || got != "app-4" {
		t.Fatalf("uniqueName = %q, %v", got, err)
	}
	if want := []string{"app", "app-2", "app-3", "app-4"}; !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}

	if got, _ := uniqueName("free", exists); got != "free" {
		t.Errorf("free name = %q", got)
	}

	boom := errors.New("boom")
	if _, err := uniqueName("app", func(string) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestWorktreeFailureShownOnce(t *testing.T) {
	cause := errors.New("Not a git repo: /tmp/x")
	origEnsure, origTerminal := ensureWorktree, stdinIsTerminal
	t.Cleanup(func() { ensureWorktree, stdinIsTerminal = origEnsure, origTerminal })
	stdinIsTerminal = func() bool { return false }
	// Outside tmux notify only prints; inside it would also reach for a server.
	t.Setenv("TMUX", "")

	for _, tc := range []struct {
		name    string
		notices []string
		want    string
	}{
		{"failure only", nil, "❌ Not a git repo: /tmp/x\n"},
		{"notices echoed first", []string{"ℹ  Branch feat exists", "⚠  x"}, "ℹ  Branch feat exists\n⚠  x\n❌ Not a git repo: /tmp/x\n"},
	} {
		ensureWorktree = func(repoPath, name string, notify func(string)) (string, error) {
			if repoPath != "/home/u/x" || name != "feat" {
				t.Errorf("Ensure(%q, %q)", repoPath, name)
			}
			for _, n := range tc.notices {
				notify(n)
			}
			return "", cause
		}
		var out bytes.Buffer
		err := Run(tmux.New(), Options{Path: "/home/u/x", Name: "x", Worktree: "feat"}, &out)
		if !errors.Is(err, ErrShown) || !errors.Is(err, cause) {
			t.Errorf("%s: err = %v, want ErrShown wrapping the cause", tc.name, err)
		}
		if got := out.String(); got != tc.want {
			t.Errorf("%s: out = %q, want %q", tc.name, got, tc.want)
		}
	}
}
