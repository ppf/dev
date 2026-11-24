package bossclear

import (
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// Right before the /clear, a boss that runs another conversation than the one that saved, or had a turn since its
// handoff passed, is refused; as is a draft in its input box.
func TestUnchanged(t *testing.T) {
	const empty = "────────────────────\n❯ \n────────────────────\n"
	for _, tt := range []struct {
		name, now, screen, want string
	}{
		{"another conversation", "s1 4", empty, "runs another conversation"},
		{"a turn since", "s0 5", empty, "had a turn since its handoff passed"},
		{"a draft", "s0 4", "────────────────────\n❯ half a sentence\n────────────────────\n", "input box holds text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := tmux.NewFake(func(args []string) (string, error) {
				switch args[0] {
				case "display-message":
					return tt.now + "\n", nil
				case "capture-pane":
					return tt.screen, nil
				}
				return "", nil
			})
			err := unchanged(c, control.Agent{Session: "boss-app", Pane: "%1"}, "s0", "s0 4")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("unchanged = %v, want %q", err, tt.want)
			}
		})
	}
}

// Right before the /clear, a note queued in the boss's inbox refuses it; none passes.
func TestUnchangedInbox(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "list-panes":
			return tmux.FormatRow("boss-app", "1", "%1", "/p/boss", "t", "claude", "idle", "claude", "", "", "", "$1", "", "1", "", "", "", "", "", "", "", "", "", "", ""), nil
		case "capture-pane":
			return "────────────────────\n❯ \n────────────────────\n", nil
		case "display-message":
			if strings.Contains(args[len(args)-1], "@dev_boss_inbox") {
				return tmux.FormatRow("$1", "1700000000", "boss-app", "1", "", "/tmp/tmux-1/default"), nil
			}
			return "s0 4\n", nil
		}
		return "", nil
	})
	a := control.Agent{Session: "boss-app", Pane: "%1"}
	if err := unchanged(c, a, "s0", "s0 4"); err != nil {
		t.Fatalf("unchanged with nothing queued = %v", err)
	}
	box, ok, err := inbox.Coordinator(c, "%1")
	if !ok || err != nil {
		t.Fatalf("Coordinator = %v, %v", ok, err)
	}
	if _, err := box.Box.Add(inbox.Note{Worker: "w", Kind: inbox.Finished, Text: "w finished", Key: "k"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := unchanged(c, a, "s0", "s0 4"); err == nil || !strings.Contains(err.Error(), "inbox notes wait for boss-app") {
		t.Errorf("unchanged with a note queued = %v", err)
	}
}
