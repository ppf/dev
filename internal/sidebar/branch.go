package sidebar

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// branchRecheck is how long a path that isn't a repo stays cached, so ticks don't fork git 8×/s.
const branchRecheck = time.Second

// branchCache remembers each path's branch until the HEAD it was read from changes, so a tick costs
// no git process while nothing moved (bash ran git per session every second).
// Only the sequential tick chain uses it, so it needs no lock.
type branchCache struct {
	// git runs git -C dir args and returns its trimmed stdout; tests replace it.
	git     func(dir string, args ...string) (string, error)
	now     func() time.Time
	entries map[string]branchEntry
}

// branchEntry is one path's cached branch.
type branchEntry struct {
	head    string    // <git-dir>/HEAD; "" when the path isn't a repo
	mtime   time.Time // head's mtime when branch was read
	branch  string
	checked time.Time // when a non-repo path was last tried
}

// newBranchCache returns an empty cache that runs the real git.
func newBranchCache() *branchCache {
	return &branchCache{git: runGit, now: time.Now, entries: map[string]branchEntry{}}
}

// runGit runs git -C dir args; stderr is dropped like bash's 2>/dev/null, it would land on the TUI.
func runGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// get returns path's branch (branch --show-current, else rev-parse --short HEAD), "" outside a repo.
func (b *branchCache) get(path string) string {
	if path == "" {
		return ""
	}
	e, ok := b.entries[path]
	switch {
	case ok && e.head == "" && b.now().Sub(e.checked) < branchRecheck:
		return e.branch
	case ok && e.head != "":
		info, err := os.Stat(e.head)
		if err == nil && info.ModTime().Equal(e.mtime) {
			return e.branch
		}
		if err == nil {
			e.mtime, e.branch = info.ModTime(), b.read(path)
			b.entries[path] = e
			return e.branch
		}
		// HEAD is gone (worktree removed, repo deleted): resolve the git dir again
	}
	e = b.resolve(path)
	b.entries[path] = e
	return e.branch
}

// resolve finds path's git dir and reads its branch; HEAD is stat'ed first, so a checkout racing
// the read shows up as a changed mtime on the next tick.
func (b *branchCache) resolve(path string) branchEntry {
	dir, err := b.git(path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return branchEntry{checked: b.now()}
	}
	head := filepath.Join(dir, "HEAD")
	info, err := os.Stat(head)
	if err != nil {
		return branchEntry{branch: b.read(path), checked: b.now()}
	}
	return branchEntry{head: head, mtime: info.ModTime(), branch: b.read(path)}
}

// read is load_session_metadata's branch lookup.
func (b *branchCache) read(path string) string {
	if branch, err := b.git(path, "branch", "--show-current"); err == nil && branch != "" {
		return branch
	}
	short, err := b.git(path, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return short
}
