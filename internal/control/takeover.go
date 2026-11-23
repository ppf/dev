package control

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// ErrKilled is TakeOver's error from a close that failed after the worker's session was gone.
var ErrKilled = errors.New("its session is gone")

// TookOver is the worker TakeOver closed, as `dev agent start --from --json` reports it under took_over.
type TookOver struct {
	Session  string `json:"session"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"` // "" for a detached HEAD
	// Base is origin's default branch (without origin, the main checkout's branch), Ahead the commits HEAD has
	// that it hasn't
	Base  string `json:"base"`
	Ahead int    `json:"ahead"`
	// Dirty are the files with uncommitted changes, tracked then untracked, kept
	Dirty      []string `json:"dirty"`
	Transcript string   `json:"transcript"` // its Claude conversation's, "" when unknown
	Name       string   `json:"-"`          // the worktree's name, under .claude/worktrees
}

// TakeOver closes worker a for the coordinator in pane, keeping its worktree, branch and changes, so that a new
// session of the same name can start in the worktree of the same name of a's project, whose directory projectDir
// returns. Refused unless pane's session started a, a isn't a boss, has no workers of its own, is idle (or waiting
// only because it sat idle) and its worktree is under that directory's repository's .claude/worktrees and used by no
// other session.
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
	// Its workers' @dev_started_by names its session id, which the new session doesn't get
	if err := noWorkers(c, a); err != nil {
		return done, err
	}
	// The new session must come back as this one: same name, same worktree
	name, under := strings.CutPrefix(wt, mainRoot+"/.claude/worktrees/")
	if !under {
		return done, fmt.Errorf("%s's worktree %s isn't under %s/.claude/worktrees", a.Session, wt, mainRoot)
	}
	done.Name = name
	dir, err := projectDir(a.Project)
	if err != nil {
		return done, err
	}
	root, ok := worktree.Root(dir)
	if !ok {
		return done, fmt.Errorf("%s's project directory %s isn't in a git repository", a.Session, dir)
	}
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
	changed, err := worktree.Changed(wt)
	if err != nil {
		return done, err
	}
	untracked, err := worktree.Untracked(wt)
	if err != nil {
		return done, err
	}
	// [] in JSON, not null
	done.Dirty = append(append([]string{}, changed...), untracked...)
	if done.Transcript, err = c.Run("display-message", "-p", "-t", a.Pane, "#{@agent_transcript}"); err != nil {
		return done, err
	}
	done.Transcript = strings.TrimSpace(done.Transcript)
	// Again: the worker may have taken a prompt, or started a worker, since
	if err := takeable(c, a, wt); err != nil {
		return done, err
	}
	if err := noWorkers(c, a); err != nil {
		return done, err
	}
	if _, err := kill(c, a.Session, own, sidebar.Ended{Worktree: wt, Note: "taken over by a fresh worker"}); err != nil {
		if _, gone := c.Run("has-session", "-t", tmux.SessionTarget(a.Session)); gone != nil {
			return done, fmt.Errorf("%w: %w", ErrKilled, err)
		}
		return done, fmt.Errorf("killing %s: %w", a.Session, err)
	}
	return done, nil
}
