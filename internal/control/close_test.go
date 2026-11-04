package control

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prDelivered looks the PR/MR up by commit in origin's repository, through fake gh and glab on PATH that log
// their api calls and answer with reply; gh is signed in to github.com and ghe.example.com.
func TestPRDelivered(t *testing.T) {
	const head, other = "aaaa", "bbbb"
	pr := func(state, merged, sha, base string) string {
		r := strings.NewReplacer("STATE", state, "MERGED", merged, "SHA", sha, "BASE", base)
		return r.Replace(`{"number":7,"state":"STATE","merged_at":MERGED,"head":{"sha":"SHA","repo":{"full_name":"me/core"}},` +
			`"base":{"ref":"BASE","repo":{"full_name":"up/core"}}}`)
	}
	mr := func(state, sha string) string {
		r := strings.NewReplacer("STATE", state, "SHA", sha)
		return r.Replace(`{"iid":12,"state":"STATE","sha":"SHA","target_branch":"main"}`)
	}
	list := func(items ...string) string { return "[" + strings.Join(items, ",") + "]" }
	const (
		fork    = "git@github.com:me/core.git"
		lab     = "https://git.example.com/group/sub/proj.git"
		ghCall  = "gh api --hostname github.com repos/me/core/commits/aaaa/pulls"
		labCall = "glab api --hostname git.example.com projects/group%2Fsub%2Fproj/repository/commits/aaaa/merge_requests"
	)
	for _, tt := range []struct {
		name, origin, reply, call, want string
	}{
		{"open fork PR", fork, list(pr("open", "null", head, "main")), ghCall, ""},
		{"merged PR", fork, list(pr("closed", `"2026-11-03T00:00:00Z"`, head, "main")), ghCall, ""},
		{"merged elsewhere", fork, list(pr("closed", `"2026-11-03T00:00:00Z"`, head, "release")), ghCall, "PR #7 was merged into release"},
		{"closed PR", fork, list(pr("closed", "null", head, "main")), ghCall, "PR #7 is closed"},
		{"HEAD behind PR head", fork, list(pr("open", "null", other, "main")), ghCall, "PR #7 is at bbbb, not at its HEAD aaaa"},
		{"one of several PRs", fork, list(pr("closed", "null", other, "main"), pr("open", "null", head, "release")), ghCall, ""},
		{"several refused", fork, list(pr("open", "null", other, "main"), pr("closed", "null", head, "main")), ghCall, "PR #7 is at bbbb, not at its HEAD aaaa; PR #7 is closed"},
		{"other forks' PR", fork, list(strings.ReplaceAll(pr("closed", `"2026-11-03T00:00:00Z"`, head, "main"), "/core", "/other")), ghCall, "no PR has it"},
		{"PR into origin", fork, list(strings.NewReplacer("me/core", "you/core", "up/core", "me/core").Replace(pr("open", "null", head, "main"))), ghCall, ""},
		{"GitHub Enterprise", "git@ghe.example.com:me/core.git", list(pr("open", "null", head, "main")), "gh api --hostname ghe.example.com repos/me/core/commits/aaaa/pulls", ""},
		{"no PR", fork, `[]`, ghCall, "no PR has it"},
		{"open MR", lab, list(mr("opened", head)), labCall, ""},
		{"merged MR", lab, list(mr("merged", head)), labCall, ""},
		{"HEAD behind MR head", lab, list(mr("opened", other)), labCall, "MR #12 is at bbbb, not at its HEAD aaaa"},
		{"local origin", "/srv/origin.git", `[]`, "", "origin /srv/origin.git isn't a GitHub or GitLab repository"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := fakeForges(t, tt.reply)
			wt := t.TempDir()
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", tt.origin}} {
				if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}

			pr, err := prDelivered(wt, "origin/main", head)
			if got := errString(err); got != tt.want {
				t.Errorf("prDelivered = %q, want %q", got, tt.want)
			}
			kind := cmp.Or(map[string]string{lab: "MR"}[tt.origin], "PR")
			if err == nil && (pr.Kind != kind || pr.Head != head) {
				t.Errorf("prDelivered = %+v, want the %s at %s", pr, kind, head)
			}
			calls, _ := os.ReadFile(log)
			if got := strings.TrimSpace(string(calls)); got != tt.call {
				t.Errorf("calls %q, want %q", got, tt.call)
			}
		})
	}
}

// fakeForges puts fake gh and glab first on PATH that log their api calls to the returned file and answer with
// reply; `gh auth status` succeeds for github.com and ghe.example.com.
func fakeForges(t *testing.T, reply string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	for _, tool := range []string{"gh", "glab"} {
		script := "#!/bin/sh\n" +
			`[ "$1" = auth ] && { [ "$4" = github.com ] || [ "$4" = ghe.example.com ]; exit; }` + "\n" +
			"echo \"" + tool + " $*\" >> " + log + "\ncat <<'EOF'\n" + reply + "\nEOF\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return log
}

// origin is a local bare repository, later behind a github.com URL (insteadOf) whose PRs a fake gh lists.
func TestDelivered(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, origin := filepath.Join(dir, "repo"), filepath.Join(dir, "origin.git")
	git(dir, "init", "-q", "--bare", "-b", "main", origin)
	git(dir, "init", "-q", "-b", "main", root)
	git(root, "commit", "-q", "--allow-empty", "-m", "init")
	git(root, "remote", "add", "origin", origin)
	git(root, "push", "-q", "origin", "main")
	worker := func(name string) string {
		wt := filepath.Join(root, ".claude", "worktrees", name)
		git(root, "worktree", "add", "-q", "-b", name, wt, "origin/main")
		return wt
	}
	inBase, dirty, ahead := worker("in-base"), worker("dirty"), worker("ahead")
	if err := os.WriteFile(filepath.Join(dirty, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	git(dirty, "add", "f")
	git(ahead, "commit", "-q", "--allow-empty", "-m", "work")
	head := git(ahead, "rev-parse", "HEAD")

	check := func(name, path string, want Delivery) {
		t.Helper()
		got, err := Delivered(path)
		if got.Delivered != (err == nil) || err != nil && got.Reason != err.Error() {
			t.Errorf("%s: Delivered = %+v, %v: delivered and reason disagree with the error", name, got, err)
		}
		if got.Delivered != want.Delivered || !strings.Contains(got.Reason, want.Reason) || got.PR != want.PR || got.Head != want.Head {
			t.Errorf("%s: Delivered = %+v, want %+v", name, got, want)
		}
	}
	check("in base", inBase, Delivery{Delivered: true, Head: git(inBase, "rev-parse", "HEAD"), Reason: "in origin/main"})
	check("main checkout", root, Delivery{Reason: root + " isn't in a .claude/worktrees worktree"})
	check("dirty", dirty, Delivery{Head: git(dirty, "rev-parse", "HEAD"), Reason: dirty + " has uncommitted changes to tracked files"})
	check("ahead, local origin", ahead, Delivery{Head: head, Reason: "and no PR/MR open or merged at its HEAD: origin " + origin + " isn't a GitHub or GitLab repository"})

	const github = "git@github.com:me/core.git"
	git(root, "remote", "set-url", "origin", github)
	git(root, "config", "url."+origin+".insteadOf", github)
	fakeForges(t, `[{"number":7,"state":"open","merged_at":null,"head":{"sha":"`+head+`","repo":{"full_name":"me/core"}},`+
		`"base":{"ref":"main","repo":{"full_name":"me/core"}}}]`)
	check("open PR", ahead, Delivery{Delivered: true, Head: head, Reason: "PR #7 open", PR: 7})
	if err := os.WriteFile(filepath.Join(ahead, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	git(ahead, "add", "f")
	check("dirty at an open PR", ahead, Delivery{Head: head, Reason: ahead + " has uncommitted changes to tracked files"})
	git(ahead, "commit", "-q", "--allow-empty", "-m", "unpushed")
	check("commit after the PR", ahead, Delivery{Head: git(ahead, "rev-parse", "HEAD"), Reason: "PR #7 is at " + head})
}

func TestRemoteRepo(t *testing.T) {
	for _, tt := range []struct{ remote, host, repo string }{
		{"git@github.com:ppf/core.git", "github.com", "ppf/core"},
		{"https://github.com/ppf/core", "github.com", "ppf/core"},
		{"ssh://git@git.example.com:2222/group/sub/proj.git", "git.example.com", "group/sub/proj"},
		{"git.example.com:group/proj.git", "git.example.com", "group/proj"},
		{"https://git.example.com:8443/group/proj", "git.example.com:8443", "group/proj"},
	} {
		if host, repo, err := remoteRepo(tt.remote); err != nil || host != tt.host || repo != tt.repo {
			t.Errorf("remoteRepo(%q) = %q, %q, %v; want %q, %q", tt.remote, host, repo, err, tt.host, tt.repo)
		}
	}
	for _, remote := range []string{"/srv/origin.git", "../origin", "file:///srv/origin.git", "https://github.com/"} {
		if _, _, err := remoteRepo(remote); err == nil {
			t.Errorf("remoteRepo(%q): want an error", remote)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
