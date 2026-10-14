//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// devBin is the dev binary built once for the whole suite.
var devBin string

// TestMain pins a UTF-8 locale, builds dev, kills servers left by an earlier run that died without
// cleanup (go test -timeout panics skip t.Cleanup) and kills this run's servers on SIGINT/SIGTERM.
func TestMain(m *testing.M) {
	// Every server, client and script inherits it: tmux draws UTF-8 only in a UTF-8 locale, and bash's
	// ${#x} counts bytes otherwise, so "·" and "●" lines would pad differently from dev's.
	os.Setenv("LC_ALL", "en_US.UTF-8")
	killServers(func(pid int) bool { return !alive(pid) })
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		killServers(func(pid int) bool { return pid == os.Getpid() })
		os.Exit(1)
	}()
	dir, err := os.MkdirTemp("", "dev-it-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	devBin = filepath.Join(dir, "dev")
	build := exec.Command("go", "build", "-o", devBin, "github.com/ppf/dev")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "build dev:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
