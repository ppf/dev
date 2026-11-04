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
	"syscall"
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
	// Question is what the turn's last dialog asks (agent.Question.Ask, cut to one line); it outlives the
	// dialog until the turn ends, so it counts only while State waits on a decision (agent.Deciding).
	Question string `json:"question,omitempty"`
	// RateLimited marks a turn a rate limit stopped; ResetAt is its reset, unix seconds, 0 when unread.
	RateLimited bool  `json:"rateLimited,omitempty"`
	ResetAt     int64 `json:"resetAt,omitempty"`
}

// staleHooks is how long a hook file goes unwritten before a session's start removes it: its session
// ended without a SessionEnd (a crash).
const staleHooks = 7 * 24 * time.Hour

// InApp reports a hook running in a Claude desktop app session.
func InApp() bool { return os.Getenv("CLAUDE_CODE_ENTRYPOINT") == "claude-desktop" }

// HookDir is where the hook files are kept: dev's state directory's desktop/ (config.StateDir).
func HookDir() (string, error) {
	dir, err := config.StateDir()
	return filepath.Join(dir, "desktop"), err
}

// RecordState updates the hook file of the session a `dev state <state>` hook (input: its stdin) runs for,
// as agent.Record does a pane's options: a subagent's event only moves Active, a Stop waiting on background
// tasks stays working, a rate limit's StopFailure records its reset, and the session's end removes the file.
// A session's start also removes the files of sessions gone for staleHooks.
func RecordState(dir, state string, input []byte, now time.Time) error {
	resolved, ok, err := agent.Resolve(state, input)
	if err != nil {
		return err
	}
	e := agent.ParseHook(input)
	var pruneErr error
	if e.Event == "SessionStart" {
		pruneErr = prune(dir, now)
	}
	// The hook's own session id, not $CLAUDE_CODE_SESSION_ID: a claude -p run from a worker's shell inherits that
	id := agent.HookSession(input).ID
	if id == "" {
		return pruneErr
	}
	return errors.Join(pruneErr, update(filepath.Join(dir, id+".json"), func(h *Hook) bool {
		h.Active = now.Unix()
		if !ok {
			return false
		}
		if resolved == agent.Clear {
			return true
		}
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
		return false
	}))
}

// RecordQuestion records what the dialog a `dev question` hook reports asks in its session's hook file. A
// PermissionRequest comes also for a tool auto mode then approves, so, as on a tmux pane, the Notification
// of a dialog shown (dev state waiting) makes it a decision; AskUserQuestion always shows one, so its
// PreToolUse does too. A subagent's dialog only moves Active.
func RecordQuestion(dir string, input []byte, now time.Time) error {
	q, ok := agent.HookQuestion(input)
	id := agent.HookSession(input).ID
	if !ok || id == "" {
		return nil
	}
	_, sub, err := agent.Resolve(agent.Working, input)
	if err != nil {
		return err
	}
	return update(filepath.Join(dir, id+".json"), func(h *Hook) bool {
		h.Active = now.Unix()
		if !sub {
			return false
		}
		h.Question = cut(strings.Join(strings.Fields(q.Ask()), " "))
		if q.Tool == agent.AskUserQuestion && agent.ParseHook(input).Event == "PreToolUse" {
			h.State, h.Since, h.WaitingFor = agent.Waiting, now.Unix(), agent.AskUserQuestion
		}
		return false
	})
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

// update runs f on path's Hook under the session's lock (hooks of one session run side by side) and writes
// it back whole (the board reads it any time), or removes it and its lock when f says so. A file that
// doesn't parse is reported and replaced, so it fails one hook, not each one after.
func update(path string, f func(*Hook) (remove bool)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	h, readErr := ReadHook(path)
	if f(&h) {
		// The lock goes too: a hook still waiting on it writes a fresh file under a lock of its own
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return errors.Join(readErr, err, os.Remove(path+".lock"))
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.Join(readErr, err)
	}
	return errors.Join(readErr, os.Rename(tmp, path))
}

// prune removes dir's hook files (and their locks) not written for staleHooks.
func prune(dir string, now time.Time) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || now.Sub(info.ModTime()) < staleHooks {
			continue
		}
		for _, p := range []string{path, path + ".lock"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
