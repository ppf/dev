//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv keeps the user's git config (hooks, signing) out of the temp repos.
var gitEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=dev-test", "GIT_AUTHOR_EMAIL=dev-test@example.com",
	"GIT_COMMITTER_NAME=dev-test", "GIT_COMMITTER_EMAIL=dev-test@example.com",
}

// The .worktreeinclude cases from ppf/terminal#8, each run through project.sh's DEV_WORKTREE mode and through
// the dev picker: gitignored files matching .worktreeinclude are copied into a fresh worktree; a failed copy
// removes the worktree and deletes the branch only if it was just created.
func TestWorktreeInclude(t *testing.T) {
	d := newDevServer(t, "set -g @dev_restored 1\n")
	failingRsync := writeStubs(t, filepath.Join(d.tmp, "failing"), "exit 1", "rsync")

	tests := []struct {
		name, gitignore, include string
		files                    []string
		nested                   string // ignored dir that is its own git repo
		existingBranch, failCopy bool
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
			name:      "failed copy removes worktree and new branch",
			gitignore: "vendor/\n", include: "vendor/\n",
			files:    []string{"vendor/a.txt"},
			failCopy: true,
		},
		{
			name:      "failed copy keeps existing branch",
			gitignore: "vendor/\n", include: "vendor/\n",
			files:          []string{"vendor/a.txt"},
			existingBranch: true, failCopy: true,
		},
	}
	for i, tt := range tests {
		for _, impl := range []string{"bash", "dev"} {
			t.Run(impl+"/"+tt.name, func(t *testing.T) {
				d := d.with(t)
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
				if tt.existingBranch {
					git(t, root, "branch", "feat")
				}
				path := d.path
				if tt.failCopy {
					path = failingRsync + ":" + d.path
				}
				name := impl + string(rune('a'+i))
				var out, status string
				if impl == "bash" {
					var err error
					out, err = runScript(append(d.scriptEnv(gitEnv...), "DEV_WORKTREE=feat", "PATH="+path), projectScript(t), root, name)
					status = errStatus(err)
				} else {
					steps := []step{
						{wait: "Select project", keys: []string{"Enter"}},
						{wait: "Session mode", keys: []string{"feat", "Enter"}},
					}
					if tt.failCopy {
						steps = append(steps, step{wait: "Press enter to close", keys: []string{"Enter"}})
					}
					env := append([]string{writeProjects(t, [2]string{name, root}), popupGuard, "PATH=" + path}, gitEnv...)
					out, status = d.runInPane(env, []string{devBin}, steps...)
				}
				worktree := filepath.Join(root, ".claude/worktrees/feat")

				if tt.failCopy {
					want := "Copying .worktreeinclude files into " + worktree + " failed — worktree removed"
					if status != "1" || !strings.Contains(out, "❌ "+want) {
						t.Fatalf("want exit 1 with %q, got exit %s\n%s", want, status, out)
					}
					_, err := d.Try("has-session", "-t", "="+name+"-feat:")
					check(t, "no session created", boolStr(err != nil), "1")
					check(t, "worktree dir removed", exists(worktree), "absent")
					check(t, "worktree unregistered", boolStr(strings.Contains(git(t, root, "worktree", "list", "--porcelain"), worktree)), "0")
					wantBranch := "absent"
					if tt.existingBranch {
						wantBranch = "present"
					}
					branch := "absent"
					if _, err := gitTry(root, "show-ref", "--verify", "refs/heads/feat"); err == nil {
						branch = "present"
					}
					check(t, "branch feat", branch, wantBranch)
					return
				}

				session := name + "-feat"
				if status != "0" {
					t.Fatalf("exit %s\n%s", status, out)
				}
				if _, err := d.Try("has-session", "-t", "="+session+":"); err != nil {
					t.Fatalf("session %s not created: %v\n%s", session, err, out)
				}
				d.waitForStubs(session)
				// Back to boot first: the attached client would detach with its session (detach-on-destroy)
				d.T("switch-client", "-t", "=boot:", ";", "kill-session", "-t", "="+session+":")
				for _, f := range tt.copied {
					check(t, "copied "+f, exists(filepath.Join(worktree, f)), "present")
				}
				for _, f := range tt.notCopied {
					check(t, "not copied "+f, exists(filepath.Join(worktree, f)), "absent")
				}
			})
		}
	}
}

// newRepo creates a repo on main with a bare origin, committing .gitignore and .worktreeinclude.
func newRepo(t *testing.T, gitignore, include string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeStubs(t *testing.T, dir, body string, names ...string) string {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		writeFile(t, p, "#!/bin/sh\n"+body+"\n")
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
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

func exists(path string) string {
	if _, err := os.Stat(path); err != nil {
		return "absent"
	}
	return "present"
}
