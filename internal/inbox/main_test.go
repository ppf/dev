package inbox

import (
	"os"
	"testing"
)

// TestMain points the manager folder at a temporary directory: a boss's start hook writes its last-session file there.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dev-inbox-config-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		panic(err)
	}
	os.Exit(code)
}
