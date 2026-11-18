package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

// bossEvent is the part of the coordinator's own hook input BossHook reads.
type bossEvent struct {
	Name       string `json:"hook_event_name"`
	StopActive bool   `json:"stop_hook_active"`
	Prompt     string `json:"prompt"`
	Source     string `json:"source"`
	Tool       string `json:"tool_name"`
}

// BossHook hands pending notes to a coordinator through its own Claude hooks: on Stop it blocks the stop
// with the notes as the reason, so Claude carries on with them (busy=true: record it as working, skip the
// done notification); on UserPromptSubmit it adds them as context to the user's prompt. out is the hook's
// stdout JSON, "" for none. A Stop already continuing from a block (stop_hook_active) is let through, so
// a boss never loops on its own hook. A UserPromptSubmit showing the batch sent over the socket clears it (Arrived).
// Notes it hands over, or a prompt that is a batch Deliver pasted or sent, come with the boss's open to do
// list (todo.List.Reminder), so it ticks what they closed in the same turn; a list it can't read is err, the
// notes still out. On SessionStart it gives the coordinator its open list (startList). Any pane but a
// coordinator's is left alone. A PostToolUse of the coordinator's AskUserQuestion keeps the user's pick (Box.Picked).
func BossHook(c *tmux.Client, pane string, input []byte) (out string, busy bool, err error) {
	var e bossEvent
	if json.Unmarshal(input, &e) != nil {
		return "", false, nil
	}
	if e.Name == "SessionStart" {
		if e.Source == "clear" || e.Source == "startup" {
			// A new conversation never saw the user's earlier words
			if b, ok, err := Coordinator(c, pane); ok && err == nil {
				if err := b.Box.ForgetPrompt(); err != nil {
					return "", false, err
				}
			}
		}
		out, err = startList(c, pane, e.Source)
		return out, false, err
	}
	if e.Name == "PostToolUse" && e.Tool == agent.AskUserQuestion {
		picks := agent.HookPick(input)
		if len(picks) == 0 {
			return "", false, nil
		}
		// Only the manager relays a pick (dev todo answer --via manager)
		b, ok, err := Coordinator(c, pane)
		if !ok || err != nil || b.Name != Manager {
			return "", false, err
		}
		return "", false, b.Box.Picked(picks, time.Now())
	}
	if e.Name != "Stop" && e.Name != "UserPromptSubmit" || e.StopActive {
		return "", false, nil
	}
	if e.Name == "UserPromptSubmit" {
		b, ok, err := Coordinator(c, pane)
		if !ok || err != nil {
			return "", false, err
		}
		if err := b.Box.Arrived(e.Prompt, time.Now()); err != nil {
			return "", false, err
		}
	}
	b, ok, err := Self(c, pane)
	if !ok || err != nil {
		return "", false, err
	}
	bt, err := b.Box.Take()
	if err != nil {
		return "", false, err
	}
	if len(bt.Notes) == 0 && (e.Name != "UserPromptSubmit" || !strings.Contains(e.Prompt, Header)) {
		return "", false, nil
	}
	text := bt.String()
	list, listErr := openList(c, pane)
	switch {
	case text == "":
		text = list
	case list != "":
		text += "\n\n" + list
	}
	if text == "" {
		return "", false, listErr
	}
	var v any = map[string]any{"decision": "block", "reason": text}
	if e.Name == "UserPromptSubmit" {
		v = promptContext(text)
	}
	data, err := json.Marshal(v)
	if err != nil {
		if rerr := b.Box.Restore(bt); rerr != nil {
			return "", false, rerr
		}
		return "", false, err
	}
	return string(data), e.Name == "Stop", listErr
}

// openList is the open to do list of the boss session pane is in (todo.List.Reminder), "" when none is open.
func openList(c *tmux.Client, pane string) (string, error) {
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return "", err
	}
	now := time.Now()
	l, err := s.Read(now)
	if err != nil {
		return "", err
	}
	// The list still goes out: what it says of each item holds without the workers
	err = l.MarkGone(func() ([]todo.Session, error) { return todo.Workers(c, pane) })
	return l.Reminder(now), err
}

// startList is the SessionStart hook output giving a dev boss coordinator pane its open to do list; "" otherwise.
// Matched by pane role: its first SessionStart comes before dev registers it as an agent.
func startList(c *tmux.Client, pane, source string) (string, error) {
	// herd.OptPane and herd.RoleCoordinator: herd imports this package
	role, err := c.Run("display-message", "-p", "-t", pane, "#{@dev_boss_pane}")
	if err != nil || strings.TrimSpace(role) != "coordinator" {
		return "", err
	}
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		// A restored boss is marked one only after its coordinator starts
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return StartContext(s, func() ([]todo.Session, error) { return todo.Workers(c, pane) }, source, time.Now())
}

// StartContext is the SessionStart hook output giving a boss list s's open items, gone workers marked; "" after
// a compaction, whose summary carries them.
func StartContext(s todo.Store, workers func() ([]todo.Session, error), source string, now time.Time) (string, error) {
	if source == "compact" {
		return "", nil
	}
	l, err := s.Read(now)
	if err != nil {
		return "", err
	}
	if err := l.MarkGone(workers); err != nil {
		return "", err
	}
	age := "just now"
	if d := now.Sub(time.Unix(l.Updated, 0)); d >= time.Minute {
		age = Span(d) + " ago"
	}
	text := l.StartReminder(source == "startup", age, now)
	if text == "" {
		return "", nil
	}
	data, err := json.Marshal(hookContext("SessionStart", text))
	return string(data), err
}

// hookContext is a hook event's output adding text to Claude's context.
func hookContext(event, text string) any {
	return map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}}
}

// promptContext is a UserPromptSubmit hook's output adding text to the prompt.
func promptContext(text string) any { return hookContext("UserPromptSubmit", text) }

// PromptContext is promptContext as the hook's stdout JSON.
func PromptContext(text string) (string, error) {
	data, err := json.Marshal(promptContext(text))
	return string(data), err
}

// WorkerHook queues a note for the boss that started pane's session when the hook event is one a boss
// hears of (finished, failed, needs a decision, asks), then delivers the inbox if the coordinator is ready. Any
// started worker's agent pane gets the note too (OptNote), also with the inbox off or the boss gone. A
// teammate pane beside it (an agent team's) sends only its decisions, named "<worker> (%N)".
// A worker's new prompt resets its dedupe, so two turns ending alike both reach the boss, and clears its note.
// A rate limit schedules the worker's resume (scheduleResume), which its failed note tells. A turn ending in a
// planned wait (dev agent wait-until) sends a waiting note in place of finished; a prompt ends the wait. A worker that is itself
// a boss sends only the notes up reports: not its finished turns.
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
	var wt wait.Wait
	if own := slices.IndexFunc(rows, func(r paneRow) bool { return r.Pane == pane }); own >= 0 {
		wt = wait.FromOptions(rows[own].WaitUntil, rows[own].WaitReason)
	}
	switch {
	case e.Name == "UserPromptSubmit" && wt.Held():
		// A prompt ends a planned wait
		resumeErr = wait.Unset(c, pane)
	case e.Name == "Stop" && wt.Held():
		// A finished turn ends a run of rate limits; one past its wait's time (its own background task woke it) ends the wait
		resumeErr = unsetResume(c, pane, true)
		switch {
		case wt.Due(clk.Now()):
			resumeErr = errors.Join(resumeErr, wait.Unset(c, pane))
		case n.Kind == Finished:
			n.Kind, n.Text = Waits, wt.Phrase(clk.Now())+", dev wakes it then"
			n.Key = n.Kind + "\x00" + n.Text
		}
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
	if ok && (w.Herd == "" || up(n.Kind) || e.Name == "UserPromptSubmit") {
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
	n.Outside = w.Outside != ""
	if n.Kind == Decision {
		// Record already moved @agent_since into this wait: two dialogs alike in one turn are two notes
		n.Key += "\x00" + w.Since
	}
	added, err := b.Box.Add(n, clk.Now())
	if !added || err != nil {
		return err
	}
	return Deliver(c, clk, b)
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

// WaitsOn tells the boss that started the boss session pane is in (a boss under a boss) that item id of its list
// waits on the user: a WaitsOnYou note with the item's text and options, delivered. Nothing for a boss no boss
// started, or whose boss is gone or has its inbox off. queued reports the note in that boss's inbox, err then
// being its delivery's.
func WaitsOn(c *tmux.Client, clk control.Clock, pane string, id int, text string, options []string) (queued bool, err error) {
	b, w, ok, err := BossOf(c, pane)
	if !ok || err != nil || w.Herd == "" {
		return false, err
	}
	if len(options) > 0 {
		text += " (options: " + strings.Join(options, " / ") + ")"
	}
	text = clean(fmt.Sprintf("#%d %s", id, text))
	// Each its own: an item given back with the same text is news again
	key := WaitsOnYou + "\x00" + strconv.FormatInt(clk.Now().UnixNano(), 10)
	return b.Send(c, clk, Note{Worker: w.Session, Kind: WaitsOnYou, Text: text, Key: key, Outside: w.Outside != ""})
}
