package desktop

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/inbox"
)

// Hook is what a desktop app session's own Claude hooks (dev state, dev question) recorded of it, kept in
// <dir>/<session id>.json: the stand-in for a tmux pane's agent options.
type Hook struct {
	State string `json:"state"`
	// Since is when State began, unix seconds; each decision starts its own stretch.
	Since int64 `json:"since"`
	// Active is the last hook, a subagent's too, unix seconds.
	Active     int64  `json:"active"`
	WaitingFor string `json:"waitingFor,omitempty"`
	// Question is what the open dialog asks (agent.Question.Ask).
	Question string `json:"question,omitempty"`
	// RateLimited marks a turn a rate limit stopped; ResetAt is its reset, unix seconds, 0 when unread.
	RateLimited bool  `json:"rateLimited,omitempty"`
	ResetAt     int64 `json:"resetAt,omitempty"`
}

// InApp reports a hook running in a Claude desktop app session.
func InApp() bool { return os.Getenv("CLAUDE_CODE_ENTRYPOINT") == "claude-desktop" }

// HookDir is where the hook files are kept: $XDG_STATE_HOME/dev/desktop.
func HookDir() (string, error) {
	dir, err := config.StateDir()
	return filepath.Join(dir, "desktop"), err
}

// RecordState updates the hook file of the session a `dev state <state>` hook (input: its stdin) runs for,
// as agent.Record does a pane's options: a subagent's event only moves Active, a Stop waiting on background
// tasks stays working, a rate limit's StopFailure records its reset, and the session's end removes the file.
func RecordState(dir, state string, input []byte, now time.Time) error {
	resolved, ok, err := agent.Resolve(state, input)
	if err != nil {
		return err
	}
	// The hook's own session id, not $CLAUDE_CODE_SESSION_ID: a claude -p run from a worker's shell inherits that
	id := agent.HookSession(input).ID
	if id == "" {
		return nil
	}
	path := filepath.Join(dir, id+".json")
	if ok && resolved == agent.Clear {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	h, err := ReadHook(path)
	if err != nil {
		return err
	}
	h.Active = now.Unix()
	if !ok {
		return writeHook(path, h)
	}
	e := agent.ParseHook(input)
	if agent.Backgrounded(input) {
		resolved = agent.Working
	}
	if resolved != h.State || resolved == agent.Waiting && e.NotificationType != agent.IdlePrompt {
		h.Since = now.Unix()
	}
	h.State, h.WaitingFor = resolved, ""
	if resolved == agent.Waiting {
		h.WaitingFor = cmp.Or(e.NotificationType, e.Event)
	}
	if slices.Contains(agent.TurnBounds, e.Event) {
		h.Question = ""
	}
	switch e.Event {
	case "StopFailure":
		h.RateLimited, h.ResetAt = rateLimit(input, now)
	case "Stop", "UserPromptSubmit", "SessionStart":
		h.RateLimited, h.ResetAt = false, 0
	}
	return writeHook(path, h)
}

// RecordQuestion records the dialog a `dev question` hook (PermissionRequest, PreToolUse of AskUserQuestion)
// reports in its session's hook file, as a decision waiting since now.
func RecordQuestion(dir string, input []byte, now time.Time) error {
	q, ok := agent.HookQuestion(input)
	id := agent.HookSession(input).ID
	if !ok || id == "" {
		return nil
	}
	path := filepath.Join(dir, id+".json")
	h, err := ReadHook(path)
	if err != nil {
		return err
	}
	h.State, h.Since, h.Active = agent.Waiting, now.Unix(), now.Unix()
	h.WaitingFor = agent.ParseHook(input).Event
	if q.Tool == agent.AskUserQuestion {
		h.WaitingFor = agent.AskUserQuestion
	}
	h.Question = cut(strings.Join(strings.Fields(q.Ask()), " "))
	return writeHook(path, h)
}

// rateLimit reads a StopFailure: whether a rate limit stopped the turn, and its reset when the message says.
func rateLimit(input []byte, now time.Time) (bool, int64) {
	var e struct {
		Error   string `json:"error"`
		Last    string `json:"last_assistant_message"`
		Details string `json:"error_details"`
	}
	if json.Unmarshal(input, &e) != nil || e.Error != "rate_limit" {
		return false, 0
	}
	reset, ok := inbox.ResetTime(e.Last+" "+e.Details, now)
	if !ok {
		return true, 0
	}
	return true, reset.Unix()
}

// ReadHook reads a hook file; a missing one is the zero Hook.
func ReadHook(path string) (Hook, error) {
	var h Hook
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return Hook{}, fmt.Errorf("%s: %w", path, err)
	}
	return h, nil
}

// writeHook replaces path with h whole: hooks of one session run side by side, and the board reads it any time.
func writeHook(path string, h Hook) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hook-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
