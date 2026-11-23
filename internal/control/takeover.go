package control

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// TookOver is the worker TakeOver closed, as `dev agent start --from --json` reports it under took_over.
type TookOver struct {
	Session  string `json:"session"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"` // "" for a detached HEAD
	// Base is origin's default branch (without origin, the main checkout's branch), Ahead the commits HEAD has
	// that it hasn't
	Base  string `json:"base"`
	Ahead int    `json:"ahead"`
	// Dirty are the tracked files with uncommitted changes, kept
	Dirty      []string `json:"dirty"`
	Transcript string   `json:"transcript"` // its Claude conversation's, "" when unknown
	Name       string   `json:"-"`          // the worktree's name, under .claude/worktrees
}

// TakeOver closes worker a for the coordinator in pane, keeping its worktree, branch and changes, so that a new
// session of the same name can start in the worktree of the same name of a's project, whose directory projectDir
// returns. Refused unless pane's session started a, a isn't a boss, is idle (or waiting only because it sat idle)
// and its worktree is under that directory's .claude/worktrees and used by no other session.
func TakeOver(c *tmux.Client, pane string, a Agent, projectDir func(project string) (string, error)) (TookOver, error) {
	done := TookOver{Session: a.Session}
	if a.Boss {
		return done, fmt.Errorf("%s is a boss: --from takes over workers only", a.Session)
	}
	own, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return done, err
	}
	own = strings.TrimSpace(own)
	if a.StartedBy != own {
		return done, fmt.Errorf("%s wasn't started by this session (%s), but by %q: take over only your own workers", a.Session, own, a.StartedBy)
	}
	mainRoot, wt, ok := worktree.SessionWorktree(a.Path)
	if !ok {
		return done, fmt.Errorf("%s isn't in a .claude/worktrees worktree (%s)", a.Session, a.Path)
	}
	done.Worktree = wt
	if err := takeable(c, a, wt); err != nil {
		return done, err
	}
	// The new session must come back as this one: same name, same worktree
	done.Name, _ = strings.CutPrefix(wt, mainRoot+"/.claude/worktrees/")
	dir, err := projectDir(a.Project)
	if err != nil {
		return done, err
	}
	root, _ := worktree.Root(dir)
	if session := sidebar.SessionName(a.Project, done.Name); root != mainRoot || worktree.Sanitize(done.Name) != done.Name || session != a.Session {
		return done, fmt.Errorf("%s can't start again as itself: dev agent start %s %s would open %s in %s", a.Session, a.Project, done.Name,
			session, filepath.Join(root, ".claude/worktrees", done.Name))
	}
	if done.Base, err = worktree.FetchDefault(mainRoot); err != nil {
		return done, fmt.Errorf("can't check %s's commits: %w", a.Session, err)
	}
	if done.Branch, err = worktree.Branch(wt); err != nil {
		return done, err
	}
	if done.Ahead, err = worktree.Ahead(wt, done.Base); err != nil {
		return done, err
	}
	if done.Dirty, err = worktree.Changed(wt); err != nil {
		return done, err
	}
	// [] in JSON, not null
	done.Dirty = append([]string{}, done.Dirty...)
	if done.Transcript, err = c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_transcript}"); err != nil {
		return done, err
	}
	done.Transcript = strings.TrimSpace(done.Transcript)
	// Again: the worker may have taken a prompt since
	if err := takeable(c, a, wt); err != nil {
		return done, err
	}
	if _, err := kill(c, a.Session, own, sidebar.Ended{Worktree: wt, Note: "taken over by a fresh worker"}); err != nil {
		return done, fmt.Errorf("killing %s: %w", a.Session, err)
	}
	return done, nil
}
