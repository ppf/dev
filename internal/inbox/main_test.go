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
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
