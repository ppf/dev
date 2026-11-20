package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/serve"
)

// launchAgent writes (install) or removes dev serve's launchd agent and prints the launchctl line that loads or
// stops it; it never runs launchctl.
func launchAgent(out io.Writer, install bool, addr string, awake bool) error {
	if runtime.GOOS != "darwin" {
		return errors.New("dev serve --install and --uninstall write a launchd agent: macOS only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if !install {
		path, err := serve.Uninstall(home)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "removed %s\nstop the running one: launchctl bootout %s/%s\n", path, domain, serve.Label)
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	state, err := config.StateDir()
	if err != nil {
		return err
	}
	return installAgent(out, home, domain, serve.Agent{Bin: bin, Addr: addr, Awake: awake, Log: filepath.Join(state, "serve", "serve.log"), Env: launchEnv(os.Getenv)})
}

// installAgent checks a's --addr, writes its plist under home and prints how to load it in domain.
func installAgent(out io.Writer, home, domain string, a serve.Agent) error {
	if a.Addr != "" {
		if _, _, err := serve.Check(a.Addr); err != nil {
			return err
		}
	}
	path, err := serve.Install(home, a)
	if err != nil {
		return err
	}
	// bootout first: a loaded agent (an earlier install) refuses a second bootstrap
	_, err = fmt.Fprintf(out, "wrote %s: %s serve starts at each login (log: %s)\nstart it now: launchctl bootout %s/%s 2>/dev/null; sleep 1; launchctl bootstrap %s %q\n",
		path, a.Bin, a.Log, domain, serve.Label, domain, path)
	return err
}

// launchEnv is serve.LaunchEnv's variables that are set.
func launchEnv(getenv func(string) string) map[string]string {
	env := map[string]string{}
	for _, k := range serve.LaunchEnv {
		if v := getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// keepAwake holds a no-sleep assertion until this process exits: caffeinate -s, which macOS honours on mains
// power only; on battery it says the Mac may sleep.
func keepAwake(out, stderr io.Writer) (int, error) {
	c := exec.Command("/usr/bin/caffeinate", "-s", "-w", strconv.Itoa(os.Getpid()))
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a Ctrl-C ends dev serve, and -w then ends caffeinate
	if err := c.Start(); err != nil {
		return 0, fmt.Errorf("--awake: %w", err)
	}
	go watchAwake(c.Process.Pid, stderr)
	ps, err := exec.Command("/usr/bin/pmset", "-g", "ps").Output()
	if err != nil {
		fmt.Fprintf(out, "⚠ --awake: could not read the power source (pmset: %v): on battery the Mac may sleep\n", err)
	} else if serve.OnBattery(string(ps)) {
		fmt.Fprintln(out, "⚠ on battery: the Mac may sleep and the phone page stop answering until it is on mains power")
	}
	return c.Process.Pid, nil
}

// watchAwake says when caffeinate pid, a child of this process (also after a reload), ends.
func watchAwake(pid int, stderr io.Writer) {
	why := "unknown"
	p, err := os.FindProcess(pid)
	if err == nil {
		var st *os.ProcessState
		if st, err = p.Wait(); err == nil {
			why = st.String()
		}
	}
	if err != nil {
		why = err.Error()
	}
	fmt.Fprintf(stderr, "⚠ --awake: caffeinate ended (%s): the Mac may sleep\n", why)
}
