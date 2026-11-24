package inbox

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ppf/dev/internal/tmux"
)

// OptHandoff is the coordinator pane option dev agent clear sets while it runs (ClearValue). While its process lives,
// no note is handed to the boss, so none is lost to the /clear or taken in after the handoff was written; and the
// start hook after that conversation's /clear names the handoff.
const OptHandoff = "@dev_handoff"

// ClearValue is OptHandoff's value for this process clearing conversation (the boss's session id before the
// /clear), with its handoff file at path.
func ClearValue(conversation, path string) string {
	return strconv.Itoa(os.Getpid()) + " " + conversation + " " + path
}

// clearing reads pane's OptHandoff: the conversation and handoff path a dev agent clear set, and whether its process
// still runs (held); a value it didn't write is an error.
func clearing(c *tmux.Client, pane string) (conversation, path string, held bool, err error) {
	v, err := c.Run("display-message", "-p", "-t", pane, "#{"+OptHandoff+"}")
	if v = strings.TrimSpace(v); err != nil || v == "" {
		return "", "", false, err
	}
	f := strings.SplitN(v, " ", 3)
	pid, perr := strconv.Atoi(f[0])
	if len(f) != 3 || perr != nil || pid <= 0 {
		return "", "", false, fmt.Errorf("%s %q: want <pid> <session> <path>", OptHandoff, v)
	}
	alive := syscall.Kill(pid, 0)
	return f[1], f[2], alive == nil || errors.Is(alive, syscall.EPERM), nil
}
