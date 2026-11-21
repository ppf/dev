package selfreload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ppf/dev/internal/tmux"
)

const (
	oldV = "v1.0.0"
	newV = "v1.1.0"
)

// rig is a watcher on a real temp file with a fake clock, version reader, --version check and exec.
type rig struct {
	mu      sync.Mutex // the watch loop and markOK run at once
	w       *watcher
	now     time.Duration
	sleeps  []time.Duration
	reads   []time.Duration
	version func(n int) (string, error) // the n-th read's result
	checks  []string
	checkFn func(want string) error
	env     map[string]string
	execs   [][]string // env of each execve call
	execErr error
	warns   []error
	opts    map[string]string // the fake tmux server's global options
	tmuxErr error             // every tmux call fails with it
	failOn  func(args []string) error
}

func newRig(t *testing.T) *rig {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		version: func(int) (string, error) { return newV, nil },
		checkFn: func(string) error { return nil },
		env:     map[string]string{},
		opts:    map[string]string{},
	}
	r.w = &watcher{
		o: Options{Exe: exe, Own: oldV, Stagger: 5 * time.Second, Warn: func(err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.warns = append(r.warns, err)
		}},
		after: func(d time.Duration) <-chan time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.now += d
			r.sleeps = append(r.sleeps, d)
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
		stat: os.Stat,
		read: func(string) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reads = append(r.reads, r.now)
			return r.version(len(r.reads) - 1)
		},
		check: func(_ context.Context, _, want string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.checks = append(r.checks, want)
			return r.checkFn(want)
		},
		getenv: func(k string) string { return r.env[k] },
		execve: func(_ string, _ []string, env []string) error {
			r.execs = append(r.execs, env)
			return r.execErr
		},
		stagger: func(d time.Duration) time.Duration { return d },
	}
	return r
}

// withTmux gives the rig a fake tmux server holding r.opts.
func (r *rig) withTmux(canary bool) *rig {
	r.w.o.Tmux = tmux.NewFake(func(args []string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tmuxErr != nil {
			return "", r.tmuxErr
		}
		if r.failOn != nil {
			if err := r.failOn(args); err != nil {
				return "", err
			}
		}
		switch {
		case args[0] == "show-options" && args[1] == "-gqv":
			return r.opts[args[2]] + "\n", nil
		case args[0] == "set-option" && args[1] == "-g":
			r.opts[args[2]] = args[3]
		case args[0] == "set-option" && args[1] == "-go":
			if _, set := r.opts[args[2]]; set {
				return "", &tmux.Error{Stderr: "already set: " + args[2], Err: errors.New("exit status 1")}
			}
			r.opts[args[2]] = args[3]
		case args[0] == "set-option" && args[1] == "-gu":
			delete(r.opts, args[2])
		default:
			return "", errors.New("unexpected tmux " + strings.Join(args, " "))
		}
		return "", nil
	})
	r.w.o.Canary = canary
	return r
}

// clone is another pane watching the same file on the same tmux server.
func (r *rig) clone(t *testing.T) *rig {
	t.Helper()
	c := newRig(t)
	c.w.o.Exe = r.w.o.Exe
	c.opts = r.opts
	return c.withTmux(r.w.o.Canary)
}

func (r *rig) step(t *testing.T) (Ready, bool) {
	t.Helper()
	return r.w.step(context.Background())
}

func (r *rig) touch(t *testing.T) {
	t.Helper()
	fi, err := os.Stat(r.w.o.Exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(r.w.o.Exe, time.Time{}, fi.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) wantNone(t *testing.T) {
	t.Helper()
	if got, ok := r.step(t); ok {
		t.Fatalf("Ready %q, want none", got.Version)
	}
}

func envValue(env []string, key string) (string, int) {
	var v string
	n := 0
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); k == key {
			v, n = val, n+1
		}
	}
	return v, n
}

func TestReadyAfterTwoEqualReadsTwoSecondsApart(t *testing.T) {
	r := newRig(t)
	got, ok := r.step(t)
	if !ok || got.Version != newV {
		t.Fatalf("step = %q %v, want %q", got.Version, ok, newV)
	}
	if !slices.Equal(r.reads, []time.Duration{0, 2 * time.Second}) {
		t.Errorf("reads at %v, want [0s 2s]", r.reads)
	}
	if !slices.Equal(r.checks, []string{newV}) {
		t.Errorf("--version checks %v, want [%s]", r.checks, newV)
	}
	if !slices.Equal(r.sleeps, []time.Duration{2 * time.Second, 5 * time.Second}) {
		t.Errorf("sleeps %v, want settle 2s then stagger 5s", r.sleeps)
	}
}

func TestNoReadyWhenVersionUnchangedOrNotARelease(t *testing.T) {
	for _, v := range []string{oldV, "(devel)", "(devel) abc123", "v1.1.1-0.20261120202319-df3fd2bdd810", "v1.1.0+dirty", "v1.2.0-rc.1"} {
		t.Run(v, func(t *testing.T) {
			r := newRig(t)
			r.version = func(int) (string, error) { return v, nil }
			r.wantNone(t)
			if len(r.checks) != 0 {
				t.Errorf("--version checked %v", r.checks)
			}
		})
	}
}

func TestDevelEnvLiftsReleaseRule(t *testing.T) {
	r := newRig(t)
	r.env[envDevel] = "1"
	r.version = func(int) (string, error) { return "v1.1.1-0.20261120202319-df3fd2bdd810", nil }
	if _, ok := r.step(t); !ok {
		t.Fatal("no Ready with DEV_RELOAD_DEVEL=1")
	}
}

func TestHalfWrittenFile(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		r := newRig(t)
		r.version = func(int) (string, error) { return "", errors.New("not a Go executable") }
		r.wantNone(t)
		if len(r.warns) != 1 {
			t.Errorf("warns %v, want 1", r.warns)
		}
	})
	t.Run("second read fails", func(t *testing.T) {
		r := newRig(t)
		r.version = func(n int) (string, error) {
			if n == 0 {
				return newV, nil
			}
			return "", errors.New("truncated")
		}
		r.wantNone(t)
		if len(r.warns) != 1 {
			t.Errorf("warns %v, want 1", r.warns)
		}
	})
	t.Run("second read names another version", func(t *testing.T) {
		r := newRig(t)
		r.version = func(n int) (string, error) {
			if n == 0 {
				return newV, nil
			}
			return "v1.2.0", nil
		}
		r.wantNone(t)
		if len(r.checks) != 0 {
			t.Errorf("--version checked %v", r.checks)
		}
	})
	t.Run("changed between reads then settled", func(t *testing.T) {
		r := newRig(t)
		r.version = func(n int) (string, error) {
			if n == 0 {
				r.touch(t)
			}
			return newV, nil
		}
		r.wantNone(t)
		if len(r.checks) != 0 {
			t.Errorf("--version checked %v before the file settled", r.checks)
		}
		if _, ok := r.step(t); !ok {
			t.Fatal("no Ready once the file settled")
		}
	})
	t.Run("read error retried on the same file", func(t *testing.T) {
		r := newRig(t)
		r.version = func(int) (string, error) { return "", errors.New("too many open files") }
		r.wantNone(t)
		r.version = func(int) (string, error) { return newV, nil }
		if _, ok := r.step(t); !ok {
			t.Fatal("no Ready on the retry")
		}
	})
	t.Run("truncated real binary", func(t *testing.T) {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		half := filepath.Join(t.TempDir(), "half")
		if err := os.WriteFile(half, data[:len(data)/2], 0o755); err != nil {
			t.Fatal(err)
		}
		if v, err := fileVersion(half); err == nil {
			t.Fatalf("fileVersion(half) = %q, want an error", v)
		}
		if v, err := fileVersion(self); err != nil || v != Version() {
			t.Fatalf("fileVersion(self) = %q, %v, want %q", v, err, Version())
		}
	})
}

func TestLoopGuard(t *testing.T) {
	r := newRig(t)
	r.env[envReloadedTo] = newV
	r.wantNone(t)
}

func TestKillSwitch(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		r := newRig(t)
		r.env[envOff] = "1"
		r.wantNone(t)
	})
	t.Run("tmux option", func(t *testing.T) {
		r := newRig(t).withTmux(false)
		r.opts[optOff] = "off"
		r.wantNone(t)
		delete(r.opts, optOff)
		if _, ok := r.step(t); !ok {
			t.Fatal("no Ready with the kill switch cleared")
		}
	})
	t.Run("set during stagger", func(t *testing.T) {
		r := newRig(t).withTmux(false)
		r.w.stagger = func(time.Duration) time.Duration {
			r.opts[optOff] = "off"
			return time.Second
		}
		r.wantNone(t)
	})
	t.Run("no tmux server is not off", func(t *testing.T) {
		r := newRig(t).withTmux(false)
		r.tmuxErr = &tmux.Error{Stderr: "no server running on /tmp/x", Err: errors.New("exit status 1")}
		if _, ok := r.step(t); !ok {
			t.Fatal("no Ready without a tmux server")
		}
	})
	t.Run("tmux error is off", func(t *testing.T) {
		r := newRig(t).withTmux(false)
		r.tmuxErr = errors.New("boom")
		r.wantNone(t)
		if len(r.warns) == 0 {
			t.Error("tmux error not warned")
		}
	})
	t.Run("Exec", func(t *testing.T) {
		r := newRig(t).withTmux(false)
		ready, ok := r.step(t)
		if !ok {
			t.Fatal("no Ready")
		}
		r.opts[optOff] = "off"
		if err := Exec(ready, []string{"dev"}); !errors.Is(err, ErrNotNow) {
			t.Fatalf("Exec = %v, want ErrNotNow", err)
		}
		if len(r.execs) != 0 {
			t.Fatal("exec'd with the kill switch on")
		}
	})
}

func TestVersionCheckFailing(t *testing.T) {
	r := newRig(t)
	r.checkFn = func(string) error { return errors.New("exit status 2") }
	r.wantNone(t)
	r.wantNone(t)
	if len(r.checks) != 2 || len(r.warns) != 1 {
		t.Errorf("%d checks, warns %v: want a retry and the repeated warning once", len(r.checks), r.warns)
	}
	if len(r.warns) != 1 {
		t.Errorf("warns %v, want the check's error", r.warns)
	}
	r.checkFn = func(string) error { return nil }
	if _, ok := r.step(t); !ok { // a busy machine's timeout passes next poll
		t.Fatal("no Ready once the check passes")
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckVersion(t *testing.T) {
	ctx := context.Background()
	if err := checkVersion(ctx, script(t, "echo "+newV), newV); err != nil {
		t.Errorf("good binary: %v", err)
	}
	if err := checkVersion(ctx, script(t, "echo "+oldV), newV); err == nil || !strings.Contains(err.Error(), "printed") {
		t.Errorf("wrong version: %v", err)
	}
	if err := checkVersion(ctx, script(t, "echo "+newV+"; exit 1"), newV); err == nil {
		t.Error("exit 1 passed")
	}
	if err := checkVersion(ctx, script(t, "echo boom >&2; exit 3"), newV); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("stderr missing: %v", err)
	}
	defer func(d time.Duration) { checkTimeout = d }(checkTimeout)
	checkTimeout = 100 * time.Millisecond
	if err := checkVersion(ctx, script(t, "exec sleep 30"), newV); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v, want DeadlineExceeded", err)
	}
}

func TestCanary(t *testing.T) {
	t.Run("first decides alone, others wait for the ok mark", func(t *testing.T) {
		a := newRig(t).withTmux(true)
		b := a.clone(t)
		ra, ok := a.step(t)
		if !ok {
			t.Fatal("first pane: no Ready")
		}
		if err := Exec(ra, []string{"dev", "sidebar"}); err != nil {
			t.Fatal(err)
		}
		if v, _ := envValue(a.execs[0], envCanary); v != newV {
			t.Fatalf("canary exec %s=%q, want %q", envCanary, v, newV)
		}
		if a.opts[canaryOption(newV)] != newV {
			t.Fatalf("claim not set: %v", a.opts)
		}
		for range canaryOverdue - 1 {
			b.wantNone(t)
		}
		if len(b.warns) != 0 {
			t.Fatalf("warns %v while the canary is on time", b.warns)
		}
		b.wantNone(t)
		b.wantNone(t)
		if len(b.warns) != 1 || !strings.Contains(b.warns[0].Error(), "waiting for the canary") {
			t.Fatalf("warns %v, want one overdue canary wait", b.warns)
		}
		a.opts[optOK] = newV
		rb, ok := b.step(t)
		if !ok {
			t.Fatal("second pane: no Ready after the ok mark")
		}
		if err := Exec(rb, []string{"dev", "sidebar"}); err != nil {
			t.Fatal(err)
		}
		if _, n := envValue(b.execs[0], envCanary); n != 0 {
			t.Fatalf("second pane exec'd as a canary")
		}
	})
	t.Run("two Ready at once: one canary", func(t *testing.T) {
		a := newRig(t).withTmux(true)
		b := a.clone(t)
		ra, _ := a.step(t)
		rb, ok := b.step(t)
		if !ok {
			t.Fatal("second pane: no Ready before any claim")
		}
		if err := Exec(ra, []string{"dev"}); err != nil {
			t.Fatal(err)
		}
		if err := Exec(rb, []string{"dev"}); !errors.Is(err, ErrNotNow) {
			t.Fatalf("second Exec = %v, want ErrNotNow", err)
		}
		if len(b.execs) != 0 {
			t.Fatal("second pane exec'd while the canary runs")
		}
	})
	t.Run("failed canary exec drops its claim", func(t *testing.T) {
		a := newRig(t).withTmux(true)
		a.execErr = errors.New("exec format error")
		ra, _ := a.step(t)
		err := Exec(ra, []string{"dev"})
		if err == nil || !strings.Contains(err.Error(), "exec format error") {
			t.Fatalf("Exec = %v, want the exec error", err)
		}
		if _, set := a.opts[canaryOption(newV)]; set {
			t.Fatal("claim kept after a failed exec")
		}
	})
	t.Run("new canary process sets the ok mark after 30 s", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.w.o.Own = newV
		r.opts[canaryOption(newV)] = newV
		r.w.markOK(context.Background())
		if r.opts[optOK] != newV || !slices.Equal(r.sleeps, []time.Duration{30 * time.Second}) {
			t.Fatalf("ok mark %q after %v, want %q after 30s", r.opts[optOK], r.sleeps, newV)
		}
		if _, set := r.opts[canaryOption(newV)]; set {
			t.Fatal("claim kept after the ok mark: a rollback to this version would wait forever")
		}
	})
	t.Run("no claim: an old canary never moves the ok mark back", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.w.o.Own = oldV
		r.opts[optOK] = newV
		r.w.markOK(context.Background())
		if r.opts[optOK] != newV {
			t.Fatalf("ok mark %q, want %q kept", r.opts[optOK], newV)
		}
	})
	t.Run("ok mark retried after a tmux error", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.w.o.Own = newV
		r.opts[canaryOption(newV)] = newV
		fails := 1
		r.failOn = func(args []string) error {
			if args[0] == "set-option" && args[1] == "-g" && fails > 0 {
				fails--
				return errors.New("boom")
			}
			return nil
		}
		r.w.markOK(context.Background())
		if r.opts[optOK] != newV || !slices.Equal(r.sleeps, []time.Duration{30 * time.Second, pollEvery}) {
			t.Fatalf("ok mark %q after %v, want %q after 30s and one retry", r.opts[optOK], r.sleeps, newV)
		}
	})
	t.Run("file replaced during the claim", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		ready, _ := r.step(t)
		r.failOn = func(args []string) error {
			if args[0] == "set-option" && args[1] == "-go" {
				r.touch(t)
			}
			return nil
		}
		if err := Exec(ready, []string{"dev"}); !errors.Is(err, ErrNotNow) || len(r.execs) != 0 {
			t.Fatalf("Exec = %v (%d execs), want ErrNotNow and no exec", err, len(r.execs))
		}
		if _, set := r.opts[canaryOption(newV)]; set {
			t.Fatal("claim kept for a file that was never exec'd")
		}
	})
	t.Run("started by the canary's Watch", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.w.o.Own = newV
		r.env[envCanary] = newV
		r.opts[canaryOption(newV)] = newV
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r.w.watch(ctx)
		for deadline := time.Now().Add(5 * time.Second); ; {
			r.mu.Lock()
			ok := r.opts[optOK]
			r.mu.Unlock()
			if ok == newV {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no ok mark from the canary's Watch")
			}
			time.Sleep(time.Millisecond)
		}
	})
	t.Run("not a canary: no ok mark", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.w.o.Own = newV
		r.env[envCanary] = oldV
		ctx, cancel := context.WithCancel(context.Background())
		ch := r.w.watch(ctx)
		for {
			r.mu.Lock()
			n := len(r.sleeps)
			r.mu.Unlock()
			if n > 100 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		for range ch {
		}
		if _, set := r.opts[optOK]; set {
			t.Fatal("ok mark set by a pane that isn't the canary")
		}
	})
	t.Run("claimed during the stagger: no Ready", func(t *testing.T) {
		a := newRig(t).withTmux(true)
		b := a.clone(t)
		ra, _ := a.step(t)
		b.w.stagger = func(d time.Duration) time.Duration {
			if err := Exec(ra, []string{"dev"}); err != nil {
				t.Fatal(err)
			}
			return d
		}
		b.wantNone(t)
	})
	t.Run("tmux error holds the pane", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		r.failOn = func(args []string) error {
			if args[0] == "show-options" && args[2] == optOK {
				return errors.New("boom")
			}
			return nil
		}
		r.wantNone(t)
		if len(r.warns) != 1 {
			t.Errorf("warns %v, want the tmux error", r.warns)
		}
	})
	t.Run("claim error is not ErrNotNow", func(t *testing.T) {
		r := newRig(t).withTmux(true)
		ready, ok := r.step(t)
		if !ok {
			t.Fatal("no Ready")
		}
		r.failOn = func(args []string) error {
			if args[0] == "set-option" && args[1] == "-go" {
				return errors.New("boom")
			}
			return nil
		}
		err := Exec(ready, []string{"dev"})
		if err == nil || errors.Is(err, ErrNotNow) || len(r.execs) != 0 {
			t.Fatalf("Exec = %v (%d execs), want a claim error and no exec", err, len(r.execs))
		}
	})
	t.Run("serve skips the canary", func(t *testing.T) {
		a := newRig(t).withTmux(false)
		a.opts[canaryOption(newV)] = newV
		ra, ok := a.step(t)
		if !ok {
			t.Fatal("no Ready")
		}
		if err := Exec(ra, []string{"dev", "serve"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExecEnvAndError(t *testing.T) {
	r := newRig(t)
	ready, ok := r.step(t)
	if !ok {
		t.Fatal("no Ready")
	}
	t.Setenv(envReloadedTo, "v0.9.0")
	t.Setenv(envCanary, oldV)
	t.Setenv("DEV_STATE", "old")
	if err := Exec(ready, []string{"dev"}, "DEV_STATE={\"a\":\"b=c\"}", envReloadedTo+"="); err != nil {
		t.Fatal(err)
	}
	if v, n := envValue(r.execs[0], "DEV_STATE"); v != `{"a":"b=c"}` || n != 1 {
		t.Errorf("DEV_STATE = %q (%d entries), want the one set", v, n)
	}
	if v, n := envValue(r.execs[0], envReloadedTo); v != newV || n != 1 {
		t.Errorf("%s = %q (%d entries), want %q once", envReloadedTo, v, n, newV)
	}
	if _, n := envValue(r.execs[0], envCanary); n != 0 {
		t.Errorf("%s kept from the old process", envCanary)
	}
	r.execErr = errors.New("permission denied")
	if err := Exec(ready, []string{"dev"}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Exec = %v, want the exec error", err)
	}
}

func TestExecFileChangedSinceReady(t *testing.T) {
	r := newRig(t)
	ready, ok := r.step(t)
	if !ok {
		t.Fatal("no Ready")
	}
	r.touch(t)
	if err := Exec(ready, []string{"dev"}); !errors.Is(err, ErrNotNow) || len(r.execs) != 0 {
		t.Fatalf("Exec = %v (%d execs), want ErrNotNow and no exec", err, len(r.execs))
	}
}

func TestExecEnvKillSwitch(t *testing.T) {
	r := newRig(t)
	ready, _ := r.step(t)
	r.env[envOff] = "1"
	if err := Exec(ready, []string{"dev"}); !errors.Is(err, ErrNotNow) || len(r.execs) != 0 {
		t.Fatalf("Exec = %v (%d execs), want ErrNotNow and no exec", err, len(r.execs))
	}
}

func TestExecRealSyscallErrorReturns(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(exe, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newWatcher(Options{Exe: exe, Own: oldV})
	w.getenv = func(string) string { return "" }
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	w.seen = fi
	if err := Exec(Ready{Version: newV, w: w}, []string{exe}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Exec = %v, want EACCES", err)
	}
}

func TestWatchSendsOneReadyThenCloses(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := r.w.watch(ctx)
	got, ok := <-ch
	if !ok || got.Version != newV {
		t.Fatalf("Watch sent %q %v, want %q", got.Version, ok, newV)
	}
	if r.sleeps[0] != pollEvery {
		t.Errorf("first wait %v, want %v", r.sleeps[0], pollEvery)
	}
	if _, open := <-ch; open {
		t.Fatal("channel open after Ready")
	}
}

func TestWatchClosesOnCancel(t *testing.T) {
	r := newRig(t)
	r.env[envOff] = "1"
	ctx, cancel := context.WithCancel(context.Background())
	ch := r.w.watch(ctx)
	cancel()
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("Ready with the kill switch on")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

// Keep is open across the exec only: close-on-exec cleared for the execve, set again after it fails.
func TestExecKeep(t *testing.T) {
	r := newRig(t)
	ready, ok := r.step(t)
	if !ok {
		t.Fatal("no Ready")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "alive"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cloexec := func() bool {
		fl, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		return fl&unix.FD_CLOEXEC != 0
	}
	ready.w.o.Keep = f
	ready.w.execve = func(_ string, _ []string, env []string) error {
		if cloexec() {
			t.Error("close-on-exec still set at the exec")
		}
		if v, _ := envValue(env, EnvKept); v != fmt.Sprint(f.Fd()) {
			t.Errorf("%s = %q, want %d", EnvKept, v, f.Fd())
		}
		return errors.New("permission denied")
	}
	if err := Exec(ready, []string{"dev"}); err == nil {
		t.Fatal("no error")
	}
	if !cloexec() {
		t.Error("close-on-exec not set again after a failed exec")
	}
}
