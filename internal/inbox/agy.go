package inbox

import (
	"encoding/json"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// AgyPermission records agy pane st, working, as waiting on the permission prompt its screen (capture) shows
// (agent.AgyPermission) and gives its boss the needs-a-decision note that Claude's Notification hook would. input is
// that Notification, for notify.Run; nil when the screen shows none or the pane moved on meanwhile.
func AgyPermission(c *tmux.Client, clk control.Clock, pane string, st control.Status, capture string) (input []byte, err error) {
	tool, what, title, ok := screen.AgyPermission(capture)
	if !ok {
		return nil, nil
	}
	q, recorded, err := agent.AgyPermission(c, pane, st.Since, st.Prompts, tool, what, title)
	if !recorded || err != nil {
		return nil, err
	}
	input, err = json.Marshal(map[string]string{"hook_event_name": "Notification", "notification_type": agent.PermissionPrompt,
		"title": "Needs approval", "message": q.Ask()})
	if err != nil {
		return nil, err
	}
	return input, WorkerHook(c, clk, pane, input)
}
