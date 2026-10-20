//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dialogStub is an agent whose "ask" prompt waits on a permission dialog the way Claude reports one: dev
// question (PermissionRequest), then dev state waiting (Notification), and the dialog drawn at the bottom.
// The key that answers it is printed; PostToolUse and Stop follow.
func dialogStub(t *testing.T, dir string) string {
	t.Helper()
	dev := shellQuote(devBin)
	return filepath.Join(writeStubs(t, dir, `hook() { printf '%s' "$2" | `+dev+` state "$1"; }
hook idle '{"hook_event_name":"SessionStart"}'
while IFS= read -r line; do
	echo "got: $line"
	hook working '{"hook_event_name":"UserPromptSubmit"}'
	case "$line" in ask*)
		printf '%s' '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"Remove the build dir"}}' | `+dev+` question
		hook waiting '{"hook_event_name":"Notification","notification_type":"permission_prompt"}'
		printf ' Bash command\n   rm -rf build\n   Remove the build dir\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n'
		stty -icanon -echo
		k=$(dd bs=1 count=1 2>/dev/null)
		stty icanon echo
		# Claude takes its dialog off the screen once answered
		printf '\033[2J\033[H'
		echo "answered: $k"
		hook working '{"hook_event_name":"PostToolUse"}'
	esac
	hook idle '{"hook_event_name":"Stop"}'
done`, "agent"), "agent")
}

// herdServer is a dev server with extraConf, a boss "herd" whose coordinator is a stub beside the previews
// pane, and its worker w1, a dialogStub registered idle.
func herdServer(t *testing.T, extraConf string) (d *devServer, w1, coord, previews string) {
	t.Helper()
	d = newDevServer(t, "set -g @dev_notify off\n"+extraConf)
	stub := dialogStub(t, t.TempDir())
	d.T("new-session", "-d", "-s", "w1", stub)
	w1 = d.T("display", "-t", "=w1:", "-p", "#{pane_id}")
	env := d.scriptEnv()
	waitFor(t, "stub registered", func() bool {
		out, _, _ := devAgent(env, "", "list")
		return strings.Contains(out, "w1\t") && strings.Contains(out, "\tidle\t")
	})
	// A coordinator for w1's inbox news to reach, beside the previews
	tmp := t.TempDir()
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	d.T("new-session", "-d", "-s", "herd", "bash", script, "", filepath.Join(tmp, "boss.log"))
	d.T("set-option", "-t", "=herd:", "@dev_herd", "1")
	d.T("set-option", "-t", "=w1:", "@dev_started_by", d.T("display", "-t", "=herd:", "-p", "#{session_id}"))
	coord = d.T("display", "-t", "=herd:", "-p", "#{pane_id}")
	cmd := exec.Command(devBin, "state", "idle")
	cmd.Env = d.scriptEnv("TMUX_PANE=" + coord)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("❌ coordinator state: %v %s", err, out)
	}
	previews = d.T("split-window", "-t", coord, "-h", "-PF", "#{pane_id}", devBin+" boss --previews")
	d.T("switch-client", "-t", "=herd:")
	return d, w1, coord, previews
}

// The boss previews' reply box answers a worker's permission dialog: it shows as a picker with the hook's
// command, its tile framed in the box's accent; a digit sends that key, the answer is verified, and the
// question is cleared once the worker moves on.
func TestHerdPicker(t *testing.T) {
	d, w1, _, previews := herdServer(t, "")
	env := d.scriptEnv()

	_, errOut, status := devAgent(env, "", "prompt", "w1", "ask")
	check(t, "prompt", status+errOut, "0")
	waitFor(t, "w1 waiting with its question", func() bool {
		return d.T("show-option", "-pqv", "-t", w1, "@agent_question") != "" && strings.Contains(d.T("capture-pane", "-p", "-t", previews), "w1 Claude · waiting")
	})
	d.T("send-keys", "-t", previews, "i")
	waitPane(t, d, previews, "the picker", func(screen string) bool {
		return strings.Contains(screen, "▎ Bash  rm -rf build") && strings.Contains(screen, "▎ ❯ 1. Yes") && strings.Contains(screen, "▎   2. No")
	})
	if styled := d.T("capture-pane", "-p", "-e", "-t", previews); !strings.Contains(styled, "48;2;228;104;118") {
		t.Errorf("❌ w1's tile header isn't on its red accent:\n%s", styled)
	}

	d.T("send-keys", "-t", previews, "2")
	waitFor(t, "w1 got the key", func() bool {
		return strings.Contains(d.T("capture-pane", "-p", "-t", w1), "answered: 2")
	})
	waitPane(t, d, previews, "answer verified", func(screen string) bool {
		return strings.Contains(screen, "Answered w1")
	})
	waitFor(t, "question cleared", func() bool {
		return d.T("show-option", "-pqv", "-t", w1, "@agent_question") == ""
	})
	// Past the answer's timeout, nothing reports a failure
	time.Sleep(3 * time.Second)
	screen := d.T("capture-pane", "-p", "-t", previews)
	if strings.Contains(screen, "❌") || strings.Contains(screen, "⚠") || !strings.Contains(screen, "⏎ send · esc cancel") {
		t.Errorf("❌ answer reported a failure:\n%s", screen)
	}
}

// waitPane is waitFor on pane's screen, showing the screen when it times out.
func waitPane(t *testing.T, d *devServer, pane, desc string, cond func(screen string) bool) {
	t.Helper()
	var screen string
	for range 60 {
		if screen = d.T("capture-pane", "-p", "-t", pane); cond(screen) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("❌ timeout waiting for: %s; screen:\n%s", desc, screen)
}

// A click on a tile opens its reply box, framed in the tile's accent; focus moving to the coordinator closes
// it back to the hint row, and focus coming back doesn't reopen it.
func TestHerdReplyBlur(t *testing.T) {
	d, _, coord, previews := herdServer(t, "set -g mouse on\n")
	d.T("select-pane", "-t", coord)
	waitPane(t, d, previews, "w1's tile", func(screen string) bool { return strings.Contains(screen, "w1 Claude · idle") })
	const accent = "48;2;152;187;108" // idle green as a background
	actClick(d, previews, 2, 0)
	waitPane(t, d, previews, "the reply box", func(screen string) bool { return strings.Contains(screen, "esc cancel") })
	check(t, "the click focused the previews", d.T("display", "-t", previews, "-p", "#{pane_active}"), "1")
	if styled := d.T("capture-pane", "-p", "-e", "-t", previews); !strings.Contains(styled, accent) {
		t.Errorf("❌ w1's tile header isn't on its accent:\n%s", styled)
	}

	d.T("select-pane", "-t", coord)
	waitPane(t, d, previews, "the box closed", func(screen string) bool {
		return !strings.Contains(screen, "esc cancel") && strings.Contains(screen, "i reply")
	})
	if styled := d.T("capture-pane", "-p", "-e", "-t", previews); strings.Contains(styled, accent) {
		t.Errorf("❌ w1's tile header still on its accent:\n%s", styled)
	}

	d.T("select-pane", "-t", previews)
	time.Sleep(2500 * time.Millisecond)
	if screen := d.T("capture-pane", "-p", "-t", previews); strings.Contains(screen, "esc cancel") {
		t.Errorf("❌ focus back reopened the box:\n%s", screen)
	}
}
