package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootPrintsHelp(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), rootCmd.Short) {
		t.Errorf("help output missing description:\n%s", out.String())
	}
}
