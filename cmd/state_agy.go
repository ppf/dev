package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// agyState is what dev state --agy records for agy hook input on pane, whose hook line named state: rec=false
// when the event only stamps activity. A question records its Question and waits; a tool call starts the check
// for the permission prompt agy may draw for it (agy-dialog); a tool call's end while a decision waits records
// working once agy's input box is back. Errors go to stderr, never stdout or the exit status.
func agyState(cmd *cobra.Command, c *tmux.Client, pane string, input []byte, state string) (out []byte, resolved string, rec bool) {
	fail := func(err error) {
		fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
	}
	ev, err := agent.AgyHook(input)
	if err != nil {
		fail(err)
		return nil, "", false
	}
	switch ev.Kind {
	case agent.AgyAsk:
		if err := agent.RecordQuestion(c, pane, ev.Question); err != nil {
			fail(err)
			return nil, "", false
		}
		return ev.Input, agent.Waiting, true
	case agent.AgyToolDone:
		st, err := control.ReadStatus(c, pane)
		if err == nil && agent.Deciding(st.Trusted(), st.WaitingFor) {
			var capture string
			if capture, err = c.Run("capture-pane", "-p", "-t", pane); err == nil && screen.AgyBox(capture) {
				// Its question answered or its permission prompt approved: the turn goes on
				return ev.Input, agent.Working, true
			}
		}
		if err != nil {
			fail(err)
		}
	case agent.AgyTurn:
		if ev.ReplyErr != nil {
			// Its turn still ends, its boss note without the reply
			fail(ev.ReplyErr)
		}
		return ev.Input, state, true
	}
	// Activity, not a turn's start or end
	if err := agent.Touch(c, pane); err != nil {
		fail(err)
	}
	if ev.Kind == agent.AgyTool {
		if err := startAgyDialog(pane); err != nil {
			fail(err)
		}
	}
	return nil, "", false
}

// startAgyDialog runs dev agy-dialog for pane in the background: the hook must return before agy draws the
// permission prompt it may ask for the tool call. Nothing holds the hook's pipes (agy reads its stdout as its
// decision): its errors go to agyDialogLog in dev's state directory, and its own process group keeps it out of
// signals sent to the hook's.
func startAgyDialog(pane string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := config.StateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, agyDialogLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	bg := exec.Command(self, "agy-dialog", pane)
	bg.Stderr = log
	bg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bg.Start(); err != nil {
		return err
	}
	return bg.Process.Release()
}

// agyDialogLog is the file in dev's state directory that dev agy-dialog's errors go to.
const agyDialogLog = "agy-dialog.log"

// agyDialogWindow is how long after a tool call's PreToolUse agy-dialog looks for its permission prompt, polling
// every agyDialogPoll: agy draws one as soon as its hooks return. Once it recorded one, it reads the pane every
// agyDialogCheck for up to agyDialogWatch, so an approved command shows working before its PostToolUse.
const (
	agyDialogWindow = 5 * time.Second
	agyDialogPoll   = 250 * time.Millisecond
	agyDialogCheck  = time.Second
	agyDialogWatch  = time.Hour
)

func newAgyDialogCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "agy-dialog <pane>",
		Short:  "agy hook helper: record a working agy pane waiting on the permission prompt its screen shows, tell its boss, and watch it until answered",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, pane := tmux.New(), args[0]
			clk := control.RealClock
			logged := func(err error) error {
				return fmt.Errorf("%s %s: %w", time.Now().Format(time.DateTime), pane, err)
			}
			recorded := false
			var told error
			for start := clk.Now(); !recorded && clk.Now().Sub(start) < agyDialogWindow; clk.Sleep(agyDialogPoll) {
				st, err := control.ReadStatus(c, pane)
				if err != nil {
					return logged(err)
				}
				if st.Trusted() != agent.Working {
					// Moved on: a hook, or the turn ended
					return nil
				}
				capture, err := c.Run("capture-pane", "-p", "-t", pane)
				if err == nil {
					recorded, err = inbox.AgyPermission(c, clk, pane, st, capture)
				}
				if err != nil && !recorded {
					return logged(err)
				}
				// Recorded, its note or notification failed: still watched
				told = err
			}
			// Its screen settles it (control.ReadChecked): approved, agy runs the command before any hook fires
			for start := clk.Now(); recorded && clk.Now().Sub(start) < agyDialogWatch; clk.Sleep(agyDialogCheck) {
				st, err := control.ReadChecked(c, pane)
				if err != nil {
					return logged(errors.Join(told, err))
				}
				if !agent.Deciding(st.Trusted(), st.WaitingFor) {
					break
				}
			}
			if told != nil {
				return logged(told)
			}
			return nil
		},
	}
}
