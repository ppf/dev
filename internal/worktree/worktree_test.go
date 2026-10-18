package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sandbox keeps the user's git config (hooks, signing) away from both the test helpers and the code under test.
func sandbox(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "dev-test", "GIT_AUTHOR_EMAIL": "dev-test@example.com",
		"GIT_COMMITTER_NAME": "dev-test", "GIT_COMMITTER_EMAIL": "dev-test@example.com",
	} {
		t.Setenv(k, v)
	}
	// git reports realpaths (/private/var on macOS)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// newRepo creates a repo on main with a bare origin, committing .gitignore and .worktreeinclude.
func newRepo(t *testing.T, gitignore, include string) string {
	t.Helper()
	dir := sandbox(t)
	root, origin := filepath.Join(dir, "repo"), filepath.Join(dir, "origin.git")
	git(t, dir, "init", "-q", "-b", "main", root)
	git(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	writeFile(t, filepath.Join(root, ".gitignore"), gitignore)
	writeFile(t, filepath.Join(root, ".worktreeinclude"), include)
	git(t, root, "add", ".gitignore", ".worktreeinclude")
	git(t, root, "commit", "-qm", "init")
	git(t, root, "remote", "add", "origin", origin)
	git(t, root, "push", "-q", "origin", "main")
	return root
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// failingRsync puts an rsync that always exits 1 first on PATH.
func failingRsync(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rsync"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, "rsync"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func collect(msgs *[]string) func(string) {
	return func(m string) { *msgs = append(*msgs, m) }
}

func TestEnsureWorktreeInclude(t *testing.T) {
	tests := []struct {
		name, gitignore, include string
		files                    []string
		nested                   string // ignored dir that is its own git repo
		copied, notCopied        []string
	}{
		{
			name:      "whole dir",
			gitignore: "vendor/\nbuild/\n", include: "vendor/\nnotes.txt\n",
			files:     []string{"vendor/pkg/a.go", "vendor/b.txt", "build/out.txt", "notes.txt"},
			copied:    []string{"vendor/pkg/a.go", "vendor/b.txt"},
			notCopied: []string{"build/out.txt", "notes.txt"},
		},
		{
			name:      "single file in ignored dir",
			gitignore: "config/\n", include: "config/local.env\n",
			files:     []string{"config/local.env", "config/other.env"},
			copied:    []string{"config/local.env"},
			notCopied: []string{"config/other.env"},
		},
		{
			name:      "nested git repo",
			gitignore: "libs/\n", include: "libs/inner/\n",
			nested: "libs/inner",
			copied: []string{"libs/inner/f.txt", "libs/inner/.git/HEAD"},
		},
		{
			// check-ignore exits 1 when nothing it's given is ignored; that is not a failure
			name:      "nothing ignored matches",
			gitignore: "build/\n", include: "notes.txt\n",
			files:     []string{"notes.txt", "build/out.txt"},
			notCopied: []string{"notes.txt", "build/out.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRepo(t, tt.gitignore, tt.include)
			for _, f := range tt.files {
				writeFile(t, filepath.Join(root, f), f)
			}
			if tt.nested != "" {
				nested := filepath.Join(root, tt.nested)
				writeFile(t, filepath.Join(nested, "f.txt"), "nested")
				git(t, nested, "init", "-q")
				git(t, nested, "add", "f.txt")
				git(t, nested, "commit", "-qm", "nested")
			}

			var msgs []string
			wt, err := Ensure(root, "feat", collect(&msgs))
			if err != nil {
				t.Fatal(err)
			}
			if want := root + "/.claude/worktrees/feat"; wt != want {
				t.Fatalf("path = %q, want %q", wt, want)
			}
			if len(msgs) != 0 {
				t.Errorf("notices = %q, want none", msgs)
			}
			for _, f := range tt.copied {
				if !exists(filepath.Join(wt, f)) {
					t.Errorf("%s not copied", f)
				}
			}
			for _, f := range tt.notCopied {
				if exists(filepath.Join(wt, f)) {
					t.Errorf("%s copied", f)
				}
			}
		})
	}
}

func TestEnsureFailedCopy(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new branch removed"
		if existing {
			name = "existing branch kept"
		}
		t.Run(name, func(t *testing.T) {
			root := newRepo(t, "vendor/\n", "vendor/\n")
			writeFile(t, filepath.Join(root, "vendor/a.txt"), "a")
			if existing {
				git(t, root, "branch", "feat")
			}
			failingRsync(t)

			_, err := Ensure(root, "feat", func(string) {})
			wt := root + "/.claude/worktrees/feat"
			if want := "Copying .worktreeinclude files into " + wt + " failed — worktree removed"; err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if exists(wt) {
				t.Error("worktree dir left behind")
			}
			if strings.Contains(git(t, root, "worktree", "list", "--porcelain"), wt) {
				t.Error("worktree still registered")
			}
			branch := exec.Command("git", "-C", root, "show-ref", "--quiet", "--verify", "refs/heads/feat").Run() == nil
			if branch != existing {
				t.Errorf("branch feat present = %v, want %v", branch, existing)
			}
		})
	}
}

func TestEnsureReuseDoesNotCopyAgain(t *testing.T) {
	root := newRepo(t, "vendor/\n", "vendor/\n")
	writeFile(t, filepath.Join(root, "vendor/a.txt"), "a")
	first, err := Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "vendor/b.txt"), "b")
	failingRsync(t)

	var msgs []string
	second, err := Ensure(root, "feat", collect(&msgs))
	if err != nil || second != first {
		t.Fatalf("Ensure = %q, %v; want %q", second, err, first)
	}
	if len(msgs) != 0 {
		t.Errorf("notices = %q, want none", msgs)
	}
	if exists(filepath.Join(first, "vendor/b.txt")) {
		t.Error("reused worktree copied again")
	}
}

func TestEnsureBase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  func(t *testing.T, root string) string // commit the worktree must be on
		msgs  []string
	}{
		{
			name: "origin HEAD, not local HEAD",
			setup: func(t *testing.T, root string) {
				git(t, root, "commit", "-q", "--allow-empty", "-m", "local only")
			},
			want: func(t *testing.T, root string) string { return git(t, root, "rev-parse", "origin/main") },
		},
		{
			name: "no origin",
			setup: func(t *testing.T, root string) {
				git(t, root, "remote", "remove", "origin")
			},
			want: func(t *testing.T, root string) string { return git(t, root, "rev-parse", "HEAD") },
			msgs: []string{
				"⚠  Could not reach origin — branching from local refs",
				"⚠  No origin/HEAD — branching from current HEAD",
			},
		},
		{
			name: "unreachable origin falls back to local origin/HEAD",
			setup: func(t *testing.T, root string) {
				git(t, root, "remote", "set-head", "--auto", "origin")
				git(t, root, "commit", "-q", "--allow-empty", "-m", "local only")
				git(t, root, "remote", "set-url", "origin", filepath.Join(filepath.Dir(root), "missing.git"))
			},
			want: func(t *testing.T, root string) string { return git(t, root, "rev-parse", "origin/main") },
			msgs: []string{"⚠  Could not reach origin — branching from local refs"},
		},
		{
			name: "existing branch checked out as-is",
			setup: func(t *testing.T, root string) {
				git(t, root, "branch", "feat")
				git(t, root, "commit", "-q", "--allow-empty", "-m", "after feat")
				git(t, root, "push", "-q", "origin", "main")
			},
			want: func(t *testing.T, root string) string { return git(t, root, "rev-parse", "feat") },
			msgs: []string{"ℹ  Branch feat exists — checking it out as-is"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRepo(t, "", "")
			tt.setup(t, root)
			want := tt.want(t, root)

			var msgs []string
			wt, err := Ensure(root, "feat", collect(&msgs))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(msgs, tt.msgs) {
				t.Errorf("notices = %q, want %q", msgs, tt.msgs)
			}
			if got := git(t, wt, "rev-parse", "HEAD"); got != want {
				t.Errorf("worktree HEAD = %s, want %s", got, want)
			}
			if got := git(t, wt, "branch", "--show-current"); got != "feat" {
				t.Errorf("branch = %q, want feat", got)
			}
		})
	}
}

func TestEnsurePrunesDeletedWorktree(t *testing.T) {
	root := newRepo(t, "", "")
	wt, err := Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	if _, err := Ensure(root, "feat", collect(&msgs)); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(wt, ".gitignore")) {
		t.Error("worktree not recreated")
	}
	if want := []string{"ℹ  Branch feat exists — checking it out as-is"}; !slices.Equal(msgs, want) {
		t.Errorf("notices = %q, want %q", msgs, want)
	}
}

func TestEnsureErrors(t *testing.T) {
	dir := sandbox(t)
	if _, err := Ensure(dir, "feat", func(string) {}); err == nil || err.Error() != "Not a git repo: "+dir {
		t.Errorf("not a repo: err = %v", err)
	}

	root := newRepo(t, "", "")
	git(t, root, "remote", "remove", "origin")
	// a name git rejects as a branch makes worktree add fail
	_, err := Ensure(root, "bad..name", func(string) {})
	if want := "Failed to create worktree " + root + "/.claude/worktrees/bad..name from HEAD"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestList(t *testing.T) {
	root := newRepo(t, "", "")
	for _, name := range []string{"feature/one", "plain"} {
		if _, err := Ensure(root, name, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "worktree", "add", "-q", filepath.Join(filepath.Dir(root), "elsewhere"))

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"feature/one", "plain"}; !slices.Equal(got, want) {
		t.Errorf("List = %q, want %q", got, want)
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"feature/one-2.x_y": "feature/one-2.x_y",
		"a b\tc":            "abc",
		"fix:bug~1^":        "fixbug1",
		"zażółć\xff":        "za",
		"":                  "",
	} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionWorktree(t *testing.T) {
	root := newRepo(t, "", "")
	wt, err := Ensure(root, "feature/one", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(wt, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if mainRoot, wtPath, ok := SessionWorktree(sub); !ok || mainRoot != root || wtPath != wt {
		t.Errorf("SessionWorktree(sub) = %q, %q, %v; want %q, %q, true", mainRoot, wtPath, ok, root, wt)
	}
	for _, p := range []string{root, filepath.Dir(root)} {
		if _, _, ok := SessionWorktree(p); ok {
			t.Errorf("SessionWorktree(%s) ok, want not a worktree", p)
		}
	}
}

func TestRemove(t *testing.T) {
	root := newRepo(t, "", "")
	wt, err := Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "dirty.txt"), "uncommitted")

	if err := Remove(root, wt); err != nil {
		t.Fatal(err)
	}
	if exists(wt) || strings.Contains(git(t, root, "worktree", "list", "--porcelain"), wt) {
		t.Error("worktree not removed")
	}
	if err := Remove(root, wt); err == nil || !strings.Contains(err.Error(), "is not a working tree") {
		t.Errorf("second Remove err = %v, want git's message", err)
	}
}

func TestDirty(t *testing.T) {
	root := newRepo(t, "", "")
	for _, tt := range []struct {
		name  string
		setup func()
		want  bool
	}{
		{"clean", func() {}, false},
		{"untracked only", func() { writeFile(t, filepath.Join(root, "new.txt"), "x") }, false},
		{"staged", func() { git(t, root, "add", "new.txt") }, true},
		{"modified", func() {
			git(t, root, "rm", "-q", "--cached", "new.txt")
			writeFile(t, filepath.Join(root, ".gitignore"), "x")
		}, true},
	} {
		tt.setup()
		if got, err := Dirty(root); err != nil || got != tt.want {
			t.Errorf("%s: Dirty = %t, %v; want %t", tt.name, got, err, tt.want)
		}
	}
}

// commit commits a new file name in dir.
func commit(t *testing.T, dir, name string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), name)
	git(t, dir, "add", name)
	git(t, dir, "commit", "-qm", name)
}

func TestMerged(t *testing.T) {
	root := newRepo(t, "", "")
	wt, err := Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	merged := func(name string, want bool) {
		t.Helper()
		base, err := FetchDefault(root)
		if err != nil || base != "origin/main" {
			t.Fatalf("FetchDefault = %q, %v; want origin/main", base, err)
		}
		if got, err := Merged(wt, base); err != nil || got != want {
			t.Errorf("%s: Merged = %t, %v; want %t", name, got, err, want)
		}
	}
	merged("fresh worktree", true)

	// The PR's branch is pushed, then gets one more commit merged without pushing it again: origin/feat
	// is stale, so git branch -d refuses, though everything is in origin/main
	commit(t, wt, "a.txt")
	git(t, wt, "push", "-q", "-u", "origin", "feat")
	commit(t, wt, "b.txt")
	merged("commits not in origin/main", false)
	other := filepath.Join(filepath.Dir(root), "other")
	git(t, root, "clone", "-q", filepath.Join(filepath.Dir(root), "origin.git"), other)
	git(t, other, "fetch", "-q", wt, "feat")
	git(t, other, "merge", "-q", "--no-ff", "-m", "merge", "FETCH_HEAD")
	git(t, other, "push", "-q", "origin", "main")
	if got, err := Merged(wt, "origin/main"); err != nil || got {
		t.Errorf("before the fetch: Merged = %t, %v; want false", got, err)
	}
	merged("merged on origin, upstream stale", true)
	if _, err := Merged(wt, "origin/nope"); err == nil {
		t.Error("Merged with an unknown ref: no error")
	}
	if err := Remove(root, wt); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "branch", "-d", "feat").CombinedOutput(); err == nil {
		t.Errorf("git branch -d deleted feat despite its stale upstream: %s", out)
	}
	if err := DeleteBranch(root, "feat"); err != nil {
		t.Error(err)
	}
}

func TestBranch(t *testing.T) {
	root := newRepo(t, "", "")
	wt, err := Ensure(root, "feat", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Branch(wt); err != nil || got != "feat" {
		t.Errorf("Branch = %q, %v; want feat", got, err)
	}
	git(t, wt, "checkout", "-q", "--detach")
	if got, err := Branch(wt); err != nil || got != "" {
		t.Errorf("detached: Branch = %q, %v; want empty", got, err)
	}
	if err := DeleteBranch(root, "feat"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteBranch(root, "feat"); err == nil {
		t.Error("second DeleteBranch: no error")
	}
}

func TestArchiveUntracked(t *testing.T) {
	root := newRepo(t, "ignored.txt\n", "")
	writeFile(t, filepath.Join(root, "RESULT.md"), "result")
	writeFile(t, filepath.Join(root, "notes", "a.txt"), "a")
	writeFile(t, filepath.Join(root, "ignored.txt"), "ignored")
	if err := os.Chmod(filepath.Join(root, "notes", "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("RESULT.md", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "inner")
	git(t, root, "init", "-q", nested)
	writeFile(t, filepath.Join(nested, "f.txt"), "f")

	paths, err := Untracked(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"RESULT.md", "inner/", "link", "notes/a.txt"}; !slices.Equal(paths, want) {
		t.Fatalf("Untracked = %q, want %q", paths, want)
	}
	dir := filepath.Join(sandbox(t), "results", "s")
	if err := Archive(root, dir, paths); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"RESULT.md": "result", "notes/a.txt": "a", "inner/f.txt": "f", "link": "result"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if link, err := os.Readlink(filepath.Join(dir, "link")); err != nil || link != "RESULT.md" {
		t.Errorf("link -> %q, %v; want a symlink to RESULT.md", link, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "notes", "a.txt")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("notes/a.txt mode = %v, %v; want 0755", info.Mode(), err)
	}
	if exists(filepath.Join(dir, "ignored.txt")) {
		t.Error("ignored file archived")
	}
	if err := Archive(root, dir, paths); err != nil {
		t.Errorf("second Archive: %v", err)
	}
}
