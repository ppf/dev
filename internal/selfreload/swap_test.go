//go:build integration

package selfreload

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const helperEnv = "SELFRELOAD_TEST_EXEC"

// TestSwapRealBuilds builds dev twice from a scratch clone (HEAD, then an empty commit on it: two
// pseudo-versions), installs "new" over "old" by rename, as a release is assumed to, and checks the
// watcher sees it (real stat, buildinfo read and --version run) and Exec runs the new file.
func TestSwapRealBuilds(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		t.Skip("helper process")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	clone := filepath.Join(dir, "clone")
	run(t, "", "git", "clone", "--quiet", root, clone)
	exe := filepath.Join(dir, "bin", "dev")
	run(t, clone, "go", "build", "-o", exe, ".")
	run(t, clone, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null", "commit", "--quiet", "--allow-empty", "-m", "next")
	next := filepath.Join(dir, "next")
	run(t, clone, "go", "build", "-o", next, ".")

	oldV := strings.TrimSpace(run(t, "", exe, "--version"))
	newV := strings.TrimSpace(run(t, "", next, "--version"))
	if oldV == newV {
		t.Fatalf("both builds report %q", oldV)
	}
	if v, err := fileVersion(exe); err != nil || v != oldV {
		t.Fatalf("fileVersion(old) = %q, %v, want %q", v, err, oldV)
	}

	w := newWatcher(Options{Exe: exe, Own: oldV, Warn: func(err error) { t.Error(err) }})
	w.getenv = func(k string) string {
		if k == envDevel {
			return "1"
		}
		return ""
	}
	w.after = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	ctx := context.Background()
	if r, ok := w.step(ctx); ok {
		t.Fatalf("Ready %q before the swap", r.Version)
	}
	if err := os.Rename(next, exe); err != nil {
		t.Fatal(err)
	}
	r, ok := w.step(ctx)
	if !ok || r.Version != newV {
		t.Fatalf("after the swap: Ready %q %v, want %q", r.Version, ok, newV)
	}

	helper := exec.Command(os.Args[0], "-test.run=^TestExecHelper$")
	helper.Env = append(os.Environ(), helperEnv+"="+exe)
	out, err := helper.Output()
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != newV {
		t.Fatalf("exec'd helper printed %q, want %q", got, newV)
	}
}

// TestExecHelper becomes `<exe> --version` through Exec when run by TestSwapRealBuilds.
func TestExecHelper(t *testing.T) {
	exe := os.Getenv(helperEnv)
	if exe == "" {
		t.Skip("run by TestSwapRealBuilds")
	}
	w := newWatcher(Options{Exe: exe})
	w.getenv = func(string) string { return "" }
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	w.seen = fi
	t.Fatal(Exec(Ready{Version: "next", w: w}, []string{exe, "--version"}))
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}
