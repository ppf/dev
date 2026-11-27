// Package tmux runs tmux commands and parses their formatted output.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ErrNoSocket is returned when a test binary tries to reach the user's default tmux server.
var ErrNoSocket = errors.New("tmux: socket override (-L) required under test")

// Error is a failed tmux invocation with its full argv and stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("tmux %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// IsNoServer reports a failed tmux call to a server that isn't running (no socket, or nothing listening).
func IsNoServer(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && (strings.Contains(e.Stderr, "no server running") ||
		strings.Contains(e.Stderr, "error connecting") && strings.Contains(e.Stderr, "No such file or directory"))
}

// IsNoSession reports a failed tmux call needing a current target on a server with no session (one still reading
// its config, or kept up by exit-empty off): list-sessions answers it empty, list-panes -a fails.
func IsNoSession(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && strings.Contains(e.Stderr, "no current target")
}

type runner func(args, env []string) (stdout, stderr []byte, err error)

// Client runs tmux commands against one server.
type Client struct {
	socket string
	config string
	env    []string
	run    runner
	fake   bool // run is a test double: no tmux process starts, so no socket is needed
}

// New returns a client for the server tmux itself picks (TMUX env, else default socket).
func New() *Client {
	return &Client{run: execTmux}
}

// NewIsolated returns a client for the server on socket name (-L), started with config (-f) if non-empty.
// Its child processes never inherit TMUX or TMUX_PANE, so nothing they spawn reaches another server;
// env entries are added on top.
func NewIsolated(socket, config string, env ...string) *Client {
	return &Client{socket: socket, config: config, env: append(environWithoutTmux(), env...), run: execTmux}
}

// NewFake returns a client whose commands go to run instead of tmux, for unit tests in other packages.
// run gets the command's arguments and returns its stdout or an error; an *Error it returns stands for
// tmux printing its Stderr and failing with its Err. Interactive is refused under test.
func NewFake(run func(args []string) (string, error)) *Client {
	return &Client{fake: true, run: func(args, _ []string) ([]byte, []byte, error) {
		out, err := run(args)
		if e, ok := errors.AsType[*Error](err); ok {
			return []byte(out), []byte(e.Stderr), e.Err
		}
		return []byte(out), nil, err
	}}
}

// Env returns the environment of an isolated client's child processes (nil for New: they inherit).
func (c *Client) Env() []string {
	return slices.Clone(c.env)
}

// Run executes one tmux command (or a ";"-chained list) and returns its stdout.
func (c *Client) Run(args ...string) (string, error) {
	if c.socket == "" && !c.fake && testing.Testing() {
		return "", ErrNoSocket
	}
	argv := c.argv(args)
	stdout, stderr, err := c.run(argv, c.env)
	if err != nil {
		return "", &Error{Args: argv, Stderr: strings.TrimSpace(string(stderr)), Err: err}
	}
	return string(stdout), nil
}

func (c *Client) argv(args []string) []string {
	var argv []string
	if c.socket != "" {
		argv = append(argv, "-L", c.socket)
	}
	if c.config != "" {
		argv = append(argv, "-f", c.config)
	}
	return append(argv, args...)
}

// SessionTarget returns an exact-match target for session name, so "dev" never matches "dev-2".
func SessionTarget(name string) string {
	return "=" + name + ":"
}

func execTmux(args, env []string) ([]byte, []byte, error) {
	// -u: in a non-UTF-8 locale outside tmux the client prints control characters (Query's \x1f) as "_"
	cmd := exec.Command("tmux", append([]string{"-u"}, args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func environWithoutTmux() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	return env
}

// startSettle is how long Start waits for tmux to fail fast (a client gone, a popup refused).
const startSettle = 300 * time.Millisecond

// Start runs a tmux command (a display-popup, blocking until closed) detached, holding no caller pipe open: its
// stderr goes to a file, read when it fails within startSettle; past that it counts as started. A fake runs it at once.
func (c *Client) Start(args ...string) error {
	switch {
	case c.fake:
		_, err := c.Run(args...)
		return err
	case c.socket == "" && testing.Testing():
		return ErrNoSocket
	}
	argv := c.argv(args)
	stderr, err := os.CreateTemp("", "dev-tmux-start-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stderr.Name()) }()
	defer func() { _ = stderr.Close() }()
	cmd := exec.Command("tmux", append([]string{"-u"}, argv...)...)
	cmd.Env, cmd.Stderr = c.env, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return &Error{Args: argv, Err: err}
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			msg, readErr := os.ReadFile(stderr.Name())
			return &Error{Args: argv, Stderr: strings.TrimSpace(string(msg)), Err: errors.Join(err, readErr)}
		}
	case <-time.After(startSettle):
	}
	return nil
}

// Interactive runs a tmux command attached to this process's terminal (attach-session, popups).
func (c *Client) Interactive(args ...string) error {
	if c.socket == "" && testing.Testing() {
		return ErrNoSocket
	}
	argv := c.argv(args)
	cmd := exec.Command("tmux", argv...)
	cmd.Env = c.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return &Error{Args: argv, Err: err}
	}
	return nil
}
