package open

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

func TestAgentCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		worker bool
		start  agent.Start
		want   []string
	}{
		{"named dev-x in Remote Control", false, agent.Start{}, []string{"claude", "--remote-control=dev-x"}},
		{"worker: Remote Control off", true, agent.Start{}, []string{"claude", "--settings", `{"remoteControlAtStartup":false}`}},
		{"worker with a model and effort", true, agent.Start{Model: "sonnet", Effort: "medium"},
			[]string{"claude", "--model", "sonnet", "--effort", "medium", "--settings", `{"remoteControlAtStartup":false}`}},
		{"effort alone", false, agent.Start{Effort: "low"}, []string{"claude", "--effort", "low", "--remote-control=dev-x"}},
	} {
		got, err := agentCommand("claude", "dev-x", tt.worker, tt.start)
		if want := tmux.ShellJoin(append([]string{exe, "pty-wrap", "--close-on-exit", "--"}, tt.want...)...); err != nil || got != want {
			t.Errorf("%s: claude = %q, %v; want %q", tt.name, got, err, want)
		}
	}
	if got, _ := agentCommand("codex", "dev-x", false, agent.Start{}); got != "codex" {
		t.Errorf("codex = %q, want it as is", got)
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

// noServer is a tmux client with no server to answer.
var noServer = tmux.NewFake(func([]string) (string, error) { return "", errors.New("no server running") })

func TestSizeSourcesOrder(t *testing.T) {
	origTTY, origTput := ttySize, tputSize
	t.Cleanup(func() { ttySize, tputSize = origTTY, origTput })
	tputSize = fixed("3", "3", true)
	env := map[string]string{"COLUMNS": "2", "LINES": "2"}
	size := func() string {
		w, h := terminalSize(sizeSources(noServer, false, "", func(k string) string { return env[k] })...)
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

func TestSizeSourcesOutsideTmuxNoTTY(t *testing.T) {
	origTTY, origTput := ttySize, tputSize
	t.Cleanup(func() { ttySize, tputSize = origTTY, origTput })
	ttySize, tputSize = fixed("1", "1", true), fixed("70", "20", true)
	var clients string
	c := tmux.NewFake(func(args []string) (string, error) {
		switch strings.Join(args, " ") {
		case "display-message -p -t =boss: #{window_width}" + tmux.Sep + "#{window_height}":
			return "200" + tmux.Sep + "50\n", nil
		case "display-message -p -t =gone: #{window_width}" + tmux.Sep + "#{window_height}":
			return tmux.Sep + "\n", nil // tmux prints empty formats for a missing session
		case "display-message -p #{client_width}" + tmux.Sep + "#{client_height}":
			return "100" + tmux.Sep + "30\n", nil
		case "list-clients -F #{client_width}" + tmux.Sep + "#{client_height}":
			return clients, nil
		}
		return "", fmt.Errorf("unexpected tmux %q", args)
	})
	size := func(boss string) string {
		w, h := terminalSize(sizeSources(c, false, boss, func(string) string { return "" })...)
		return fmt.Sprintf("%dx%d", w, h)
	}

	if got := size("boss"); got != "200x50" {
		t.Errorf("--boss: %s, want the boss's window to beat the tty", got)
	}
	if w, h := terminalSize(sizeSources(c, true, "boss", func(string) string { return "" })...); w != 200 || h != 50 {
		t.Errorf("--boss in tmux: %dx%d, want the boss's window to beat the client", w, h)
	}
	ttySize = fixed("", "", false)
	clients = "90" + tmux.Sep + "30\n" + "160" + tmux.Sep + "40\n" + "120" + tmux.Sep + "60\n"
	if got := size("gone"); got != "160x40" {
		t.Errorf("boss gone, no tty: %s, want the widest attached client", got)
	}
	if got := size(""); got != "160x40" {
		t.Errorf("no boss, no tty: %s, want the widest attached client", got)
	}
	clients = ""
	if got := size(""); got != "70x20" {
		t.Errorf("no boss, tty or client: %s, want tput", got)
	}
	if w, h := terminalSize(sizeSources(noServer, false, "boss", func(string) string { return "" })...); w != 70 || h != 20 {
		t.Errorf("no server: %dx%d, want tput", w, h)
	}
}

func TestTmuxSizeOutsideTmuxDefers(t *testing.T) {
	// The client is never used: outside tmux the source must not query it.
	if _, _, ok := tmuxSize(nil, false)(); ok {
		t.Error("tmux source answered outside tmux")
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

// A session that exists by now (another start made it meanwhile) keeps its claude: another model or effort for
// it is refused, not dropped.
func TestRunExistingSessionRefusesStart(t *testing.T) {
	t.Setenv("TMUX", "")
	c := tmux.NewFake(func(args []string) (string, error) {
		if args[0] == "display-message" && args[len(args)-1] == "#{@dev_model}\t#{@dev_effort}" {
			return "\tmedium\n", nil
		}
		return "", nil
	})
	opts := Options{Path: "/p", Name: "x", Detach: true}
	if err := Run(c, opts, &bytes.Buffer{}); err != nil {
		t.Errorf("reused without a model or effort = %v, want none", err)
	}
	opts.Start = agent.Start{Effort: "medium"}
	if err := Run(c, opts, &bytes.Buffer{}); err != nil {
		t.Errorf("reused with the effort it started with = %v, want none", err)
	}
	opts.Start = agent.Start{Effort: "low"}
	if err := Run(c, opts, &bytes.Buffer{}); err == nil || !strings.HasPrefix(err.Error(), "session x was started with --effort medium, not low") {
		t.Errorf("reused with another effort = %v, want it refused", err)
	}
}
