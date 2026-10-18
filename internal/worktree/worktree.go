// Package worktree manages <repo>/.claude/worktrees/<name> git worktrees, the layout `claude --worktree` uses.
package worktree

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the repo top level of path; ok=false when path is not in a git repo.
func Root(path string) (root string, ok bool) {
	root, err := gitOutput(path, "rev-parse", "--show-toplevel")
	return root, err == nil
}

// List returns worktree names under root/.claude/worktrees/ from `git worktree list --porcelain`
// (nested names like feature/one included), in git's order.
func List(root string) ([]string, error) {
	out, err := gitOutput(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	prefix := "worktree " + root + "/.claude/worktrees/"
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, prefix); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// Sanitize keeps only A-Za-z0-9._/- bytes.
func Sanitize(name string) string {
	// Byte-wise like `LC_ALL=C tr -cd`: popup input can carry stray non-UTF-8 bytes that poison the branch ref
	b := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("._/-", c) >= 0 {
			b = append(b, c)
		}
	}
	return string(b)
}

// Ensure returns the path of worktree name for the repo at repoPath, creating it when it isn't
// registered: prune first; an existing branch is checked out as-is; otherwise fetch origin, set
// origin/HEAD, branch from it (falling back to HEAD). A fresh worktree gets the gitignored files
// matching .worktreeinclude; if that copy fails the worktree is removed, and the branch too if it
// was just created. Warnings and notices go to notify.
func Ensure(repoPath, name string, notify func(msg string)) (string, error) {
	root, ok := Root(repoPath)
	if !ok {
		return "", errors.New("Not a git repo: " + repoPath)
	}
	wt := root + "/.claude/worktrees/" + name

	// Clears worktrees whose directory was deleted — otherwise git refuses to re-add them
	if err := gitCmd(root, "worktree", "prune").Run(); err != nil {
		return "", fmt.Errorf("git worktree prune: %w", err)
	}
	registered, err := isRegistered(root, wt)
	if err != nil {
		return "", err
	}
	if registered {
		return wt, nil
	}

	created := false
	if gitCmd(root, "show-ref", "--quiet", "--verify", "refs/heads/"+name).Run() == nil {
		notify("ℹ  Branch " + name + " exists — checking it out as-is")
		if gitCmd(root, "worktree", "add", wt, name).Run() != nil {
			return "", fmt.Errorf("Failed to check out %s at %s", name, wt)
		}
	} else {
		base := baseRef(root, notify)
		if gitCmd(root, "worktree", "add", "-b", name, wt, base).Run() != nil {
			return "", fmt.Errorf("Failed to create worktree %s from %s", wt, base)
		}
		created = true
	}

	// Same convention as `claude --worktree`: copy gitignored files matching .worktreeinclude
	if fi, err := os.Stat(filepath.Join(root, ".worktreeinclude")); err == nil && fi.Mode().IsRegular() && !copyIncluded(root, wt) {
		// A half-set-up worktree is removed so a retry starts clean; cleanup failures surface on stderr, as in bash
		_ = gitCmd(root, "worktree", "remove", "--force", wt).Run()
		if created {
			_ = gitCmd(root, "branch", "-D", name).Run()
		}
		return "", fmt.Errorf("Copying .worktreeinclude files into %s failed — worktree removed", wt)
	}
	return wt, nil
}

// SessionWorktree reports the main checkout and worktree path when panePath is inside a
// .claude/worktrees/<name> worktree.
func SessionWorktree(panePath string) (mainRoot, wtPath string, ok bool) {
	top, ok := Root(panePath)
	if !ok || !strings.Contains(top, "/.claude/worktrees/") {
		return "", "", false
	}
	common, err := gitOutput(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", false
	}
	return filepath.Dir(common), top, true
}

// Remove force-removes the worktree at wtPath from the repo at mainRoot.
// The error text is git's output, which the sidebar shows as is.
func Remove(mainRoot, wtPath string) error {
	out, err := exec.Command("git", "-C", mainRoot, "worktree", "remove", "--force", wtPath).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimRight(string(out), "\n"); msg != "" {
		return errors.New(msg)
	}
	return err
}

// Dirty reports whether wt has changes to tracked files, staged or not.
func Dirty(wt string) (bool, error) {
	// Submodule edits count even where .gitmodules says to ignore them
	out, err := gitOutput(wt, "status", "--porcelain", "--untracked-files=no", "--ignore-submodules=none")
	return out != "", err
}

// FetchDefault fetches origin into the repo at root and returns origin's default branch (origin/HEAD), e.g. origin/main.
func FetchDefault(root string) (string, error) {
	// A credential prompt would hang the coordinator's call
	fetch := exec.Command("git", "-C", root, "fetch", "--quiet", "origin")
	fetch.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := fetch.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git fetch origin: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// fetch leaves origin/HEAD where it was when the remote's default branch changed
	if _, err := gitOutput(root, "remote", "set-head", "--auto", "origin"); err != nil {
		return "", err
	}
	ref, err := gitOutput(root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("no origin/HEAD (git remote set-head origin --auto sets it): %w", err)
	}
	return ref, nil
}

// Merged reports whether wt's HEAD is an ancestor of ref. merge-base, not branch -d: that one checks
// the branch's own upstream and refuses when its remote ref is stale.
func Merged(wt, ref string) (bool, error) {
	_, err := gitOutput(wt, "merge-base", "--is-ancestor", "HEAD", ref)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Branch is the branch checked out in wt, "" for a detached HEAD.
func Branch(wt string) (string, error) {
	return gitOutput(wt, "branch", "--show-current")
}

// DeleteBranch force-deletes branch name in the repo at root.
func DeleteBranch(root, name string) error {
	_, err := gitOutput(root, "branch", "-D", name)
	return err
}

// Untracked lists wt's untracked files relative to wt, ignored ones left out; a nested repo is one
// entry ending in /.
func Untracked(wt string) ([]string, error) {
	out, err := gitOutput(wt, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Archive copies paths (relative to wt, as Untracked lists them) to the same paths under dir, keeping
// their modes and copying symlinks as links.
func Archive(wt, dir string, paths []string) error {
	for _, p := range paths {
		err := filepath.WalkDir(filepath.Join(wt, p), func(src string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(wt, src)
			if err != nil {
				return err
			}
			dst := filepath.Join(dir, rel)
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				link, err := os.Readlink(src)
				if err != nil {
					return err
				}
				if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				return os.Symlink(link, dst)
			case d.Type().IsRegular():
				return copyFile(src, dst)
			}
			return fmt.Errorf("%s is not a file, directory or symlink", src)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// isRegistered checks git rather than the directory: a stray dir would otherwise run every pane on the main checkout.
func isRegistered(root, wt string) (bool, error) {
	out, err := gitOutput(root, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "worktree "+wt {
			return true, nil
		}
	}
	return false, nil
}

// baseRef returns origin's default branch, refreshed from the remote when reachable, else HEAD.
func baseRef(root string, notify func(msg string)) string {
	// fetch alone leaves origin/HEAD stale, so ask the remote what its default branch is
	setHead := gitCmd(root, "remote", "set-head", "--auto", "origin")
	setHead.Stdout = nil
	if gitCmd(root, "fetch", "--quiet", "origin").Run() != nil || setHead.Run() != nil {
		notify("⚠  Could not reach origin — branching from local refs")
	}
	if base, err := gitOutput(root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && base != "" {
		return base
	}
	notify("⚠  No origin/HEAD — branching from current HEAD")
	return "HEAD"
}

// copyIncluded runs git ls-files | git check-ignore | rsync, reporting whether every stage succeeded.
func copyIncluded(root, wt string) bool {
	// --directory lists a wholly matching dir (vendor/) once instead of every file in it; -r copies it
	list := exec.Command("git", "-C", root, "ls-files", "-z", "--others", "--ignored", "--directory", "--exclude-from=.worktreeinclude")
	filter := exec.Command("git", "-C", root, "check-ignore", "-z", "--stdin")
	rsync := exec.Command("rsync", "-a", "-r", "--from0", "--files-from=-", root+"/", wt+"/")
	stages := []*exec.Cmd{list, filter, rsync}
	for _, c := range stages {
		c.Stderr = os.Stderr
	}
	rsync.Stdout = os.Stdout

	var ends []*os.File
	defer func() {
		for _, f := range ends {
			_ = f.Close()
		}
	}()
	for i := range len(stages) - 1 {
		r, w, err := os.Pipe()
		if err != nil {
			return false
		}
		stages[i].Stdout, stages[i+1].Stdin = w, r
		ends = append(ends, r, w)
	}

	started := make([]bool, len(stages))
	for i, c := range stages {
		started[i] = c.Start() == nil
	}
	// Children hold their own copies; closing ours lets EOF and EPIPE reach them if a neighbour died
	for _, f := range ends {
		_ = f.Close()
	}
	ends = nil

	ok := true
	for i, c := range stages {
		if !started[i] {
			ok = false
			continue
		}
		err := c.Wait()
		var exit *exec.ExitError
		// check-ignore exits 1 when nothing matched
		if err != nil && !(c == filter && errors.As(err, &exit) && exit.ExitCode() == 1) {
			ok = false
		}
	}
	return ok
}

// gitCmd returns git -C dir args wired to the caller's stdout and stderr, so git's own messages show as in bash.
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd
}

// gitOutput returns git's stdout without trailing newlines, like $(...); stderr goes into the error.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		err = fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
