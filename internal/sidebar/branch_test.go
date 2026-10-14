package sidebar

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelFakeGit answers rev-parse --absolute-git-dir with gitDir for repo paths and the branch from branch,
// logging every call.
type modelFakeGit struct {
	gitDir string
	branch string
	calls  []string
}

func (g *modelFakeGit) run(dir string, args ...string) (string, error) {
	g.calls = append(g.calls, dir+": "+strings.Join(args, " "))
	if dir != "/repo" {
		return "", errors.New("not a git repository")
	}
	switch strings.Join(args, " ") {
	case "rev-parse --absolute-git-dir":
		return g.gitDir, nil
	case "branch --show-current":
		return g.branch, nil
	case "rev-parse --short HEAD":
		return "abc1234", nil
	}
	return "", errors.New("unexpected")
}

func TestModelBranchCache(t *testing.T) {
	gitDir := t.TempDir()
	head := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &modelFakeGit{gitDir: gitDir, branch: "main"}
	now := time.Unix(1000, 0)
	b := &branchCache{git: g.run, now: func() time.Time { return now }, entries: map[string]branchEntry{}}

	if got := b.get("/repo"); got != "main" || len(g.calls) != 2 {
		t.Fatalf("first get = %q, calls %q; want main after git-dir + branch", got, g.calls)
	}
	if got := b.get("/repo"); got != "main" || len(g.calls) != 2 {
		t.Fatalf("unchanged HEAD = %q, calls %q; want no git", got, g.calls)
	}

	// Detached HEAD: an empty --show-current falls back to the short hash
	g.branch = ""
	if err := os.Chtimes(head, time.Time{}, time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	if got := b.get("/repo"); got != "abc1234" || len(g.calls) != 4 {
		t.Fatalf("rewritten HEAD = %q, calls %q; want abc1234 without a new git-dir lookup", got, g.calls)
	}

	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	g.calls = nil
	if got := b.get("/repo"); !strings.HasPrefix(g.calls[0], "/repo: rev-parse --absolute-git-dir") {
		t.Fatalf("missing HEAD = %q, calls %q; want the git dir resolved again", got, g.calls)
	}

	g.calls = nil
	if got := b.get("/tmp"); got != "" || len(g.calls) != 1 {
		t.Fatalf("non-repo = %q, calls %q", got, g.calls)
	}
	now = now.Add(branchRecheck - time.Millisecond)
	b.get("/tmp")
	if len(g.calls) != 1 {
		t.Fatalf("non-repo within %v: calls %q; want none", branchRecheck, g.calls)
	}
	now = now.Add(time.Millisecond)
	b.get("/tmp")
	if len(g.calls) != 2 {
		t.Fatalf("non-repo after %v: calls %q; want a recheck", branchRecheck, g.calls)
	}

	if got := b.get(""); got != "" || len(g.calls) != 2 {
		t.Errorf("empty path = %q, calls %q; want no git", got, g.calls)
	}
}

func TestModelBranchCacheGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "topic").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	b := newBranchCache()
	if got := b.get(repo); got != "topic" {
		t.Errorf("get(repo) = %q, want topic", got)
	}
	if got := b.get(t.TempDir()); got != "" {
		t.Errorf("get(non-repo) = %q, want empty", got)
	}
}
