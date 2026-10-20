package inbox

import (
	"encoding/json"
	"errors"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
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
// hears of (finished, failed, needs a decision), then delivers the inbox if the coordinator is ready. Any
// started worker's agent pane gets the note too (OptNote), also with the inbox off or the boss gone. A
// teammate pane beside it (an agent team's) sends only its decisions, named "<worker> (%N)".
// A worker's new prompt resets its dedupe, so two turns ending alike both reach the boss, and clears its note.
// A rate limit schedules the worker's resume (scheduleResume), which its failed note tells.
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
	if err != nil || w.StartedBy == "" {
		return err
	}
	rows, err := tmux.Query[paneRow](c, "list-panes", "-s", "-t", pane)
	if err != nil {
		return err
	}
	n, _ := FromHook(w.Session, input)
	mate := teammates(rows)[pane]
	if mate && (!ok || n.Kind != Decision) {
		// A teammate: only its decisions, named by its pane. Its prompts mustn't reset the worker's dedupe
		return nil
	}
	var askErr error
	if n.Kind == Decision {
		// Claude's own message ("Claude needs your permission") doesn't say what it asks; it still goes out
		// when the dialog can't be read
		var text string
		if text, askErr = asked(c, w.Question, pane); text != "" {
			n.Text, n.Key = text, Decision+"\x00"+text
		}
	}
	if mate {
		n.Worker += " (" + pane + ")"
		return errors.Join(askErr, deliverNote(c, clk, b, w, n, e.Name))
	}
	var resumeErr error
	switch {
	case e.Name == "StopFailure" && ok:
		// Only a boss's Watch resumes it
		var resume string
		if resume, resumeErr = scheduleResume(c, clk.Now(), pane, input); resume != "" {
			n.Text += "; " + resume
		}
	case e.Name == "Stop":
		// A finished turn ends a run of rate limits
		resumeErr = unsetResume(c, pane, true)
	}
	if ok {
		err = deliverNote(c, clk, b, w, n, e.Name)
	}
	// After the boss's note, which a pane that can't take its own doesn't hold up; a new prompt has none
	return errors.Join(askErr, resumeErr, err, setNote(c, pane, n.Summary()))
}

// asked is what pane's dialog asks (agent.Question.Ask), clipped, when its question (@agent_question) is the
// dialog on its screen (screen.ClaudeDialog): a tool auto mode approved since, or an earlier dialog of the
// turn, may have left another one; "" when none is.
func asked(c *tmux.Client, question, pane string) (string, error) {
	q, ok := agent.ParseQuestion(question)
	if !ok {
		return "", nil
	}
	out, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil || !q.Shows(screen.ClaudeDialog(out)) {
		return "", err
	}
	return clean(q.Ask()), nil
}

// deliverNote queues n for b and delivers the inbox; a new prompt (event UserPromptSubmit) only resets the dedupe.
func deliverNote(c *tmux.Client, clk control.Clock, b Boss, w workerView, n Note, event string) error {
	if event == "UserPromptSubmit" {
		return b.Box.Forget(w.Session)
	}
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

// setNote records summary on pane (OptNote); "" unsets it.
func setNote(c *tmux.Client, pane, summary string) error {
	if summary == "" {
		_, err := c.Run("set-option", "-pu", "-t", pane, OptNote)
		return err
	}
	_, err := c.Run("set-option", "-p", "-t", pane, OptNote, summary)
	return err
}
