//go:build integration

package integration

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func TestHarnessOwnsOnlyItsServer(t *testing.T) {
	if _, err := tmux.New().Run("list-sessions"); !errors.Is(err, tmux.ErrNoSocket) {
		t.Fatalf("default-server client under test: err = %v, want ErrNoSocket", err)
	}

	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "own")
	path := s.socketPath()
	tmuxEnv := envValue(s.scriptEnv(), "TMUX")
	if !strings.HasPrefix(tmuxEnv, path+",") {
		t.Errorf("script env TMUX = %q, want the isolated socket %s", tmuxEnv, path)
	}

	s.kill()
	if _, err := s.Try("has-session"); err == nil {
		t.Error("server still running after kill")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket %s left behind: %v", path, err)
	}
}

func TestClientAgainstRealTmux(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "dev-2", "-x", "80", "-y", "24")
	s.T("set-option", "-t", tmux.SessionTarget("dev-2"), "@project", "dev")

	type session struct {
		Name    string `tmux:"session_name"`
		Missing string `tmux:"@not_set"`
		Project string `tmux:"@project"`
		Windows int    `tmux:"session_windows"`
	}
	c := tmux.NewIsolated(s.socket, "")
	got, err := tmux.Query[session](c, "list-sessions")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (session{Name: "dev-2", Project: "dev", Windows: 1}) {
		t.Errorf("Query = %+v", got)
	}

	if _, err := s.Try("has-session", "-t", "dev"); err != nil {
		t.Errorf("plain target should prefix-match dev-2: %v", err)
	}
	_, err = c.Run("has-session", "-t", tmux.SessionTarget("dev"))
	var tmuxErr *tmux.Error
	if !errors.As(err, &tmuxErr) || tmuxErr.Stderr == "" {
		t.Errorf("exact target =dev: should fail with stderr, got %v", err)
	}
}

// envValue returns the last value of key in env, the one exec uses.
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, key+"="); ok {
			v = rest
		}
	}
	return v
}

// tmux prints control characters as "_" for a client in a non-UTF-8 locale outside tmux (a shell
// with no LANG, e.g. some SSH logins), which would turn Query's \x1f separator into "_".
func TestQueryInNonUTF8Locale(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "loc", "-x", "80", "-y", "24")

	type session struct {
		Name    string `tmux:"session_name"`
		Windows int    `tmux:"session_windows"`
	}
	c := tmux.NewIsolated(s.socket, "", "LC_ALL=C", "LC_CTYPE=C", "LANG=C")
	got, err := tmux.Query[session](c, "list-sessions")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (session{Name: "loc", Windows: 1}) {
		t.Errorf("Query = %+v", got)
	}
}

// A server up with no session yet (still reading its config) fails list-panes -a with "no current target": Live
// reads it as no sessions, as it does no server.
func TestLiveOnServerWithNoSession(t *testing.T) {
	s := newServer(t, "")
	s.T("start-server", ";", "set", "-g", "exit-empty", "off")
	if _, err := s.Try("list-panes", "-a"); !tmux.IsNoSession(err) {
		t.Fatalf("list-panes -a on no session: err = %v, want no current target", err)
	}
	if got, err := sidebar.Live(s.tmux); got != nil || err != nil {
		t.Errorf("Live = %v, %v; want none, no error", got, err)
	}
}
