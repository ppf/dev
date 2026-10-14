package tmux

import (
	"errors"
	"os/exec"
	"slices"
	"testing"
)

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":            "''",
		"/go/bin/dev": "'/go/bin/dev'",
		"it's $HOME":  `'it'"'"'s $HOME'`,
		"a b;c":       "'a b;c'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
		if got, err := exec.Command("sh", "-c", "printf %s "+shellQuote(in)).Output(); err != nil || string(got) != in {
			t.Errorf("sh round trip of %q = %q, %v", in, got, err)
		}
	}
}

func TestPopupArgs(t *testing.T) {
	got := popupArgs("GUARD", "80%", "60%", "'/bin/dev' 'x'")
	want := []string{"display-popup", "-E", "-w", "80%", "-h", "60%", "-b", "rounded", "-e", "GUARD=1", "--", "'/bin/dev' 'x'"}
	if !slices.Equal(got, want) {
		t.Errorf("popupArgs = %q, want %q", got, want)
	}
}

func TestReexecInPopupRefusedUnderTest(t *testing.T) {
	if _, err := New().ReexecInPopup("GUARD", "80%", "60%"); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want ErrNoSocket", err)
	}
}
