package sidebar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/tmux"
)

// Ended is the archive record of a session closed for good: $XDG_STATE_HOME/dev/archive/<session>[.N]/session.json.
type Ended struct {
	Session string `json:"session"`
	Project string `json:"project,omitempty"`
	Path    string `json:"path,omitempty"`
	// Conversation is its agent's last Claude conversation, to resume by hand (claude --resume)
	Conversation    string    `json:"conversation,omitempty"`
	Transcript      string    `json:"transcript,omitempty"`
	Worktree        string    `json:"worktree,omitempty"`
	WorktreeRemoved bool      `json:"worktree_removed,omitempty"`
	Note            string    `json:"note,omitempty"`
	At              time.Time `json:"ended_at"`
}

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
	}](c, "list-panes", "-s", "-t", tmux.SessionTarget(session))
	if err != nil {
		return "", err
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
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	// A session name may hold a /, which would nest the directory
	if dir, err = config.NewDir(filepath.Join(dir, "archive"), strings.ReplaceAll(session, "/", "_")); err != nil {
		return "", err
	}
	return dir, writeRecord(dir, e)
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
