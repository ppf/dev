package inbox

import (
	"encoding/json"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
)

// bossEvent is the part of the coordinator's own hook input BossHook reads.
type bossEvent struct {
	Name       string `json:"hook_event_name"`
	StopActive bool   `json:"stop_hook_active"`
}

// BossHook hands pending notes to a coordinator through its own Claude hooks: on Stop it blocks the stop
// with the notes as the reason, so Claude carries on with them (busy=true: record it as working, skip the
// done notification); on UserPromptSubmit it adds them as context to the user's prompt. out is the hook's
// stdout JSON, "" for none. A Stop already continuing from a block (stop_hook_active) is let through, so
// a boss never loops on its own hook. Any pane but a coordinator's is left alone.
func BossHook(c *tmux.Client, pane string, input []byte) (out string, busy bool, err error) {
	var e bossEvent
	if json.Unmarshal(input, &e) != nil || e.Name != "Stop" && e.Name != "UserPromptSubmit" || e.StopActive {
		return "", false, nil
	}
	b, ok, err := Self(c, pane)
	if !ok || err != nil {
		return "", false, err
	}
	bt, err := b.Box.Take()
	if err != nil || len(bt.Notes) == 0 {
		return "", false, err
	}
	var v any = map[string]any{"decision": "block", "reason": bt.String()}
	if e.Name == "UserPromptSubmit" {
		v = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": e.Name, "additionalContext": bt.String()}}
	}
	data, err := json.Marshal(v)
	if err != nil {
		if rerr := b.Box.Restore(bt); rerr != nil {
			return "", false, rerr
		}
		return "", false, err
	}
	return string(data), e.Name == "Stop", nil
}

// WorkerHook queues a note for the boss that started pane's session when the hook event is one a boss
// hears of (finished, failed, needs a decision), then delivers the inbox if the coordinator is ready.
// A worker's new prompt resets its dedupe, so two turns ending alike both reach the boss.
func WorkerHook(c *tmux.Client, clk control.Clock, pane string, input []byte) error {
	var e bossEvent
	if json.Unmarshal(input, &e) != nil {
		return nil
	}
	if _, ok := FromHook("", input); !ok && e.Name != "UserPromptSubmit" {
		// Most hook events (tool use) never concern a boss: no tmux call for them
		return nil
	}
	b, w, ok, err := BossOf(c, pane)
	if !ok || err != nil {
		return err
	}
	if e.Name == "UserPromptSubmit" {
		return b.Box.Forget(w.Session)
	}
	n, _ := FromHook(w.Session, input)
	if n.Kind == Decision {
		// Record already moved @agent_since into this wait: two dialogs alike in one turn are two notes
		n.Key += "\x00" + w.Since
	}
	added, err := b.Box.Add(n)
	if !added || err != nil {
		return err
	}
	_, err = Deliver(c, clk, b)
	return err
}
