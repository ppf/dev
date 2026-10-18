package ptywrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// drainIdle and drainMax end the reading of the command's last output: its exit doesn't close the pty
// while a process it left behind holds it, and that process may keep writing.
const (
	drainIdle = 50 * time.Millisecond
	drainMax  = 500 * time.Millisecond
)

// Run runs argv like exec would, wrapping its graphics APCs when inside tmux with a terminal for stdin and
// stdout; elsewhere it execs argv in place. Once argv runs, Run exits with its status and never returns.
//
// The command keeps the terminal's process group, session and controlling terminal (its /dev/tty), so
// job control and signals sent to the group reach it as before; only its stdio is the new pty. Run
// copies stdin to it raw, resizes it with the terminal and forwards the HUP, INT, QUIT and TERM it gets
// (one sent to the whole group reaches the command twice). When the command stops (Claude's Ctrl-Z), Run
// stops too, so the shell gets the terminal back.
func Run(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	in := int(os.Stdin.Fd())
	if os.Getenv("TMUX") == "" || !term.IsTerminal(in) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return syscall.Exec(path, argv, os.Environ())
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		return err
	}
	if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
		return err
	}
	// Before the start, so no resize or signal is missed; caught signals are reset in the command
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	modes, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	// The command starts with the terminal's own modes, and switches them itself (Claude goes raw)
	err = term.Restore(int(tty.Fd()), modes)
	var proc *os.Process
	if err == nil {
		proc, err = os.StartProcess(path, argv, &os.ProcAttr{Env: os.Environ(), Files: []*os.File{tty, tty, tty}})
	}
	if err != nil {
		_ = term.Restore(in, modes)
		return err
	}

	out := make(chan []byte)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				out <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(out)
				return
			}
		}
	}()
	go copyInput(ptmx, tty, proc)
	status := make(chan syscall.WaitStatus)
	go func() {
		for {
			var ws syscall.WaitStatus
			if _, err := syscall.Wait4(proc.Pid, &ws, syscall.WUNTRACED, nil); errors.Is(err, syscall.EINTR) {
				continue
			} else if err != nil {
				// Only a pid that isn't Run's child fails, and proc is
				_ = term.Restore(in, modes)
				panic(err)
			}
			status <- ws
			if !ws.Stopped() {
				return
			}
		}
	}()

	var o output
	for {
		select {
		case b, ok := <-out:
			if !ok {
				out = nil
				continue
			}
			o.write(b)
		case sig := <-sigs:
			switch sig {
			case syscall.SIGWINCH:
				// The terminal's resize signals the command too, but the pty's size changes only now
				_ = pty.InheritSize(os.Stdin, ptmx)
				_ = proc.Signal(sig)
			case syscall.SIGTSTP:
				// Sent to the group, the command stops itself, which status reports; one sent to Run alone is dropped
			default:
				_ = proc.Signal(sig)
			}
		case ws := <-status:
			o.drain(out)
			_ = term.Restore(in, modes)
			if ws.Stopped() {
				_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
				// Continued (fg): take the terminal back, the size may have changed meanwhile
				_, _ = term.MakeRaw(in)
				_ = pty.InheritSize(os.Stdin, ptmx)
				_ = proc.Signal(syscall.SIGCONT)
				_ = proc.Signal(syscall.SIGWINCH)
				continue
			}
			if ws.Signaled() {
				os.Exit(128 + int(ws.Signal()))
			}
			os.Exit(ws.ExitStatus())
		}
	}
}

// output writes the command's output, filtered, to stdout as it comes.
type output struct {
	f   filter
	buf []byte
}

func (o *output) write(b []byte) {
	o.buf = o.f.Write(o.buf[:0], b)
	o.emit()
}

func (o *output) flushHeld() {
	o.buf = o.f.Flush(o.buf[:0])
	o.emit()
}

// emit drops write errors: a terminal that went away must not stop the command's output from being read.
func (o *output) emit() {
	if len(o.buf) > 0 {
		_, _ = os.Stdout.Write(o.buf)
	}
}

// drain writes the output the command wrote before it stopped or exited.
func (o *output) drain(out <-chan []byte) {
	deadline := time.After(drainMax)
	for out != nil {
		select {
		case b, ok := <-out:
			if !ok {
				out = nil
				continue
			}
			o.write(b)
		case <-time.After(drainIdle):
			out = nil
		case <-deadline:
			out = nil
		}
	}
	o.flushHeld()
}

// keys are the control characters a terminal with ISIG on turns into signals.
var keys = []struct {
	cc  int
	sig syscall.Signal
}{{unix.VINTR, syscall.SIGINT}, {unix.VQUIT, syscall.SIGQUIT}, {unix.VSUSP, syscall.SIGTSTP}}

// copyInput copies stdin to the pty as it comes. The pty is no process's controlling terminal, so its
// line discipline signals no one: while the command reads it with ISIG on (cooked, as Claude does before
// it goes raw), copyInput sends the command the signal of its Ctrl-C, Ctrl-\ or Ctrl-Z itself.
func copyInput(ptmx, tty *os.File, proc *os.Process) {
	buf := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if t, terr := unix.IoctlGetTermios(int(tty.Fd()), getTermios); terr == nil && t.Lflag&unix.ISIG != 0 {
				for _, b := range buf[:n] {
					for _, k := range keys {
						// A disabled key is _POSIX_VDISABLE: 0xff on macOS, 0 on Linux
						if c := t.Cc[k.cc]; c == b && c != 0 && c != 0xff {
							_ = proc.Signal(k.sig)
						}
					}
				}
			}
			if _, err := ptmx.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Argv is argv run under this executable's `dev pty-wrap`.
func Argv(argv ...string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("ptywrap: %w", err)
	}
	return append([]string{exe, "pty-wrap", "--"}, argv...), nil
}
