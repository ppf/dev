package herd

import (
	"os"
	"testing"
)

// TestMain points the state directory at a temp one: a coordinator's start writes its prompt there, never into
// the live dev's.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "herd-state-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
