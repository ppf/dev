//go:build integration

package integration

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dev boss starts dev serve detached when none answers: once, outliving tmux, never failing the boss; off with
// DEV_NO_SERVE or --no-serve, on a private tmux server only with DEV_SERVE_ADDR, not with a launchd agent installed.
func TestBossStartsServe(t *testing.T) {
	d := newDevServer(t, "set -g @dev_notify off\n")
	stubs := filepath.SplitList(d.path)[0]
	if err := os.Remove(filepath.Join(stubs, "claude")); err != nil {
		t.Fatal(err)
	}
	agentStub(t, stubs, "claude")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	// Never the user's tmux server, launchd agent or default address
	base := d.scriptEnv(writeProjects(t, [2]string{"hq", tempDir(t)}, [2]string{"app", tempDir(t)}),
		"DEV_NO_SERVE=", "DEV_SERVE_ADDR=", "TMUX_TMPDIR="+tempDir(t), "HOME="+tempDir(t))
	env := append(base, "DEV_SERVE_ADDR="+addr)
	run := func(env []string, args ...string) (string, string) {
		cmd := exec.Command(devBin, args...)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		return errStatus(cmd.Run()) + " " + stdout.String(), stderr.String()
	}
	pids := func() []int {
		out, _ := exec.Command("pgrep", "-f", devBin+" serve --awake=false").Output()
		var ps []int
		for _, f := range strings.Fields(string(out)) {
			if n, err := strconv.Atoi(f); err == nil {
				ps = append(ps, n)
			}
		}
		return ps
	}
	stop := func() {
		for _, p := range pids() {
			_ = syscall.Kill(p, syscall.SIGTERM)
		}
		waitFor(t, "dev serve gone", func() bool { return len(pids()) == 0 })
	}
	t.Cleanup(stop)
	health := func() bool {
		resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/v1/health")
		if err == nil {
			_ = resp.Body.Close()
		}
		return err == nil && resp.StatusCode == http.StatusOK
	}

	out, errOut := run(base, "boss", "hq", "--background", "--timeout", "30s")
	check(t, "private server, no DEV_SERVE_ADDR", out+errOut+strconv.Itoa(len(pids())),
		"0 boss-hq\n⚠ dev serve not started: this boss's tmux server isn't the default one: set DEV_SERVE_ADDR to start it for this one\n0")
	out, errOut = run(append(env, "DEV_NO_SERVE=1"), "boss", "hq", "--background")
	check(t, "DEV_NO_SERVE", out+errOut+strconv.Itoa(len(pids())), "0 boss-hq\n0")
	out, errOut = run(env, "boss", "hq", "--background", "--no-serve")
	check(t, "--no-serve", out+errOut+strconv.Itoa(len(pids())), "0 boss-hq\n0")
	home := tempDir(t)
	writeFile(t, filepath.Join(home, "Library/LaunchAgents/dev.serve.plist"), "<plist/>")
	out, errOut = run(append(env, "HOME="+home), "boss", "hq", "--background")
	check(t, "launchd agent", out+errOut+strconv.Itoa(len(pids())),
		"0 boss-hq\n⚠ dev serve: launchd agent installed but not answering: launchctl kickstart gui/"+strconv.Itoa(os.Getuid())+"/dev.serve (not loaded: launchctl bootstrap gui/"+strconv.Itoa(os.Getuid())+" \""+filepath.Join(home, "Library/LaunchAgents/dev.serve.plist")+"\")\n0")

	out, errOut = run(env, "boss", "hq", "--background")
	check(t, "started", out+errOut, "0 boss-hq\ndev serve started: http://"+addr+"/ (pair this browser: dev serve pair)\n")
	t.Logf("printed: %s", strings.TrimSpace(errOut))
	first := pids()
	if len(first) != 1 || !health() {
		t.Fatalf("serve %v, health %v", first, health())
	}
	ppid, _ := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(first[0])).Output()
	check(t, "reparented", strings.TrimSpace(string(ppid)), "1")

	out, errOut = run(env, "boss", "app", "--background", "--timeout", "30s")
	check(t, "already running", out+errOut, "0 boss-app\n")
	if got := pids(); len(got) != 1 || got[0] != first[0] {
		t.Fatalf("serve %v, want %v", got, first)
	}

	// Alive but not answering (hung, its address gone): no second one
	if err := syscall.Kill(first[0], syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	out, errOut = run(env, "boss", "hq", "--background")
	_ = syscall.Kill(first[0], syscall.SIGCONT)
	if out != "0 boss-hq\n" || !strings.HasPrefix(errOut, "⚠ dev serve runs but doesn't answer on its address") || len(pids()) != 1 {
		t.Errorf("stopped: %q %q %v", out, errOut, pids())
	}

	stop()
	taken, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut = run(env, "boss", "hq", "--background")
	_ = taken.Close()
	if out != "0 boss-hq\n" || !strings.HasPrefix(errOut, "⚠ dev serve not started: ") ||
		!strings.Contains(errOut, "address already in use") || strings.Count(errOut, "\n") != 1 {
		t.Errorf("port taken: %q %q", out, errOut)
	}

	out, _ = run(env, "boss", "hq", "--background")
	check(t, "started again", out, "0 boss-hq\n")
	d.kill()
	if !health() {
		t.Fatal("dev serve ended with the tmux server")
	}
}
