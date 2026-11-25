package herd

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the state directory at a temp one: a coordinator's start writes its prompt there, never into
// the live dev's.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "herd-state-")
	if err == nil {
		err = os.Setenv("XDG_STATE_HOME", dir)
	}
	if err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
