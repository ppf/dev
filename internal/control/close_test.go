package control

import (
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
			bin := t.TempDir()
			log := filepath.Join(bin, "log")
			for _, tool := range []string{"gh", "glab"} {
				script := "#!/bin/sh\n" +
					`[ "$1" = auth ] && { [ "$4" = github.com ] || [ "$4" = ghe.example.com ]; exit; }` + "\n" +
					"echo \"" + tool + " $*\" >> " + log + "\ncat <<'EOF'\n" + tt.reply + "\nEOF\n"
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			wt := t.TempDir()
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", tt.origin}} {
				if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}

			err := prDelivered(wt, "origin/main", head)
			if got := errString(err); got != tt.want {
				t.Errorf("prDelivered = %q, want %q", got, tt.want)
			}
			calls, _ := os.ReadFile(log)
			if got := strings.TrimSpace(string(calls)); got != tt.call {
				t.Errorf("calls %q, want %q", got, tt.call)
			}
		})
	}
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
