//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// notifyRig runs dev state with a PATH of only a tmux link, a fake terminal-notifier that logs its
// arguments, and /usr/bin:/bin, so no real notifier on the machine can fire.
type notifyRig struct {
	s   *server
	log string
	bin string // tmux link + fake terminal-notifier
}

func newNotifyRig(t *testing.T, s *server) *notifyRig {
	return newNotifyRigThen(t, s, "")
}

// newNotifyRigThen's fake notifier runs then after logging.
func newNotifyRigThen(t *testing.T, s *server, then string) *notifyRig {
	t.Helper()
	tmp := t.TempDir()
	r := &notifyRig{s: s, log: filepath.Join(tmp, "notes.log"), bin: filepath.Join(tmp, "bin")}
	writeStubs(t, r.bin, `{ printf '%s\037' "$@"; echo; } >> '`+r.log+`'`+"\n"+then, "terminal-notifier")
	linkTmux(t, r.bin)
	return r
}

func linkTmux(t *testing.T, dir string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tmuxPath, filepath.Join(dir, "tmux")); err != nil {
		t.Fatal(err)
	}
}

// state runs `dev state <state>` for pane with stdin; bin replaces the rig's PATH dir when non-empty.
func (r *notifyRig) state(t *testing.T, pane, state, stdin, bin string) string {
	t.Helper()
	if bin == "" {
		bin = r.bin
	}
	cmd := exec.Command(devBin, "state", state)
	cmd.Env = r.s.scriptEnv("TMUX_PANE="+pane, "PATH="+bin+":/usr/bin:/bin", "__CFBundleIdentifier=com.test.term")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("❌ dev state %s: %v\n%s", state, err, out)
	}
	return string(out)
}

// notes waits until n notifications were logged and returns them; more than n already logged fails the test.
func (r *notifyRig) notes(t *testing.T, n int) [][]string {
	t.Helper()
	var notes [][]string
	waitFor(t, "notifications logged", func() bool {
		data, _ := os.ReadFile(r.log)
		notes = nil
		for line := range strings.Lines(string(data)) {
			notes = append(notes, strings.Split(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\x1f"), "\x1f"))
		}
		return len(notes) >= n
	})
	if len(notes) != n {
		t.Fatalf("❌ %d notifications, want %d: %q", len(notes), n, notes)
	}
	return notes
}

func arg(note []string, flag string) string {
	if i := slices.Index(note, flag); i >= 0 && i+1 < len(note) {
		return note[i+1]
	}
	return "<no " + flag + ">"
}

const (
	needsInput = `{"hook_event_name":"Notification","title":"Permission needed","message":"Claude needs your permission to use Bash"}`
	stopped    = `{"hook_event_name":"Stop","last_assistant_message":"All tests pass."}`
)

// A pane no client shows notifies; non-notifying events and @dev_notify off don't, each followed by a
// sentinel that must be the next note. A missing notifier prints one stderr line and still records the state.
func TestNotifyUnwatched(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "agents", "sleep 600")
	pane := s.T("display", "-t", "=agents:", "-p", "#{pane_id}")
	r := newNotifyRig(t, s)

	r.state(t, pane, "waiting", needsInput, "")
	n := r.notes(t, 1)[0]
	check(t, "title is the session", arg(n, "-title"), "agents")
	check(t, "subtitle", arg(n, "-subtitle"), "Permission needed")
	check(t, "message", arg(n, "-message"), "Claude needs your permission to use Bash")
	check(t, "group per server and pane", arg(n, "-group"), "dev-"+s.T("display", "-p", "#{pid}")+"-"+pane)
	tmuxLink := filepath.Join(r.bin, "tmux")
	check(t, "click switches to the pane on the test server", arg(n, "-execute"),
		tmux.ShellJoin(tmuxLink, "-S", s.socketPath(), "switch-client", "-t", pane))
	check(t, "activate from __CFBundleIdentifier", arg(n, "-activate"), "com.test.term")
	check(t, "sound on needs input", arg(n, "-sound"), "default")
	check(t, "state still recorded", s.T("show-option", "-pqv", "-t", pane, "@agent_state"), "waiting")

	r.state(t, pane, "working", `{"hook_event_name":"PostToolUse"}`, "")
	r.state(t, pane, "idle", `{"hook_event_name":"SessionStart"}`, "")
	r.state(t, pane, "idle", stopped, "")
	n = r.notes(t, 2)[1]
	check(t, "non-notifying events skipped, Stop notifies", arg(n, "-subtitle")+"/"+arg(n, "-message"), "Done/All tests pass.")
	check(t, "no sound on Stop", arg(n, "-sound"), "<no -sound>")

	s.T("set", "-g", "@dev_notify", "off")
	r.state(t, pane, "waiting", needsInput, "")
	s.T("set", "-gu", "@dev_notify")
	s.T("set", "-t", "=agents:", "@dev_notify_activate", "com.other.term")
	r.state(t, pane, "idle", `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "")
	n = r.notes(t, 3)[2]
	check(t, "@dev_notify off skipped, StopFailure notifies", arg(n, "-subtitle")+"/"+arg(n, "-message"), "Failed/rate_limit")
	check(t, "@dev_notify_activate overrides", arg(n, "-activate"), "com.other.term")

	noNotifier := filepath.Join(t.TempDir(), "bin")
	linkTmux(t, noNotifier)
	out := r.state(t, pane, "idle", stopped, noNotifier)
	if !strings.HasPrefix(out, "❌ dev state: notify: ") {
		t.Errorf("❌ missing notifier: output %q, want a ❌ dev state: notify: line", out)
	}
	check(t, "state recorded without a notifier", s.T("show-option", "-pqv", "-t", pane, "@agent_state"), "idle")
}

// A focused client showing the pane's window suppresses; focus-out (as the terminal sends it) notifies.
func TestNotifyWatched(t *testing.T) {
	d := newDevServer(t, "set -g focus-events on\n")
	pane := d.T("display", "-t", "=boot:", "-p", "#{pane_id}")
	r := newNotifyRig(t, d.server)
	focused := func() bool { return strings.Contains(d.T("list-clients", "-F", "#{client_flags}"), "focused") }
	waitFor(t, "client focused", focused)

	r.state(t, pane, "waiting", needsInput, "")
	d.T("split-window", "-t", pane, "sleep 600") // another pane active, window still in view
	r.state(t, pane, "idle", `{"hook_event_name":"Stop","last_assistant_message":"split"}`, "")

	d.outer.T("send-keys", "-t", "out", "-H", "1b", "5b", "4f")
	waitFor(t, "client unfocused", func() bool { return !focused() })
	r.state(t, pane, "idle", stopped, "")
	n := r.notes(t, 1)[0]
	check(t, "watched skipped, unfocused notifies", arg(n, "-message"), "All tests pass.")
}

// A slow notifier doesn't hold up the hook: dev state returns, pipes closed, while it still runs.
func TestNotifyDoesNotWait(t *testing.T) {
	s := newServer(t, "")
	s.T("new-session", "-d", "-s", "agents", "sleep 600")
	pane := s.T("display", "-t", "=agents:", "-p", "#{pane_id}")
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := newNotifyRigThen(t, s, "echo $$ > '"+pidFile+"'; exec sleep 30")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(pidFile); err == nil {
			exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
	})

	start := time.Now()
	r.state(t, pane, "idle", stopped, "")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("❌ dev state took %v with a notifier that sleeps 30s", d)
	}
	r.notes(t, 1)
}

// The click command, run by sh outside tmux as terminal-notifier does, switches the attached client to the
// pane's session, window and pane.
func TestNotifyClickSwitches(t *testing.T) {
	d := newDevServer(t, "set -g focus-events on\n")
	d.T("new-session", "-d", "-s", "agents", "sleep 600")
	target := d.T("split-window", "-t", "=agents:1", "-PF", "#{pane_id}", "sleep 600")
	d.T("select-pane", "-t", "=agents:1.1")
	d.T("new-window", "-t", "=agents:", "sleep 600")
	r := newNotifyRig(t, d.server)

	r.state(t, target, "waiting", needsInput, "")
	cmd := exec.Command("/bin/sh", "-c", arg(r.notes(t, 1)[0], "-execute"))
	cmd.Env = d.tmux.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("❌ click command: %v\n%s", err, out)
	}
	check(t, "client switched to the session", d.T("list-clients", "-F", "#{client_session}"), "agents")
	check(t, "window and pane selected", d.T("display", "-t", "=agents:", "-p", "#{window_index}/#{pane_id}"), "1/"+target)
}
