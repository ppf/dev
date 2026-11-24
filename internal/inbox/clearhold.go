package inbox

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// OptHandoff is the coordinator pane option dev agent clear sets while it runs (ClearValue). While its process lives,
// until the time it set, no note is handed to the boss, so none is lost to the /clear or taken in after the handoff
// was written; and the start hook after that conversation's /clear names the handoff.
const OptHandoff = "@dev_handoff"

// ClearValue is OptHandoff's value for this process clearing conversation (the boss's session id before the
// /clear) until at the latest, with its handoff file at path.
func ClearValue(conversation, path string, until time.Time) string {
	return strconv.Itoa(os.Getpid()) + " " + strconv.FormatInt(until.Unix(), 10) + " " + conversation + " " + path
}

// clearing reads pane's OptHandoff: the conversation and handoff path a dev agent clear set, and whether it still
// holds (its process runs, and its time isn't up: a pid reused after a kill holds nothing for long); a value it
// didn't write is an error.
func clearing(c *tmux.Client, pane string) (conversation, path string, held bool, err error) {
	v, err := c.Run("display-message", "-p", "-t", pane, "#{"+OptHandoff+"}")
	if v = strings.TrimSpace(v); err != nil || v == "" {
		return "", "", false, err
	}
	f := strings.SplitN(v, " ", 4)
	if len(f) != 4 {
		return "", "", false, fmt.Errorf("%s %q: want <pid> <until> <session> <path>", OptHandoff, v)
	}
	pid, perr := strconv.Atoi(f[0])
	until, uerr := strconv.ParseInt(f[1], 10, 64)
	if perr != nil || uerr != nil || pid <= 0 {
		return "", "", false, fmt.Errorf("%s %q: want <pid> <until> <session> <path>", OptHandoff, v)
	}
	alive := syscall.Kill(pid, 0)
	held = (alive == nil || errors.Is(alive, syscall.EPERM)) && time.Now().Before(time.Unix(until, 0))
	return f[2], f[3], held, nil
}

// ClearHeld reports a dev agent clear holding pane's notes now (OptHandoff).
func ClearHeld(c *tmux.Client, pane string) (bool, error) {
	_, _, held, err := clearing(c, pane)
	return held, err
}
