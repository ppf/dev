package control

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
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
// used by no other session) has no tracked changes and its HEAD is in origin's default branch, or is the head of a
// PR/MR that is open (into any branch), or merged into that branch.
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
	// Slow too (gh/glab): before the checks, which then only need its HEAD unchanged
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
		if dir, err = config.NewDir(filepath.Join(dir, "results"), a.Session); err != nil {
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
	record := sidebar.Ended{Worktree: wt}
	if done.Results != "" {
		record.Note = "untracked files copied to " + done.Results
	}
	dir, err := kill(c, a.Session, own, record)
	if err != nil {
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
	if err := sidebar.MarkRemoved(dir); err != nil {
		return done, fmt.Errorf("closed %s, but its archive record %s: %w", a.Session, dir, err)
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

// delivered returns wt's HEAD once it is in base, or is the head of an open or merged PR/MR (prDelivered).
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
		if err := prDelivered(wt, base, head); err != nil {
			return "", fmt.Errorf("%s has commits that aren't in %s, and no PR/MR open or merged at its HEAD: %w", wt, base, err)
		}
	}
	return head, nil
}

// change is a PR or MR of a commit.
type change struct {
	Number     int
	State      string // open, merged, closed or locked
	Head, Base string // head commit, base branch
}

// prDelivered checks that a PR (gh) or MR (glab) with head as its head commit is open, or merged into base's
// branch, as a squash or rebase merge leaves its commits out of base. Looked up by commit in origin's
// repository, so a PR from a fork to its upstream counts.
func prDelivered(wt, base, head string) error {
	kind, changes, err := changesOf(wt, head)
	if err != nil {
		return err
	}
	branch := strings.TrimPrefix(base, "origin/")
	var why []string
	for _, c := range changes {
		switch {
		case c.Head != head:
			// HEAD behind the PR's head; an unpushed commit after it fails the lookup instead
			why = append(why, fmt.Sprintf("%s #%d is at %s, not at its HEAD %s", kind, c.Number, c.Head, head))
		case c.State == "open":
			return nil
		case c.State == "merged" && c.Base == branch:
			return nil
		case c.State == "merged":
			why = append(why, fmt.Sprintf("%s #%d was merged into %s", kind, c.Number, c.Base))
		default:
			why = append(why, fmt.Sprintf("%s #%d is %s", kind, c.Number, c.State))
		}
	}
	if len(why) == 0 {
		return fmt.Errorf("no %s has it", kind)
	}
	return errors.New(strings.Join(why, "; "))
}

// changesOf lists the PRs (gh, for github.com or a host gh is signed in to) or else MRs (glab) of origin's
// repository that contain commit head, returning "PR" or "MR" with them.
func changesOf(wt, head string) (string, []change, error) {
	remote, err := worktree.OriginURL(wt)
	if err != nil {
		return "", nil, err
	}
	host, repo, err := remoteRepo(remote)
	if err != nil {
		return "", nil, err
	}
	var changes []change
	if strings.EqualFold(host, "github.com") || ghHost(host) {
		out, err := sidebar.RunGH(wt, "api", "--hostname", host, "repos/"+repo+"/commits/"+head+"/pulls")
		if err != nil {
			return "", nil, err
		}
		var prs []struct {
			Number   int
			State    string  // open or closed
			MergedAt *string `json:"merged_at"`
			Head     struct {
				SHA  string
				Repo *ghRepo // nil once the head fork is deleted
			}
			Base struct {
				Ref  string
				Repo ghRepo
			}
		}
		if err := json.Unmarshal(out, &prs); err != nil {
			return "", nil, fmt.Errorf("gh api: %w", err)
		}
		for _, pr := range prs {
			// The commit's PRs in other forks of the network, e.g. one merged into another fork's main
			if !strings.EqualFold(pr.Base.Repo.FullName, repo) && (pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, repo)) {
				continue
			}
			if pr.MergedAt != nil {
				pr.State = "merged"
			}
			changes = append(changes, change{pr.Number, pr.State, pr.Head.SHA, pr.Base.Ref})
		}
		return "PR", changes, nil
	}
	out, err := sidebar.RunGLab(wt, "api", "--hostname", host, "projects/"+url.PathEscape(repo)+"/repository/commits/"+head+"/merge_requests")
	if err != nil {
		return "", nil, err
	}
	var mrs []struct {
		IID          int
		State        string // opened, merged, closed or locked
		SHA          string
		TargetBranch string `json:"target_branch"`
	}
	if err := json.Unmarshal(out, &mrs); err != nil {
		return "", nil, fmt.Errorf("glab api: %w", err)
	}
	for _, mr := range mrs {
		if mr.State == "opened" {
			mr.State = "open"
		}
		changes = append(changes, change{mr.IID, mr.State, mr.SHA, mr.TargetBranch})
	}
	return "MR", changes, nil
}

type ghRepo struct {
	FullName string `json:"full_name"`
}

// ghHost reports whether gh is signed in to host, e.g. a GitHub Enterprise one.
func ghHost(host string) bool {
	_, err := sidebar.RunGH("", "auth", "status", "--hostname", host)
	return err == nil
}

// remoteRepo splits a remote URL (https://, ssh:// or scp-like git@host:path) into its host and repository path.
func remoteRepo(remote string) (host, repo string, err error) {
	u, err := url.Parse(remote)
	switch {
	case err == nil && u.Host != "":
		host, repo = u.Hostname(), u.Path
		if u.Scheme == "http" || u.Scheme == "https" {
			// An API port; an ssh:// one isn't
			host = u.Host
		}
	case err == nil && u.Opaque == "":
		// A path or file:// URL
	default:
		if user, path, ok := strings.Cut(remote, ":"); ok && !strings.Contains(user, "/") {
			host, repo = user[strings.LastIndex(user, "@")+1:], path
		}
	}
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	if host == "" || !strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("origin %s isn't a GitHub or GitLab repository", remote)
	}
	return host, repo, nil
}

// kill switches the clients attached to session over to to, so they aren't detached with it, then closes it
// for good with record (sidebar.CloseUnasked), returning the record's directory.
func kill(c *tmux.Client, session, to string, record sidebar.Ended) (string, error) {
	clients, err := c.Run("list-clients", "-t", tmux.SessionTarget(session), "-F", "#{client_name}")
	if err != nil {
		return "", err
	}
	for _, client := range strings.Fields(clients) {
		if _, err := c.Run("switch-client", "-c", client, "-t", tmux.SessionTarget(to)); err != nil {
			return "", err
		}
	}
	return sidebar.CloseUnasked(c, session, record)
}
