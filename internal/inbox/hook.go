package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/selfreload"
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
	Session    string `json:"session_id"` // the conversation (todo.Store.Remind)
}

// BossHook hands pending notes to a coordinator through its own Claude hooks: on Stop it blocks the stop
// with the notes as the reason, so Claude carries on with them (busy=true: record it as working, skip the
// done notification); on UserPromptSubmit it adds them as context to the user's prompt. out is the hook's
// stdout JSON, "" for none. A Stop already continuing from a block (stop_hook_active) is let through, so
// a boss never loops on its own hook. A UserPromptSubmit showing the batch sent over the socket clears it (Arrived).
// Notes it hands over, or a prompt that is a batch Deliver pasted or sent, come with the boss's open to do
// list (todo.Store.Remind), so it ticks what they closed in the same turn; a list it can't read is err, the
// notes still out. On SessionStart it gives the coordinator its open list (startList), then hint's line. Any pane
// but a coordinator's is left alone. A PostToolUse of the coordinator's AskUserQuestion keeps the user's pick (Box.Picked).
func BossHook(c *tmux.Client, pane string, input []byte, hint Hint) (out string, busy bool, err error) {
	var e bossEvent
	if json.Unmarshal(input, &e) != nil {
		return "", false, nil
	}
	if e.Name == "SessionStart" {
		var ferr error
		switch e.Source {
		case "clear", "startup":
			// A new conversation never saw the user's earlier words
			if b, ok, err := Coordinator(c, pane); ok && err == nil {
				ferr = b.Box.ForgetPrompt()
			}
		case "resume":
			// A restarted coordinator has no dialog up
			if b, ok, err := Coordinator(c, pane); ok && err == nil {
				ferr = b.Box.ClearAsking()
			}
		}
		out, err = startList(c, pane, e.Source, e.Session, hint)
		return out, false, errors.Join(ferr, err)
	}
	if e.Name == "PostToolUse" && e.Tool == agent.AskUserQuestion {
		picks := agent.HookPick(input)
		if len(picks) == 0 {
			// Its dialog is gone without a pick: the manager's question isn't up any more
			b, ok, err := Coordinator(c, pane)
			if !ok || err != nil || b.Name != Manager {
				return "", false, err
			}
			return "", false, b.Box.ClearAsking()
		}
		// Only the manager relays a pick (dev todo answer --via manager)
		b, ok, err := Coordinator(c, pane)
		if !ok || err != nil || b.Name != Manager {
			return "", false, err
		}
		now := time.Now()
		if err := b.Box.Picked(picks, now); err != nil {
			return "", false, err
		}
		return "", false, recordHandOn(c, picks, now)
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
	werr := b.Box.WithdrawTicked(listItem(c, time.Now()))
	if _, _, held, err := clearing(c, pane); held || err != nil {
		// Its notes wait for the clear's end (dev agent clear refuses while any wait)
		return "", false, errors.Join(werr, err)
	}
	bt, err := b.Box.Take()
	if err != nil {
		return "", false, errors.Join(werr, err)
	}
	if len(bt.Notes) == 0 && (e.Name != "UserPromptSubmit" || !strings.Contains(e.Prompt, Header)) {
		return "", false, werr
	}
	text := bt.String()
	list, listErr := openList(c, pane, e.Session)
	listErr = errors.Join(werr, listErr)
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

// openList is the open to do list of the boss session pane is in, after its items ticked by themselves, told once
// (todo.Store.Remind: the items changed since the last one); "" when none ticked and none is open.
func openList(c *tmux.Client, pane, conversation string) (string, error) {
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
	gone := l.MarkGone(func() ([]todo.Session, error) { return todo.Workers(c, pane) })
	ticked, err := s.TakeTicked(now, nil, nil)
	if err != nil {
		return "", errors.Join(gone, err)
	}
	if gone != nil {
		// Its gone items unmarked: the full list, and the next one too
		return l.Reminder(ticked, now), errors.Join(gone, s.ForgetShown())
	}
	return s.Remind(l, ticked, conversation, now)
}

// startList is the SessionStart hook output giving a dev boss coordinator pane its open to do list and notes, after
// a dev agent clear's /clear (or a compaction while one runs) its handoff's line (handoffLine), and after a compaction
// first what it still owes (owedLines), also without a list; an error reading one still gives the others, with why; ""
// otherwise. A fresh start (startup) first records the dev version it started on (todo.OptVersion).
// Matched by pane role: its first SessionStart comes before dev registers it as an agent.
func startList(c *tmux.Client, pane, source, session string, hint Hint) (string, error) {
	// herd.OptPane and herd.RoleCoordinator: herd imports this package
	role, err := c.Run("display-message", "-p", "-t", pane, "#{@dev_boss_pane}")
	if err != nil || strings.TrimSpace(role) != "coordinator" {
		return "", err
	}
	var verr error
	if source == "startup" {
		// A fresh start only: a /clear or resume keeps the prompt and settings it started with; a failure still gives the list
		_, verr = c.Run("set-option", "-p", "-t", pane, todo.OptVersion, strconv.FormatInt(time.Now().Unix(), 10)+" "+devVersion())
	}
	// Its list as dev's rules leave it; a failure still gives the list
	eerr := expirePane(c, pane, time.Now())
	var owed string
	var oerr error
	if source == "compact" {
		if owed, oerr = compactOwed(c, pane, time.Now()); oerr != nil {
			owed = "[dev] dev couldn't tell what you still owe from before the compaction (" + oerr.Error() + "): re-check the " +
				"user's last picks and the items you marked asked (dev todo --all) before acting."
		}
	}
	var handoff string
	var herr error
	if source == "clear" || source == "compact" {
		if handoff, herr = handoffLine(c, pane); herr != nil && source == "clear" {
			// Its list still goes out, and the boss learns a handoff may wait
			handoff = "dev couldn't tell whether a dev agent clear left you a handoff (" + herr.Error() + "): if one cleared " +
				"you just now, its handoff is the newest file in $XDG_STATE_HOME/dev/handoff/<your boss session>/, written " +
				"minutes ago; an older one isn't yours to act on."
		}
	}
	last, lerr := lastSession(c, pane, source, time.Now())
	handoff = strings.TrimSpace(handoff + "\n" + last)
	out, err := listContext(c, pane, source, session, hint, owed, handoff)
	if err != nil && (owed != "" || handoff != "") {
		// The boss still gets what it owes and its handoff, and why its list is missing
		data, jerr := json.Marshal(hookContext("SessionStart", strings.TrimSpace(owed+"\n"+handoff)+
			"\ndev couldn't give your list and notes ("+err.Error()+"): run dev todo and dev todo note."))
		return string(data), errors.Join(verr, eerr, oerr, herr, lerr, err, jerr)
	}
	return out, errors.Join(verr, eerr, oerr, herr, lerr, err)
}

// lastSession writes a coordinator's last-session files on a fresh start or /clear, a boss's starting a new session in
// its list, and returns the line naming them; none on a resume or compaction, as the same session goes on.
func lastSession(c *tmux.Client, pane, source string, now time.Time) (string, error) {
	if source != "startup" && source != "clear" {
		return "", nil
	}
	name, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); name == Manager {
		return managerLastLine(c, now)
	}
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	how := EndedGoneStart
	if source == "clear" {
		how = EndedCleared
	}
	_, err = EndBossSession(c, s, name, how, 0, 0, true, now)
	path, perr := LastSessionPath(name)
	if perr != nil {
		return "", errors.Join(err, perr)
	}
	if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
		return "", err
	} else if serr != nil {
		return "", errors.Join(err, serr)
	}
	line := "Your last session's file: " + path + ": read it before new work (dev wrote it from your list, workers and " +
		"messages; its first part is your state note). Keep that state note current as you go: dev todo note state " +
		"\"<half-way>; next: <step>\" --why \"<why>\"."
	if err != nil {
		line = "dev couldn't write your last session's file (" + err.Error() + "): " + path + " is from an earlier " +
			"session; check dev todo and dev todo note before new work."
	}
	return line, err
}

// managerLastLine writes the last-session files of the bosses gone without one, then names the files there are.
func managerLastLine(c *tmux.Client, now time.Time) (string, error) {
	_, err := WriteGone(c, now)
	dir, derr := LastSessionDir()
	if derr != nil {
		return "", errors.Join(err, derr)
	}
	paths, gerr := filepath.Glob(filepath.Join(dir, "*.md"))
	if gerr != nil || len(paths) == 0 {
		return "", errors.Join(err, gerr)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = strings.TrimSuffix(filepath.Base(p), ".md")
	}
	return "Bosses' last-session files in " + dir + " (" + strings.Join(names, ", ") + "): read a boss's <boss>.md " +
		"before handing it work.", err
}

// listContext is startList's output from the boss's list and notes, owed's lines (when any) first and the handoff line
// (when any) after hint's.
func listContext(c *tmux.Client, pane, source, session string, hint Hint, owed, handoff string) (string, error) {
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		// A restored boss is marked one only after its coordinator starts
		text := strings.TrimSpace(owed + "\n" + handoff)
		if text == "" {
			return "", nil
		}
		data, err := json.Marshal(hookContext("SessionStart", text))
		return string(data), err
	}
	if err != nil {
		return "", err
	}
	if handoff != "" {
		listHint := hint
		hint = func(s todo.Store) string { return strings.TrimSpace(listHint(s) + "\n" + handoff) }
	}
	return StartContext(s, func() ([]todo.Session, error) { return todo.Workers(c, pane) }, hint, owed, source, session, time.Now())
}

// expirePane runs dev's rules at now on the list of the boss session pane is in (expire); none for a pane in no boss
// session.
func expirePane(c *tmux.Client, pane string, now time.Time) error {
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		return nil
	}
	if err != nil {
		return err
	}
	return expire(c, s, now)
}

// devVersion is the version of the running dev, which a coordinator's fresh start records (todo.OptVersion); tests
// replace it.
var devVersion = selfreload.Version

// handoffLine is the start line naming the handoff of the dev agent clear that, still running, cleared pane's
// conversation (never a leftover's); "" for none.
func handoffLine(c *tmux.Client, pane string) (string, error) {
	conversation, path, held, err := clearing(c, pane)
	if err != nil || !held {
		return "", err
	}
	ended, err := c.Run("display-message", "-p", "-t", pane, "#{"+agent.OptEndedSession+"}")
	if err != nil || conversation != strings.TrimSpace(ended) {
		return "", err
	}
	return "Your handoff from before this /clear: " + path + ". Read it before acting; dev agent clear asks you for a proof of it next.", nil
}

// Hint is the line telling the boss of list s, when s is empty, that an earlier boss's list waits (dev todo
// take-over), "" for none; it checks s and reports its own failures.
type Hint func(s todo.Store) string

// StartContext is the SessionStart hook output giving a boss lead's lines (when any), then list s's open items, gone
// workers marked, then its notes, then hint's line; also after a compaction, as its summary may not keep them.
func StartContext(s todo.Store, workers func() ([]todo.Session, error), hint Hint, lead, source, conversation string, now time.Time) (string, error) {
	l, err := s.Read(now)
	if err != nil {
		return "", err
	}
	if err := l.MarkGone(workers); err != nil {
		return "", err
	}
	notes, err := s.Notes(now)
	if err != nil {
		return "", err
	}
	// Taken last: a reminder that doesn't go out leaves them for the next
	ticked, err := s.TakeTicked(now, nil, nil)
	if err != nil {
		return "", err
	}
	age := "just now"
	if d := now.Sub(time.Unix(l.Updated, 0)); d >= time.Minute {
		age = Span(d) + " ago"
	}
	// The list goes out even when what it named isn't kept
	list, shownErr := s.StartRemind(l, source == "startup", age, ticked, conversation, now)
	parts := []string{lead, list, todo.NotesText(notes, now), hint(s)}
	text := strings.Join(slices.DeleteFunc(parts, func(t string) bool { return t == "" }), "\n")
	if text == "" {
		return "", shownErr
	}
	data, err := json.Marshal(hookContext("SessionStart", text))
	return string(data), errors.Join(shownErr, err)
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
// planned wait (dev agent wait-until) sends a waiting note in place of finished; a prompt ends the wait, a background
// task's end (agent.TaskEnded) doesn't. A worker that is itself a boss sends only the notes up reports:
// not its finished turns.
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
	if e.Name == "UserPromptSubmit" && wt.Held() && agent.TaskEnded(e.Prompt) {
		// Its own background task's turn: the wait, its note and the boss's dedupe stay
		return askErr
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
	var handErr error
	if ok && n.Kind == Decision && w.Herd == "" && w.Outside == "" {
		n.Handed, handErr = handOver(c, clk, b, w, n)
	}
	if ok && n.Kind != Decision {
		// The worker moved on: its hand-over mark goes, or a decision of the same second (@agent_since) would match it
		handErr = b.Box.unhand(w.Session)
	}
	if ok && (w.Herd == "" || up(n.Kind) || e.Name == "UserPromptSubmit") {
		err = deliverNote(c, clk, b, w, n, e.Name)
	}
	// After the boss's note, which a pane that can't take its own doesn't hold up; a new prompt has none
	return errors.Join(askErr, resumeErr, handErr, err, setNote(c, pane, n.Summary()))
}

// handOver queues worker w's decision n in the Manager's inbox, once per wait, and delivers it, when w's boss b runs
// under the live Manager: the Manager asks the user and sends their pick to w itself, so the user is asked once.
// handed reports it there (queued now or before), kept in b's box for Watch's stuck note; else b relays it as before.
func handOver(c *tmux.Client, clk control.Clock, b Boss, w workerView, n Note) (handed bool, err error) {
	m, ok, err := managerOver(c, b)
	if !ok || err != nil {
		return false, err
	}
	// One per wait: a dialog read or not, its Notification and PermissionRequest are one question
	n.Of, n.Key = b.Name, Decision+"\x00"+w.Since
	if _, err := m.Box.Add(n, clk.Now()); err != nil {
		return false, err
	}
	return true, errors.Join(b.Box.update(func(s *state) error {
		s.health(w.Session).Handed = w.Since
		return nil
	}), Deliver(c, clk, m))
}

// HandedToManager reports the decision worker pane shows, in trusted state, as one handOver gave the live Manager:
// its pick is the Manager's to send until the worker moves on (a new @agent_since).
func HandedToManager(c *tmux.Client, pane, state string) (bool, error) {
	b, w, ok, err := BossOf(c, pane)
	if !ok || err != nil || !agent.Deciding(state, w.WaitingFor) {
		return false, err
	}
	if _, live, err := managerOver(c, b); !live || err != nil {
		return false, err
	}
	s, err := b.Box.locked()
	if err != nil {
		return false, err
	}
	h := s.Workers[w.Session]
	return h != nil && h.Handed != "" && h.Handed == w.Since, nil
}

// ManagerOver is managerOver for the boss session named boss.
func ManagerOver(c *tmux.Client, boss string) (Boss, bool, error) {
	return managerOver(c, Boss{Name: boss})
}

// managerOver is the Manager when it started or adopted boss b and is alive with its inbox on and its Claude running.
func managerOver(c *tmux.Client, b Boss) (Boss, bool, error) {
	m, _, ok, err := BossOf(c, tmux.SessionTarget(b.Name))
	if !ok || err != nil || m.Name != Manager {
		return Boss{}, false, err
	}
	a, err := control.Resolve(c, Manager)
	if control.NoAgent(err) {
		return Boss{}, false, nil
	}
	if err != nil {
		return Boss{}, false, err
	}
	// remain-on-exit keeps a crashed coordinator's pane
	dead, err := c.Run("display-message", "-p", "-t", a.Pane, "#{pane_dead}")
	if err != nil || strings.TrimSpace(dead) == "1" {
		return Boss{}, false, err
	}
	return m, true, nil
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
// being its delivery's; noState why the note went without the item's state.
func WaitsOn(c *tmux.Client, clk control.Clock, pane string, id int, text string, options []string) (queued bool, noState, err error) {
	b, w, ok, err := BossOf(c, pane)
	if !ok || err != nil || w.Herd == "" {
		return false, nil, err
	}
	// Still sent without its state: the item is on the list
	state, noState := itemState(c, pane, id, text, clk.Now())
	if len(options) > 0 {
		text += " (options: " + strings.Join(options, " / ") + ")"
	}
	// The state outside the clip: it never cuts the options
	if state != "" {
		state = clean(state) + ": "
	}
	text = fmt.Sprintf("#%d %s", id, state) + clean(text)
	// Each its own: an item given back with the same text is news again
	key := WaitsOnYou + "\x00" + strconv.FormatInt(clk.Now().UnixNano(), 10)
	queued, err = b.Send(c, clk, Note{Worker: w.Session, Kind: WaitsOnYou, Text: text, Key: key, Outside: w.Outside != ""})
	return queued, noState, err
}

// itemState is the mark and who it waits on (todo.List.ItemState) of item id reading text on the list of the boss
// session pane is in, so a note about it names them as dev has them; "" with the error when the list can't say.
func itemState(c *tmux.Client, pane string, id int, text string, now time.Time) (string, error) {
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return "", err
	}
	l, err := s.Read(now)
	if err != nil {
		return "", err
	}
	state, ok := l.ItemState(id, text)
	if !ok {
		return "", fmt.Errorf("#%d %q left the list", id, text)
	}
	return state, nil
}

// AnsweredUp tells the boss above, as WaitsOn, that the user answered ask it of the boss session pane is in, answer
// being the note that boss got: an AskAnswered note, its state after its number, so the boss above drops its question
// for it; noState says why that state isn't there.
func AnsweredUp(c *tmux.Client, clk control.Clock, pane string, it todo.Item, answer Note) (queued bool, noState, err error) {
	b, w, ok, err := BossOf(c, pane)
	if !ok || err != nil || w.Herd == "" {
		return false, nil, err
	}
	// The answered item itself: a list set since may give its number to another
	num, text := fmt.Sprintf("#%d ", it.ID), clean(answer.Text)
	if strings.HasPrefix(text, num) {
		// The state outside the clip
		text = num + clean(it.State()) + ": " + strings.TrimPrefix(text, num)
	} else {
		noState = fmt.Errorf("the note doesn't start with %q", num)
	}
	key := AskAnswered + "\x00" + strconv.FormatInt(clk.Now().UnixNano(), 10)
	queued, err = b.Send(c, clk, Note{Worker: w.Session, Kind: AskAnswered, Text: text, Key: key})
	return queued, noState, err
}
