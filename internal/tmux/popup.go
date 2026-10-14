package tmux

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ReexecInPopup re-runs this binary with the same arguments in a rounded display-popup of
// width×height, with guardEnv=1 set so the child doesn't popup again, and returns its exit code.
func (c *Client) ReexecInPopup(guardEnv, width, height string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	err = c.Interactive(popupArgs(guardEnv, width, height, ShellJoin(append([]string{self}, os.Args[1:]...)...))...)
	// tmux exits with the popup command's status, so a non-zero exit is the child's, not a failure.
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func popupArgs(guardEnv, width, height, command string) []string {
	return []string{"display-popup", "-E", "-w", width, "-h", height, "-b", "rounded", "-e", guardEnv + "=1", "--", command}
}

// ShellJoin quotes each word for the shell tmux runs popup and pane commands with, joined by spaces.
func ShellJoin(words ...string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = shellQuote(w)
	}
	return strings.Join(quoted, " ")
}

// shellQuote single-quotes s for the shell tmux runs the popup command with.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
