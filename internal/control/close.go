package control

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// Closed is what Close did, as `dev agent close --json` reports it.
type Closed struct {
	Session  string `json:"session"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch,omitempty"` // deleted branch; "" for a detached HEAD or the default branch
	// Results is where the worktree's untracked files were copied; omitted when it had none.
	Results string   `json:"results,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// Close closes worker a for the coordinator in pane, archiving its untracked files. It refuses unless pane's
// session started a, a is idle (or waiting only because it sat idle), and a's worktree (under .claude/worktrees,
// used by no other session) has no tracked changes and its HEAD is in origin's default branch, or gh reports its PR merged at that HEAD.
func Close(c *tmux.Client, pane string, a Agent) (Closed, error) {
	done := Closed{Session: a.Session}
	own, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return done, err
	}
	own = strings.TrimSpace(own)
	if a.StartedBy != own {
		return done, fmt.Errorf("%s wasn't started by this session (%s), but by %q: close only your own workers", a.Session, own, a.StartedBy)
	}
	mainRoot, wt, ok := worktree.SessionWorktree(a.Path)
	if !ok {
		return done, fmt.Errorf("%s isn't in a .claude/worktrees worktree (%s)", a.Session, a.Path)
	}
	done.Worktree = wt
	// Slow step first, so the checks after it run as close to the kill as possible
	base, err := worktree.FetchDefault(mainRoot)
	if err != nil {
		return done, fmt.Errorf("can't check %s's commits: %w", a.Session, err)
	}
	// Slow too (gh): before the checks, which then only need its HEAD unchanged
	head, err := delivered(wt, base)
	if err != nil {
		return done, err
	}
	if err := closable(c, a, wt, head); err != nil {
		return done, err
	}
	branch, err := worktree.Branch(wt)
	if err != nil {
		return done, err
	}

	files, err := worktree.Untracked(wt)
	if err != nil {
		return done, err
	}
	if len(files) > 0 {
		dir, err := config.StateDir()
		if err != nil {
			return done, err
		}
		if dir, err = newDir(filepath.Join(dir, "results"), a.Session); err != nil {
			return done, err
		}
		if err := worktree.Archive(wt, dir, files); err != nil {
			return done, fmt.Errorf("copying untracked files to %s: %w", dir, err)
		}
		done.Results, done.Files = dir, files
	}
	// Again: the worker may have taken a prompt since
	if err := closable(c, a, wt, head); err != nil {
		return done, err
	}
	if err := kill(c, a.Session, own); err != nil {
		return done, fmt.Errorf("killing %s: %w", a.Session, err)
	}
	// Only now: panes inside the worktree would block its removal
	if err := worktree.Remove(mainRoot, wt); err != nil {
		return done, fmt.Errorf("killed %s, but removing %s failed, so it and branch %q remain: %w", a.Session, wt, branch, err)
	}
	// Never delete the default branch, even if the worker switched to it
	if branch != "" && "origin/"+branch != base {
		if err := worktree.DeleteBranch(mainRoot, branch); err != nil {
			return done, fmt.Errorf("removed %s, but deleting branch %s failed: %w", wt, branch, err)
		}
		done.Branch = branch
	}
	return done, nil
}

// closable refuses a working, deciding or hookless worker, or a shared or dirty worktree, or one whose HEAD
// moved from head, the one delivered checked.
func closable(c *tmux.Client, a Agent, wt, head string) error {
	st, err := ReadChecked(c, a.Pane)
	if err != nil {
		return err
	}
	switch state := st.Trusted(); {
	case state == "":
		return fmt.Errorf("%s has no hook state (Codex or Grok without its hooks, or its agent exited), so it may still be working", a.Session)
	case state == agent.Working:
		return fmt.Errorf("%s is working: wait for it first", a.Session)
	case agent.Deciding(state, st.WaitingFor):
		return fmt.Errorf("%s is waiting for a decision (%s): answer it first", a.Session, cmp.Or(st.WaitingFor, "unknown"))
	}
	if shared, err := sidebar.WorktreeShared(c, a.Session, wt); err != nil || shared {
		return cmp.Or(err, fmt.Errorf("another session has a pane in %s", wt))
	}
	if dirty, err := worktree.Dirty(wt); err != nil || dirty {
		return cmp.Or(err, fmt.Errorf("%s has uncommitted changes to tracked files", wt))
	}
	if now, err := worktree.Head(wt); err != nil || now != head {
		return cmp.Or(err, fmt.Errorf("%s got new commits while closing", wt))
	}
	return nil
}

// delivered returns wt's HEAD once it is in base, or gh reports its PR merged at it.
func delivered(wt, base string) (string, error) {
	// Read first: a HEAD moving on after it fails closable
	head, err := worktree.Head(wt)
	if err != nil {
		return "", err
	}
	merged, err := worktree.Merged(wt, base)
	if err != nil {
		return "", err
	}
	if !merged {
		if err := prMerged(wt, base, head); err != nil {
			return "", fmt.Errorf("%s has commits that aren't in %s, and no PR merged at its HEAD: %w", wt, base, err)
		}
	}
	return head, nil
}

// prMerged checks that gh reports the PR of wt's branch merged into base's branch at head, as a squash or
// rebase merge leaves its commits out of base.
func prMerged(wt, base, head string) error {
	out, err := sidebar.RunGH(wt, "pr", "view", "--json", "state,headRefOid,baseRefName")
	if err != nil {
		return err
	}
	var pr struct{ State, HeadRefOid, BaseRefName string }
	if err := json.Unmarshal(out, &pr); err != nil {
		return fmt.Errorf("gh pr view: %w", err)
	}
	switch {
	case pr.State != "MERGED":
		return fmt.Errorf("its PR is %s", strings.ToLower(pr.State))
	case pr.BaseRefName != strings.TrimPrefix(base, "origin/"):
		return fmt.Errorf("its PR was merged into %s", pr.BaseRefName)
	case pr.HeadRefOid != head:
		// A commit after the merge, or a PR of another branch
		return fmt.Errorf("its PR was merged at %s, not at its HEAD %s", pr.HeadRefOid, head)
	}
	return nil
}

// newDir creates parent/name, or name.2, .3, … if taken, so a reused name never mixes in earlier results.
func newDir(parent, name string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		dir := filepath.Join(parent, name)
		if n > 1 {
			dir += "." + strconv.Itoa(n)
		}
		err := os.Mkdir(dir, 0o755)
		if !errors.Is(err, fs.ErrExist) {
			return dir, err
		}
	}
}

// kill switches the clients attached to session over to to, so they aren't detached with it, then kills it.
func kill(c *tmux.Client, session, to string) error {
	clients, err := c.Run("list-clients", "-t", tmux.SessionTarget(session), "-F", "#{client_name}")
	if err != nil {
		return err
	}
	for _, client := range strings.Fields(clients) {
		if _, err := c.Run("switch-client", "-c", client, "-t", tmux.SessionTarget(to)); err != nil {
			return err
		}
	}
	if _, err := c.Run("kill-session", "-t", tmux.SessionTarget(session)); err != nil {
		return err
	}
	if _, err := c.Run("has-session", "-t", tmux.SessionTarget(session)); err == nil {
		return fmt.Errorf("%s is still there after kill-session", session)
	}
	return nil
}
