package ptywrap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// The test binary doubles as the wrapper (wrap <argv>) and as the command it runs (child <mode>).
func TestMain(m *testing.M) {
	if len(os.Args) > 2 {
		switch os.Args[1] {
		case "wrap":
			err := Run(os.Args[2:])
			fmt.Fprintln(os.Stderr, "Run:", err)
			os.Exit(99)
		case "child":
			child(os.Args[2])
			return
		}
	}
	os.Exit(m.Run())
}

func size() string {
	rows, cols, err := pty.Getsize(os.Stdout)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%dx%d", cols, rows)
}

// child is the wrapped command: it reports what it sees, as Claude would.
func child(mode string) {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM)
	fmt.Printf("tty=%v,%v TERM=%s size=%s\n", term.IsTerminal(0), term.IsTerminal(1), os.Getenv("TERM"), size())
	switch mode {
	case "echo":
		if _, err := term.MakeRaw(0); err != nil {
			panic(err)
		}
		go func() {
			for s := range sigs {
				if s == syscall.SIGTERM {
					fmt.Print("got TERM\r\n")
					os.Exit(3)
				}
				fmt.Printf("resized size=%s\r\n", size())
			}
		}()
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				os.Exit(2)
			}
			fmt.Printf("in=%q\r\n", buf[:n])
			if bytes.Contains(buf[:n], []byte("q")) {
				// An image split across writes, then exit with a status of its own
				fmt.Print("img:\x1b_Ga=T,U=1;QU")
				time.Sleep(20 * time.Millisecond)
				fmt.Print("JD\x1b")
				time.Sleep(20 * time.Millisecond)
				fmt.Print("\\:end\r\n")
				os.Exit(7)
			}
		}
	case "kill":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {} // the signal may land after Kill returns
	case "suspend":
		// Claude's Ctrl-Z: release the terminal, stop the whole process group, take it back on SIGCONT
		cont := make(chan os.Signal, 1)
		signal.Notify(cont, syscall.SIGCONT)
		fmt.Print("suspending\n")
		_ = syscall.Kill(0, syscall.SIGTSTP)
		<-cont // Kill may return before the stop lands (Linux delivers it to any thread)
		fmt.Print("resumed\n")
		os.Exit(5)
	case "cooked":
		// Claude before it goes raw: Ctrl-C must still interrupt it
		ints := make(chan os.Signal, 1)
		signal.Notify(ints, syscall.SIGINT)
		<-ints
		fmt.Print("got INT\n")
		os.Exit(4)
	case "orphan":
		// A process left behind (a background tool) keeps the pty open, and writing
		sleep := exec.Command("sh", "-c", "while :; do echo tick; sleep 0.01; done")
		sleep.Stdout = os.Stdout
		if err := sleep.Start(); err != nil {
			panic(err)
		}
		fmt.Printf("orphan=%d;\n", sleep.Process.Pid)
		os.Exit(4)
	case "pid":
		fmt.Printf("pid=%d;\n", os.Getpid())
		select {}
	case "image":
		fmt.Print("\x1b_Gi=1\x1b\\")
		os.Exit(7)
	}
}

// terminal is the outer pty, standing in for a tmux pane.
type terminal struct {
	ptmx *os.File
	mu   sync.Mutex
	out  bytes.Buffer
}

// start runs argv as a tmux pane does: a new session on a fresh pty of 100x30.
func start(t *testing.T, env []string, argv ...string) (*terminal, *exec.Cmd) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = tty.Close()
	tm := &terminal{ptmx: ptmx}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			tm.mu.Lock()
			tm.out.Write(buf[:n])
			tm.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
	})
	return tm, cmd
}

func (tm *terminal) String() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.out.String()
}

// waitFor waits until the terminal shows s.
func (tm *terminal) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(tm.String(), s) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal never showed %q; got %q", s, tm.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func exitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	err := cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode()
}

func wrapper(mode string) []string {
	return []string{os.Args[0], "wrap", os.Args[0], "child", mode}
}

var inTmux = []string{"TMUX=/tmp/tmux-test,1,0", "TERM=tmux-256color"}

func TestRunIsTransparent(t *testing.T) {
	tm, cmd := start(t, inTmux, wrapper("echo")...)
	tm.waitFor(t, "tty=true,true TERM=tmux-256color size=100x30\r\n")

	if err := pty.Setsize(tm.ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, "resized size=120x40\r\n")

	// Raw: Ctrl-C, Ctrl-Z, ESC and a CR reach the command as bytes, nothing is echoed twice
	if _, err := tm.ptmx.Write([]byte("a\x03\x1a\x1b[A\r")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, `in="a\x03\x1a\x1b[A\r"`)
	if _, err := tm.ptmx.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, ":end\r\n")
	if got := exitCode(t, cmd); got != 7 {
		t.Errorf("exit code %d, want 7", got)
	}
	if want := "img:\x1bPtmux;\x1b\x1b_Ga=T,U=1;QUJD\x1b\x1b\\\x1b\\:end"; !strings.Contains(tm.String(), want) {
		t.Errorf("image not wrapped: got %q", tm.String())
	}
	if strings.Contains(tm.String(), "a\x03") {
		t.Errorf("input echoed: %q", tm.String())
	}
}

func TestRunOutsideTmuxExecs(t *testing.T) {
	tm, cmd := start(t, []string{"TMUX=", "TERM=xterm-ghostty"}, wrapper("echo")...)
	tm.waitFor(t, "tty=true,true TERM=xterm-ghostty size=100x30\r\n")
	// Exec'd in place: the pane's own process is the command, no pty in between
	if _, err := tm.ptmx.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, ":end\r\n")
	if got := exitCode(t, cmd); got != 7 {
		t.Errorf("exit code %d, want 7", got)
	}
	if want := "img:\x1b_Ga=T,U=1;QUJD\x1b\\:end"; !strings.Contains(tm.String(), want) {
		t.Errorf("output changed outside tmux: got %q", tm.String())
	}
}

func TestRunForwardsSignals(t *testing.T) {
	tm, cmd := start(t, inTmux, wrapper("echo")...)
	tm.waitFor(t, "size=100x30")
	// Sent to the wrapper alone, as `kill <pid>` does
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, "got TERM")
	if got := exitCode(t, cmd); got != 3 {
		t.Errorf("exit code %d, want 3", got)
	}
}

func TestRunKilledCommand(t *testing.T) {
	tm, cmd := start(t, inTmux, wrapper("kill")...)
	tm.waitFor(t, "size=100x30")
	if got := exitCode(t, cmd); got != 128+int(syscall.SIGKILL) {
		t.Errorf("exit code %d, want %d", got, 128+int(syscall.SIGKILL))
	}
}

// Ctrl-Z under a job-control shell: the shell gets the terminal back in its own modes, fg resumes.
func TestRunSuspends(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	// Job control needs an interactive shell, even with set -m
	script := `"$@"; echo "stopped=$?"; stty -a | grep -o -- '-\{0,1\}icanon' | head -n 1; fg; echo "rc=$?"`
	tm, cmd := start(t, inTmux, append([]string{bash, "--norc", "--noprofile", "-i", "-c", script, "bash"}, wrapper("suspend")...)...)
	tm.waitFor(t, "rc=")
	if got := exitCode(t, cmd); got != 0 {
		t.Errorf("bash exit code %d", got)
	}
	out := tm.String()
	stop := fmt.Sprintf("stopped=%d", 128+int(syscall.SIGSTOP))
	for _, want := range []string{"suspending", stop, "\nicanon", "resumed", "rc=5"} {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("missing %q in order; got %q", want, tm.String())
		}
		out = out[i+len(want):]
	}
}

func TestRunCookedKeysSignal(t *testing.T) {
	tm, cmd := start(t, inTmux, wrapper("cooked")...)
	tm.waitFor(t, "size=100x30")
	if _, err := tm.ptmx.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, "got INT")
	if got := exitCode(t, cmd); got != 4 {
		t.Errorf("exit code %d, want 4", got)
	}
}

// The command's exit ends Run though a process it left behind still holds the pty and writes to it.
func TestRunExitsWithCommand(t *testing.T) {
	tm, cmd := start(t, inTmux, wrapper("orphan")...)
	tm.waitFor(t, ";")
	var pid int
	if _, err := fmt.Sscanf(tm.String()[strings.Index(tm.String(), "orphan="):], "orphan=%d;", &pid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	done := make(chan int, 1)
	go func() { done <- exitCode(t, cmd) }()
	select {
	case got := <-done:
		if got != 4 {
			t.Errorf("exit code %d, want 4", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run outlived the command")
	}
}

// Closing the pane (its pty) ends Run and the command, as it ends an unwrapped command.
func TestRunEndsWithPane(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	argv := wrapper("pid")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), inTmux...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = tty.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Read in this goroutine: Close waits for a Read blocked in another one
	var out []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(out, []byte(";")) {
		n, err := ptmx.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			t.Fatal(err)
		}
	}
	var pid int
	if _, err := fmt.Sscanf(string(out[bytes.Index(out, []byte("pid=")):]), "pid=%d;", &pid); err != nil {
		t.Fatal(err)
	}
	_ = ptmx.Close()
	if got := exitCode(t, cmd); got != 128+int(syscall.SIGHUP) {
		t.Errorf("exit code %d, want %d", got, 128+int(syscall.SIGHUP))
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("command outlived its pane")
	}
}

// Output that isn't a terminal gets nothing to wrap for: the command runs in place.
func TestRunStdoutPipeExecs(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	argv := wrapper("image")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), inTmux...)
	cmd.Stdin, cmd.Stderr = tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	out, err := cmd.Output()
	_ = tty.Close()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("err %v, want exit code 7", err)
	}
	if !strings.HasPrefix(string(out), "tty=true,false ") || !strings.HasSuffix(string(out), "\n\x1b_Gi=1\x1b\\") {
		t.Errorf("got %q, want the command's own output, unwrapped", out)
	}
}
