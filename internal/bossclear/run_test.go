package bossclear

import (
	"strings"
	"testing"

	"github.com/ppf/dev/internal/control"
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
