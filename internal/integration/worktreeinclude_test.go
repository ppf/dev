//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// gitEnv keeps the user's git config (hooks, signing) out of the temp repos.
var gitEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=dev-test", "GIT_AUTHOR_EMAIL=dev-test@example.com",
	"GIT_COMMITTER_NAME=dev-test", "GIT_COMMITTER_EMAIL=dev-test@example.com",
}

// The .worktreeinclude cases from ppf/terminal#8, run through project.sh's DEV_WORKTREE mode: gitignored
// files matching .worktreeinclude are copied into a fresh worktree; a failed copy removes the worktree and
// deletes the branch only if it was just created.
func TestWorktreeInclude(t *testing.T) {
	project := filepath.Join(tmuxHome(t), "sessions/project.sh")
	requireFiles(t, project)
	tmp := t.TempDir()
	// claude, lazygit and nvim are stubbed so the layout doesn't start real apps in temp repos
	stubs := writeStubs(t, filepath.Join(tmp, "stubs"), "exec sleep 600", "claude", "lazygit", "nvim")
	failingRsync := writeStubs(t, filepath.Join(tmp, "failing"), "exit 1", "rsync")
	path := stubs + ":" + os.Getenv("PATH")
	// @dev_restored skips project.sh's first-run restore. Panes run a non-login /bin/sh: a login shell
	// reads /etc/profile and ~/.profile, which put the real binaries ahead of the stubs.
	s := newServer(t, `set -g base-index 1
setw -g pane-base-index 1
set -g default-shell /bin/sh
set -g default-command /bin/sh
set -g @dev_restored 1
set -g @resurrect-dir '`+tmp+`/resurrect'
`, "PATH="+path, "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	s.T("new-session", "-d", "-s", "boot")

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
		t.Run(tt.name, func(t *testing.T) {
			s := s.with(t)
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
			env := append(s.scriptEnv(gitEnv...), "DEV_WORKTREE=feat")
			if tt.failCopy {
				env = append(env, "PATH="+failingRsync+":"+path)
			}
			name := "wt" + string(rune('a'+i))
			out, err := runScript(env, project, root, name)
			worktree := filepath.Join(root, ".claude/worktrees/feat")

			if tt.failCopy {
				if err == nil || !strings.Contains(out, "worktree removed") {
					t.Fatalf("want failure with cleanup message, got err=%v\n%s", err, out)
				}
				_, err := s.Try("has-session", "-t", tmux.SessionTarget(name+"-feat"))
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
			if _, err := s.Try("has-session", "-t", "="+session+":"); err != nil {
				t.Fatalf("session %s not created: %v\n%s", session, err, out)
			}
			for _, pane := range []string{":1.2", ":1.3", ":2.1"} { // claude, lazygit, nvim
				waitFor(t, "stub in "+session+pane, func() bool {
					cmd, _ := s.Try("display", "-t", "="+session+pane, "-p", "#{pane_current_command}")
					return cmd == "sleep"
				})
			}
			s.T("kill-session", "-t", "="+session+":")
			for _, f := range tt.copied {
				check(t, "copied "+f, exists(filepath.Join(worktree, f)), "present")
			}
			for _, f := range tt.notCopied {
				check(t, "not copied "+f, exists(filepath.Join(worktree, f)), "absent")
			}
		})
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
