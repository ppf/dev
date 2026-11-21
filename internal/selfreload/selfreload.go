// Package selfreload lets a long-running dev process (dev serve, a sidebar, a boss's previews pane)
// re-exec itself in place (same pid, same tmux pane) once a new release of its binary is installed.
//
//   - Watch(ctx, Options) <-chan Ready: checks Options.Exe every 10 s and sends one Ready when the
//     file holds another release, then closes (also on ctx done). Ready needs: the file's version
//     (debug/buildinfo, no exec) differs from Options.Own, and reads the same twice 2 s apart with an
//     unchanged inode, size and mtime; it is a plain release tag vX.Y.Z (a local build stamps a
//     pseudo-version, +dirty or (devel); only a clean build at a tag commit stamps the tag); it isn't
//     the version this process was exec'd into (DEV_RELOADED_TO, the loop guard); `<exe> --version`
//     exits 0 within 5 s and prints it; the kill switch is off. Then it waits a random 0 to
//     Options.Stagger. A failed check is retried next poll; a repeated warning is passed on once.
//   - Exec(Ready, args, set...) error: re-checks the kill switch, that Exe is still the file checked,
//     and the canary, then syscall.Exec(exe, args) with DEV_RELOADED_TO=<version> and set's KEY=VALUE
//     entries (set, not appended). It returns only on failure: the caller keeps running the old version
//     and says so; it must never exit to reload. Watch's channel is closed by then: after any Exec
//     error, call Watch again to keep watching (ErrNotNow: refused without trying).
//   - Kill switch: tmux global option @dev_reload off (read on every check and in Exec), or the
//     env DEV_NO_RELOAD=1. A missing tmux server isn't "off"; any other tmux error is.
//   - Canary (Options.Canary with Options.Tmux, panes only; ignored without Tmux): any pane may get
//     Ready until one claims the version; the first to Exec claims it (set-option -go
//     @dev_reload_canary_<version>, atomic) and reloads alone with DEV_RELOAD_CANARY=<version>, and the
//     others' Exec returns ErrNotNow. Watching again, they get Ready only once @dev_reload_ok names the
//     version, which the canary's new process sets when its Watch (Canary on) has run 30 s, dropping
//     its claim (a later rollback gets a canary again). A canary whose exec fails drops its claim.
//     One that dies later holds every other pane on the old version (warned after 60 s): unset
//     @dev_reload_canary_<version> or set @dev_reload_ok to move on.
//   - DEV_RELOAD_DEVEL=1 (tests, manual swap runs) lifts the release-tag rule, so two local builds swap.
//
// Options.Exe is the path captured once at start (os.Executable), never resolved again: on Linux
// /proc/self/exe names the old, deleted file once a release replaces it.
package selfreload

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

const (
	envReloadedTo = "DEV_RELOADED_TO"
	envCanary     = "DEV_RELOAD_CANARY"
	envOff        = "DEV_NO_RELOAD"
	envDevel      = "DEV_RELOAD_DEVEL"
	optOff        = "@dev_reload"
	optOK         = "@dev_reload_ok"
	optCanary     = "@dev_reload_canary_"

	pollEvery   = 10 * time.Second
	settleAfter = 2 * time.Second
	canaryOKAt  = 30 * time.Second
	// canaryOverdue is the polls held by another pane's canary before it is warned: twice canaryOKAt
	canaryOverdue = int(2 * canaryOKAt / pollEvery)
)

// checkTimeout bounds `<exe> --version` (a var for tests).
var checkTimeout = 5 * time.Second

// ErrNotNow is Exec refusing without trying: the kill switch is on or another pane is the canary.
var ErrNotNow = errors.New("selfreload: not now")

var (
	releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	optUnsafe  = regexp.MustCompile(`[^A-Za-z0-9]`)
)

// Options says what Watch watches and how the process takes part in the canary.
type Options struct {
	Exe     string        // binary path captured once at start
	Own     string        // this process's Version()
	Tmux    *tmux.Client  // server holding the kill switch and canary options; nil: env kill switch only
	Canary  bool          // take part in the canary (panes; not serve); needs Tmux
	Stagger time.Duration // longest random wait before Ready
	Warn    func(error)   // a failed check, called from Watch's goroutines (maybe at once); nil drops it
}

// Ready says Exe now holds Version and this process may reload into it.
type Ready struct {
	Version string
	w       *watcher
}

type watcher struct {
	o       Options
	after   func(time.Duration) <-chan time.Time
	stat    func(string) (os.FileInfo, error)
	read    func(string) (string, error)
	check   func(ctx context.Context, exe, want string) error
	getenv  func(string) string
	execve  func(path string, args, env []string) error
	stagger func(time.Duration) time.Duration

	seen    os.FileInfo // the file as last decided on; nil to decide again
	target  string      // the version decided on; "" while none
	waitFor string      // the version held by another pane's canary
	waited  int         // polls held for waitFor

	mu     sync.Mutex // warned: markOK and the watch loop warn at once
	warned string     // the last warning
}

func newWatcher(o Options) *watcher {
	return &watcher{
		o:      o,
		after:  time.After,
		stat:   os.Stat,
		read:   fileVersion,
		check:  checkVersion,
		getenv: os.Getenv,
		execve: syscall.Exec,
		stagger: func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return rand.N(d + 1)
		},
	}
}

// Watch watches o.Exe until ctx is done and sends at most one Ready; the channel is closed after it.
func Watch(ctx context.Context, o Options) <-chan Ready {
	return newWatcher(o).watch(ctx)
}

func (w *watcher) watch(ctx context.Context) <-chan Ready {
	out := make(chan Ready, 1)
	if w.o.Canary && w.o.Tmux != nil && w.getenv(envCanary) == w.o.Own {
		go w.markOK(ctx)
	}
	go func() {
		defer close(out)
		for w.sleep(ctx, pollEvery) {
			if r, ok := w.step(ctx); ok {
				out <- r
				return
			}
		}
	}()
	return out
}

// markOK sets @dev_reload_ok=Own after canaryOKAt, then drops the claim so a rollback gets a canary again;
// retried each poll. No claim: no-op, so a later Watch never moves the ok mark back from a newer version.
func (w *watcher) markOK(ctx context.Context) {
	for d := canaryOKAt; w.sleep(ctx, d); d = pollEvery {
		claim, err := w.option(canaryOption(w.o.Own))
		if err == nil && claim == "" {
			return
		}
		if err == nil {
			_, err = w.o.Tmux.Run("set-option", "-g", optOK, w.o.Own)
		}
		if err == nil {
			_, err = w.o.Tmux.Run("set-option", "-gu", canaryOption(w.o.Own))
		}
		if err == nil {
			return
		}
		w.warn(fmt.Errorf("selfreload: canary ok mark: %w", err))
	}
}

// step is one check: Ready when this process may reload now.
func (w *watcher) step(ctx context.Context) (Ready, bool) {
	if w.isOff() {
		return Ready{}, false
	}
	fi, err := w.stat(w.o.Exe)
	if err != nil {
		w.warn(fmt.Errorf("selfreload: %w", err))
		return Ready{}, false
	}
	if w.seen == nil || !sameFile(w.seen, fi) {
		w.seen = fi
		w.target, err = w.decide(ctx, fi)
		if err != nil {
			w.seen = nil
			if ctx.Err() == nil {
				w.warn(err)
			}
		}
	}
	if w.target == "" || !w.mayGo(w.target) {
		return Ready{}, false
	}
	// Checked again after the stagger: another pane may have claimed the canary meanwhile
	if !w.sleep(ctx, w.stagger(w.o.Stagger)) || w.isOff() || ctx.Err() != nil || !w.mayGo(w.target) {
		return Ready{}, false
	}
	return Ready{Version: w.target, w: w}, true
}

// decide returns the version Exe settled on when it is one to reload into, else ""; an error is
// worth a retry on the same file.
func (w *watcher) decide(ctx context.Context, fi os.FileInfo) (string, error) {
	v, err := w.read(w.o.Exe)
	if err != nil {
		return "", fmt.Errorf("selfreload: %w", err)
	}
	if v == w.o.Own || v == w.getenv(envReloadedTo) {
		return "", nil
	}
	if !releaseTag.MatchString(v) && w.getenv(envDevel) != "1" {
		return "", nil
	}
	if !w.sleep(ctx, settleAfter) {
		return "", ctx.Err()
	}
	fi2, err := w.stat(w.o.Exe)
	if err != nil {
		return "", fmt.Errorf("selfreload: %w", err)
	}
	if !sameFile(fi, fi2) {
		return "", nil // still being written: the next poll sees another file
	}
	v2, err := w.read(w.o.Exe)
	if err != nil {
		return "", fmt.Errorf("selfreload: %w", err)
	}
	if v2 != v {
		return "", fmt.Errorf("selfreload: %s changed from %s to %s while settling", w.o.Exe, v, v2)
	}
	if err := w.check(ctx, w.o.Exe, v); err != nil {
		return "", err
	}
	return v, nil
}

// mayGo applies the canary: go when not taking part, when the ok mark names v, or while no pane claimed v.
func (w *watcher) mayGo(v string) bool {
	if !w.o.Canary || w.o.Tmux == nil {
		return true
	}
	ok, err := w.option(optOK)
	if err != nil {
		w.warn(err)
		return false
	}
	if ok == v {
		return true
	}
	claim, err := w.option(canaryOption(v))
	if err != nil {
		w.warn(err)
		return false
	}
	if claim == "" {
		return true
	}
	if w.waitFor != v {
		w.waitFor, w.waited = v, 0
	}
	if w.waited++; w.waited == canaryOverdue {
		w.warn(fmt.Errorf("selfreload: waiting for the canary of %s (%s set, %s not yet)", v, canaryOption(v), optOK))
	}
	return false
}

// isOff is the kill switch for Watch: off warns its error and counts as on.
func (w *watcher) isOff() bool {
	on, err := w.off()
	if err != nil {
		w.warn(err)
	}
	return on
}

// off is the kill switch; a tmux error other than no server counts as on.
func (w *watcher) off() (bool, error) {
	if w.getenv(envOff) == "1" {
		return true, nil
	}
	if w.o.Tmux == nil {
		return false, nil
	}
	v, err := w.option(optOff)
	if err != nil {
		return true, err
	}
	return v == "off", nil
}

// option reads a tmux global option ("" when unset or no server runs).
func (w *watcher) option(name string) (string, error) {
	out, err := w.o.Tmux.Run("show-options", "-gqv", name)
	if tmux.IsNoServer(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("selfreload: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (w *watcher) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-w.after(d):
		return true
	}
}

// warn passes err to Options.Warn unless it repeats the last one.
func (w *watcher) warn(err error) {
	w.mu.Lock()
	repeat := err.Error() == w.warned
	w.warned = err.Error()
	w.mu.Unlock()
	if w.o.Warn != nil && !repeat {
		w.o.Warn(err)
	}
}

// Exec replaces this process with r's binary run with args (args[0] included) and set's KEY=VALUE entries in
// its environment; it returns only on failure.
func Exec(r Ready, args []string, set ...string) error {
	w := r.w
	if w == nil {
		return errors.New("selfreload: Exec without a Ready from Watch")
	}
	if on, err := w.off(); on {
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNotNow, err)
		}
		return ErrNotNow
	}
	claimed := false
	if w.o.Canary && w.o.Tmux != nil {
		ok, err := w.option(optOK)
		if err != nil {
			return err
		}
		if ok != r.Version {
			if _, err := w.o.Tmux.Run("set-option", "-go", canaryOption(r.Version), r.Version); err != nil {
				// -o fails when another pane claimed first; read back so tmux's wording doesn't matter
				if claim, rerr := w.option(canaryOption(r.Version)); rerr == nil && claim != "" {
					return ErrNotNow
				}
				return fmt.Errorf("selfreload: canary claim: %w", err)
			}
			claimed = true
		}
	}
	// Checked after the claim: an install during it must not run unchecked
	if fi, err := w.stat(w.o.Exe); err != nil || !sameFile(w.seen, fi) {
		return w.dropClaim(claimed, r.Version, fmt.Errorf("%w: %s changed since it was checked", ErrNotNow, w.o.Exe))
	}
	canary := ""
	if claimed {
		canary = r.Version
	}
	env := setEnv(os.Environ(), envReloadedTo, r.Version)
	env = setEnv(env, envCanary, canary)
	for _, kv := range set {
		k, v, _ := strings.Cut(kv, "=")
		env = setEnv(env, k, v)
	}
	err := w.execve(w.o.Exe, args, env)
	if err == nil { // only a test double returns from a successful exec
		return nil
	}
	return w.dropClaim(claimed, r.Version, fmt.Errorf("selfreload: exec %s: %w", w.o.Exe, err))
}

// dropClaim returns err, after unsetting version's canary claim when this process made it.
func (w *watcher) dropClaim(claimed bool, version string, err error) error {
	if !claimed {
		return err
	}
	if _, uerr := w.o.Tmux.Run("set-option", "-gu", canaryOption(version)); uerr != nil {
		return errors.Join(err, fmt.Errorf("selfreload: drop canary claim: %w", uerr))
	}
	return err
}

// setEnv returns env with key=value in place of any key entry; an empty value drops it.
func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	if value != "" {
		out = append(out, key+"="+value)
	}
	return out
}

func canaryOption(version string) string {
	return optCanary + optUnsafe.ReplaceAllString(version, "_")
}

// sameFile: same inode, size and mtime.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Version is this binary's version as `dev --version` prints it.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return format(info)
}

// fileVersion reads the version of the binary at path without running it.
func fileVersion(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	return format(info), nil
}

// format is the module version; a "(devel)" build adds the vcs revision when stamped.
func format(info *debug.BuildInfo) string {
	v := info.Main.Version
	if v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return v + " " + s.Value
		}
	}
	return v
}

// checkVersion runs `exe --version`: it must exit 0 within checkTimeout and print want.
func checkVersion(ctx context.Context, exe, want string) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("selfreload: %s --version: %w", exe, ctx.Err())
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && len(ee.Stderr) > 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n")
		return fmt.Errorf("selfreload: %s --version: %w: %s", exe, err, first)
	}
	if err != nil {
		return fmt.Errorf("selfreload: %s --version: %w", exe, err)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		return fmt.Errorf("selfreload: %s --version printed %q, want %q", exe, got, want)
	}
	return nil
}
