package cmd

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/control"
)

func TestDelivered(t *testing.T) {
	if _, _, err := execute(t, "", "delivered"); err == nil || err.Error() != "accepts 1 arg(s), received 0" {
		t.Errorf("dev delivered = %v, want an argument error", err)
	}
	dir := t.TempDir()
	want := dir + " isn't in a .claude/worktrees worktree"
	if out, _, err := execute(t, "", "delivered", dir); err == nil || err.Error() != want || out != "" {
		t.Errorf("dev delivered %s = %q, %v, want error %q", dir, out, err, want)
	}
	out, _, err := execute(t, "", "delivered", "--json", dir)
	var d control.Delivery
	if jsonErr := json.Unmarshal([]byte(out), &d); jsonErr != nil || err == nil || d != (control.Delivery{Reason: want}) {
		t.Errorf("dev delivered --json %s = %q (%v), %v, want reason %q and an error", dir, out, jsonErr, err, want)
	}
	if !strings.Contains(out, `"delivered": false`) {
		t.Errorf("dev delivered --json = %q, want delivered false", out)
	}
}

// A worktree whose HEAD is in origin/main is delivered: exit 0, in text and JSON.
func TestDeliveredInBase(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, origin, wt := filepath.Join(dir, "repo"), filepath.Join(dir, "origin.git"), filepath.Join(dir, "repo", ".claude", "worktrees", "w")
	var head string
	for _, args := range [][]string{{"init", "-q", "--bare", "-b", "main", origin}, {"init", "-q", "-b", "main", root},
		{"-C", root, "commit", "-q", "--allow-empty", "-m", "init"}, {"-C", root, "remote", "add", "origin", origin},
		{"-C", root, "push", "-q", "origin", "main"}, {"-C", root, "worktree", "add", "-q", "--detach", wt, "origin/main"},
		{"-C", wt, "rev-parse", "HEAD"}} {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		head = strings.TrimSpace(string(out))
	}
	if out, _, err := execute(t, "", "delivered", wt); err != nil || out != "delivered: "+head+" (in origin/main)\n" {
		t.Errorf("dev delivered = %q, %v, want delivered at %s", out, err, head)
	}
	out, _, err := execute(t, "", "delivered", "--json", wt)
	var d control.Delivery
	if jsonErr := json.Unmarshal([]byte(out), &d); jsonErr != nil || err != nil || d != (control.Delivery{Delivered: true, Head: head, Reason: "in origin/main"}) {
		t.Errorf("dev delivered --json = %q (%v), %v, want delivered at %s", out, jsonErr, err, head)
	}
}
