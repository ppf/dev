package inbox

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/notify"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// AgyPermission records agy pane st, working, as waiting on the permission prompt its screen (capture) shows
// (agent.AgyPermission), and gives its boss the needs-a-decision note and the user the notification that Claude's
// Notification hook would. recorded=false when the screen shows none or the pane moved on meanwhile.
func AgyPermission(c *tmux.Client, clk control.Clock, pane string, st control.Status, capture string) (recorded bool, err error) {
	tool, what, title, ok := screen.AgyPermission(capture)
	if !ok {
		return false, nil
	}
	q, recorded, err := agent.AgyPermission(c, pane, st.Since, st.Prompts, st.Active, tool, what, title)
	if !recorded || err != nil {
		return false, err
	}
	input, err := json.Marshal(map[string]string{"hook_event_name": "Notification", "notification_type": agent.PermissionPrompt,
		"title": "Needs approval", "message": q.Ask()})
	if err != nil {
		return true, err
	}
	return true, errors.Join(WorkerHook(c, clk, pane, input), notify.Run(c, pane, input, os.Getenv("__CFBundleIdentifier")))
}
