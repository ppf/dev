package control

import (
	"fmt"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// AgyReady waits up to timeout for an agy pane's input box, then records it idle: agy runs no hook before its
// first turn, and wait and prompt --wait need a state. With trust, its trust dialog for the pane's directory is
// confirmed once (Enter on "Yes, I trust this folder"); without trust it is left alone at once. Any other screen
// (a login, a dialog, a trust dialog still drawn for another width or folder) is left once timeout passes: left
// says why, with the screen. Without hooked (agy's hooks don't run dev state --agy) its input box records nothing:
// no hook would ever move that state.
func AgyReady(c *tmux.Client, clk Clock, pane string, trust, hooked bool, timeout time.Duration) (left string, err error) {
	dir, err := c.Run("display-message", "-p", "-t", pane, "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(dir)
	pressed := false
	for start := clk.Now(); ; clk.Sleep(pollInterval) {
		capture, err := c.Run("capture-pane", "-p", "-t", pane)
		if err != nil {
			return "", err
		}
		if screen.AgyIdle(capture) && !hooked {
			return "agy runs no dev state --agy hook (see the README's agy state): no state recorded; send with plain prompt and read", nil
		}
		if screen.AgyIdle(capture) {
			return "", agent.Record(c, pane, agent.Idle, agent.Session{}, agent.Hook{})
		}
		asked, yes, ok := screen.AgyTrust(capture)
		why := ""
		switch {
		case !ok:
		case !trust:
			// Its pane's folder: a dialog drawn before the pane took its size may show a cut path
			return fmt.Sprintf("agy asks whether to trust %s, a folder this start didn't make: left for the user%s", dir, leftHint(capture)), nil
		case squash(asked) != squash(dir):
			// A path wrapped at a space loses it in AgyTrust
			why = "not its pane's " + dir
		case !yes:
			why = "its cursor not on Yes"
		case pressed:
			why = "still open after Enter"
		default:
			if _, err := c.Run("send-keys", "-t", pane, "Enter"); err != nil {
				return "", fmt.Errorf("confirming agy's trust dialog: %w", err)
			}
			pressed = true
		}
		if clk.Now().Sub(start) < timeout {
			continue
		}
		if why != "" {
			return fmt.Sprintf("agy asks whether to trust %s, %s, after %s: left for the user%s", asked, why, timeout, leftHint(capture)), nil
		}
		return fmt.Sprintf("agy shows no input box after %s: left as it is%s", timeout, leftHint(capture)), nil
	}
}

// leftHint ends AgyReady's note on a screen it left alone: how to go on, and the screen.
func leftHint(capture string) string {
	return "; once it's answered, run this start again to record agy idle; its screen:\n" + strings.TrimSpace(capture)
}
