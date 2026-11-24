package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/bossclear"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
)

func newAgentClearCmd() *cobra.Command {
	var o bossclear.Options
	cmd := &cobra.Command{
		Use:   "clear <boss> [--facts <file>] [--force]",
		Short: "Clear an idle boss's context safely: it saves a handoff, dev verifies it, clears it, and the fresh boss proves it",
		Long: fmt.Sprintf(`Clear an idle boss's context, for the manager that started or adopted it, or the user in a shell. A boss never
clears itself, and in a Claude Code session only the boss's manager runs it.
Refused, naming every failed check, unless the boss's hook records its session, it is idle with an empty input box
(dim suggestion text counts as empty), none of its workers is working, without hook state or waiting on a decision,
no inbox note waits for it, no planned wait or rate-limit resume is ahead for it or a worker, and its context share
is at least %d%% (about 200k tokens of a 1M window; --force skips only this check). Open asks to the user don't
block it.
Then, each prompt sent only into an empty input box, and a turn ending on a permission prompt or dialog stopping it:
 1. dev prompts the boss to update its list (statuses, --details on every item the user or a person owes) and notes,
    and write the handoff $XDG_STATE_HOME/dev/handoff/<boss>/<time>.md with the sections %s:
    key facts numbered, at most %d, each ending "confirmed: <item, PR or file>" or "unconfirmed". --facts <file>
    (the caller's own facts) is copied next to it as <time>-facts.md for the boss to merge in.
 2. dev verifies it: the file was written with every section, 1-%d marked key facts, every open item's #number and
    the old transcript's path, and no item of the user's or a person's on the list without details. A miss prompts
    the boss once to fix it; a second miss stops, nothing cleared.
 3. The checks again, then /clear, confirmed by the new session. Its start hook gives it its list, notes and the
    handoff's path.
 4. Proof: dev prompts it to write <time>-proof.md (%s) before touching any worker, and checks each key
    fact is restated under its number with its mark and each open ask's number is there; a miss prompts it once
    with what it missed.
It prints the handoff, proof and old transcript paths. dev checks form, not meaning: compare the two files (an
unconfirmed fact stated as done, the roles) and tell the user in one line. The files stay on disk.`,
			bossclear.MinContext, bossclear.Headings(bossclear.Sections), bossclear.MaxFacts, bossclear.MaxFacts,
			bossclear.Headings(bossclear.ProofSections)),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := typeInto(c, args[0])
			if err != nil {
				return err
			}
			// Not a boss: Run refuses it, naming that
			if a.Boss {
				if err := clearCaller(c, a); err != nil {
					return err
				}
			}
			o.Out = cmd.ErrOrStderr()
			r, err := bossclear.Run(c, control.RealClock, a, o)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared %s: %d key facts restated, numbers match\nhandoff: %s\nproof: %s\nold transcript: %s\n"+
				"Compare the proof with the handoff (meaning is yours to judge), then tell the user in one line.\n",
				a.Session, r.Facts, r.Handoff, r.Proof, r.Transcript)
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Facts, "facts", "", "your own numbered key facts for the boss to merge into its handoff")
	cmd.Flags().BoolVar(&o.Force, "force", false, fmt.Sprintf("clear a boss under %d%% context (or with it unknown)", bossclear.MinContext))
	cmd.Flags().DurationVar(&o.Stall, "stall", 30*time.Second, "give up when a prompt isn't picked up within this")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", 15*time.Minute, "give up when a turn (save, proof) runs longer than this")
	return cmd
}

// clearCaller refuses dev agent clear in a Claude Code session other than boss a's manager (its starter): another
// boss never clears a peer.
func clearCaller(c *tmux.Client, a control.Agent) error {
	if os.Getenv("CLAUDECODE") == "" {
		return nil
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("%s is cleared only by its manager's coordinator pane, or by the user in a shell", a.Session)
	}
	own, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return err
	}
	own = strings.TrimSpace(own)
	if a.StartedBy == "" || own != a.StartedBy {
		return fmt.Errorf("%s is cleared only by the manager that started or adopted it (%q), or by the user in a shell; not from %s",
			a.Session, a.StartedBy, own)
	}
	return nil
}
