package control

import (
	"fmt"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// AgyReady waits up to timeout for a new agy pane's input box, then records it idle: agy runs no hook before
// its first turn, and wait and prompt --wait need a state. With trust, its trust dialog for the pane's directory
// is confirmed once (Enter on "Yes, I trust this folder"). Any other screen is left alone, as a login screen, an
// unknown dialog, or the trust dialog without trust: left says which, with the screen.
func AgyReady(c *tmux.Client, clk Clock, pane string, trust bool, timeout time.Duration) (left string, err error) {
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
		switch {
		case screen.AgyIdle(capture):
			return "", agent.Record(c, pane, agent.Idle, agent.Session{}, agent.Hook{})
		case screen.AgyTrust(capture, dir) && !trust:
			return fmt.Sprintf("agy asks whether to trust %s, a folder this start didn't make: left for the user; its screen:\n%s", dir, strings.TrimSpace(capture)), nil
		case screen.AgyTrust(capture, dir) && !pressed:
			if _, err := c.Run("send-keys", "-t", pane, "Enter"); err != nil {
				return "", fmt.Errorf("confirming agy's trust dialog: %w", err)
			}
			pressed = true
		}
		if clk.Now().Sub(start) >= timeout {
			return fmt.Sprintf("agy shows no input box after %s: left as it is; its screen:\n%s", timeout, strings.TrimSpace(capture)), nil
		}
	}
}
