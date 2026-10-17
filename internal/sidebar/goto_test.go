package sidebar

import (
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestGoto(t *testing.T) {
	sessions := "list-sessions -F #{session_name}\x1f#{@project}\x1f#{session_id}\x1f#{@dev_started_by}\x1f"
	clients := "list-clients -F #{client_tty}\x1f#{client_session}\x1f#{client_activity}\x1f"
	out := map[string]string{
		// Order: alpha, app, app-2 (group app), boot
		sessions:                                 tmux.FormatRow("boot", "", "$boot", "") + tmux.FormatRow("app-2", "", "$app-2", "") + tmux.FormatRow("app", "", "$app", "") + tmux.FormatRow("alpha", "", "$alpha", ""),
		clients:                                  tmux.FormatRow("/dev/a", "app", "1"),
		"display-message -t =app: -p #{pane_id}": "%1\n",
		ensHidden:                                "1\n",
	}
	tests := []struct {
		name string
		n    int
		want []string
	}{
		{"zero", 0, []string{sessions}},
		{"past the end", 5, []string{sessions}},
		{"current session only selected", 2, []string{sessions, "set-option -gq @sidebar_selected_session app",
			clients, "display-message -t =app: -p #{pane_id}"}},
		{"switches the client", 3, []string{sessions, "set-option -gq @sidebar_selected_session app-2",
			clients, "display-message -t =app: -p #{pane_id}", ensHidden,
			"switch-client -c /dev/a -t =app-2:", "switch-client -c /dev/a -T root"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, log := ensTmux(out)
			if err := Goto(c, tt.n, "/dev/a"); err != nil {
				t.Fatal(err)
			}
			ensCheck(t, log(), tt.want)
		})
	}

	c, log := ensTmux(nil)
	if err := Goto(c, 1, ""); err != nil {
		t.Fatal(err)
	}
	ensCheck(t, log(), []string{sessions})

	c, _ = ensTmux(out)
	if err := Goto(c, 1, "/dev/gone"); err == nil {
		t.Error("unknown client: want error")
	}
}
