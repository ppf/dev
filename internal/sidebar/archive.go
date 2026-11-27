package sidebar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/spent"
	"github.com/ppf/dev/internal/tmux"
)

// Ended is the archive record of a session closed for good: $XDG_STATE_HOME/dev/archive/<session>[.N]/session.json.
type Ended struct {
	Session string `json:"session"`
	Project string `json:"project,omitempty"`
	Path    string `json:"path,omitempty"`
	// Conversation is its agent's last Claude conversation, to resume by hand (claude --resume)
	Conversation    string `json:"conversation,omitempty"`
	Transcript      string `json:"transcript,omitempty"`
	Worktree        string `json:"worktree,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	Note            string `json:"note,omitempty"`
	// StartedBy is the boss or manager that started it, by name; StartedAt when its tmux session was made
	StartedBy string    `json:"started_by,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	At        time.Time `json:"ended_at"`
}

// BossEnding, when set, writes a boss's last-session file as its session closes, before the kill: a boss's archive
// record notes its failure. Set by package cmd, as it needs the boss's list.
var BossEnding func(c *tmux.Client, session string, created int64) error

const recordFile = "session.json"

// archive writes e for session, completed from its panes, into a new archive directory, which it returns.
func archive(c *tmux.Client, session string, e Ended) (string, error) {
	panes, err := tmux.Query[struct {
		Project         string `tmux:"@project"`
		Path            string `tmux:"session_path"`
		ID              string `tmux:"@agent_session"`
		Transcript      string `tmux:"@agent_transcript"`
		EndedID         string `tmux:"@agent_ended_session"`
		EndedTranscript string `tmux:"@agent_ended_transcript"`
		Cost            string `tmux:"@agent_cost"`
		Socket          string `tmux:"socket_path"`
		SessionID       string `tmux:"session_id"`
		Created         string `tmux:"session_created"`
		StartedBy       string `tmux:"@dev_started_by"`
		Herd            string `tmux:"@dev_herd"`
	}](c, "list-panes", "-s", "-t", tmux.SessionTarget(session))
	if err != nil {
		return "", err
	}
	live := spent.Ledger{}
	for _, p := range panes {
		if cost, ok := agent.ParseCost(p.Cost); ok && p.ID != "" {
			live[p.ID] = cost
		}
	}
	e.Session, e.At = session, time.Now()
	var endedID, endedTranscript string
	for _, p := range panes {
		e.Project, e.Path = p.Project, p.Path
		if e.Conversation == "" && p.ID != "" {
			e.Conversation, e.Transcript = p.ID, p.Transcript
		}
		if endedID == "" && p.EndedID != "" {
			endedID, endedTranscript = p.EndedID, p.EndedTranscript
		}
	}
	// No live conversation: the one its agent's exit cleared
	if e.Conversation == "" {
		e.Conversation, e.Transcript = endedID, endedTranscript
	}
	if len(panes) > 0 {
		p := panes[0]
		var created int64
		if p.Created != "" {
			if created, err = strconv.ParseInt(p.Created, 10, 64); err != nil {
				return "", fmt.Errorf("%s's creation time %q: %w", session, p.Created, err)
			}
			e.StartedAt = time.Unix(created, 0)
		}
		if e.StartedBy, err = starterName(c, p.StartedBy); err != nil {
			return "", err
		}
		if BossEnding != nil && p.Herd != "" {
			if err := BossEnding(c, session, created); err != nil {
				e.Note = strings.TrimPrefix(e.Note+"; ", "; ") + "its last-session file isn't written: " + err.Error()
			}
		}
	}
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	// A session name may hold a /, which would nest the directory
	if dir, err = config.NewDir(filepath.Join(dir, "archive"), strings.ReplaceAll(session, "/", "_")); err != nil {
		return "", err
	}
	if err := writeRecord(dir, e); err != nil || len(panes) == 0 {
		return dir, err
	}
	// Last, so a close that fails before keeps its ledger; one this fails still closes, its record saying so
	p := panes[0]
	if err := spent.Closing(c, p.Socket, p.SessionID, p.Created, p.StartedBy, live); err != nil {
		e.Note = strings.TrimPrefix(e.Note+"; ", "; ") + "its cost isn't kept: " + err.Error()
		return dir, writeRecord(dir, e)
	}
	return dir, nil
}

// starterName is the name of the session the @dev_started_by value v names (a session id, or a boss's name); v itself
// when no session has that id, "" for none.
func starterName(c *tmux.Client, v string) (string, error) {
	if !IsSessionID(v) {
		return v, nil
	}
	name, err := c.Run("display-message", "-p", "-t", v, "#{session_name}")
	if err != nil {
		if _, gone := c.Run("has-session", "-t", v); gone != nil {
			return v, nil
		}
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// Records are the archive records of the sessions closed for good, in no order.
func Records() ([]Ended, error) {
	dir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "archive", "*", recordFile))
	if err != nil {
		return nil, err
	}
	records := make([]Ended, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var e Ended
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		records = append(records, e)
	}
	return records, nil
}

// MarkRemoved records in dir's record that the session's worktree was removed.
func MarkRemoved(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, recordFile))
	if err != nil {
		return err
	}
	var e Ended
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	e.WorktreeRemoved = true
	return writeRecord(dir, e)
}

func writeRecord(dir string, e Ended) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordFile), append(data, '\n'), 0o644)
}

// Exited closes target's session (target: a pane or session id) for good once its agent exited cleanly, as
// the sidebar's × does: a worktree session asks about its worktree on a client attached to it. With no
// client to ask, it closes and keeps the worktree, and the record says so.
func Exited(c *tmux.Client, target string) error {
	session, err := c.Run("display-message", "-p", "-t", target, "#{session_name}")
	if err != nil {
		return err
	}
	session = strings.TrimSuffix(session, "\n")
	clients, err := c.Run("list-clients", "-t", tmux.SessionTarget(session), "-F", "#{client_name}")
	if err != nil {
		return err
	}
	if client, _, _ := strings.Cut(clients, "\n"); client != "" {
		return Close(c, client, session, session)
	}
	var e Ended
	_, wt, ok, err := sessionWorktree(c, session)
	if err != nil {
		return err
	}
	if ok {
		e.Worktree, e.Note = wt, "worktree kept: no client attached to ask about it"
	}
	_, err = CloseUnasked(c, session, e)
	return err
}

// CloseUnasked closes session for good with e as its record, asking nothing and switching no client, and
// returns the record's directory.
func CloseUnasked(c *tmux.Client, session string, e Ended) (string, error) {
	// The sidebars' selection stays where it is, unless on session
	selected, err := c.Run("show-option", "-gqv", optSelected)
	if err != nil {
		return "", err
	}
	if selected = strings.TrimSuffix(selected, "\n"); selected == session {
		selected = ""
	}
	killed, record, err := killNow(c, "", selected, session, e)
	if err == nil && !killed {
		err = fmt.Errorf("%s is still there after kill-session", session)
	}
	return record, err
}
