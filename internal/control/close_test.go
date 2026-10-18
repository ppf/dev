package control

import (
	"path/filepath"
	"testing"
)

func TestNewDir(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "results")
	for _, want := range []string{"s", "s.2", "s.3"} {
		if got, err := newDir(parent, "s"); err != nil || got != filepath.Join(parent, want) {
			t.Errorf("newDir = %q, %v; want %s", got, err, want)
		}
	}
}
