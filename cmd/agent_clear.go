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
		Long: fmt.Sprintf(`Clear an idle boss's context, for the session that started or adopted it (its manager), or the user in a shell.
A boss never clears itself, and in a Claude Code session only that session runs it. It takes minutes (each turn up
to --timeout): from an agent's Bash tool, run it in the background.
Refused, naming every failed check, unless the boss's hook records its session and transcript, it is idle with an
empty input box (dim suggestion text counts as empty), no other dev agent clear runs for it, none of the sessions
it started is working, without an agent or hook state, or waiting on a decision, no inbox note waits for it, no
planned wait or rate-limit resume is ahead for it or a worker, and its context share is at least %d%% (about 200k
tokens of a 1M window; --force skips only this check, also with the share unknown). Open asks to the user don't
block it.
From the save on, the boss's inbox notes are held back (@dev_handoff) until the command ends. Then, each prompt sent
only into an empty input box, and a turn ending on a permission prompt or dialog stopping it:
 1. dev prompts the boss to update its list (statuses, --details on every item the user or a person owes) and notes,
    and write the handoff $XDG_STATE_HOME/dev/handoff/<boss>/<time>.md with the sections %s:
    key facts numbered, at most %d, each ending "unconfirmed" or in a last clause "confirmed: <item, PR or file>".
    --facts <file> (the caller's own numbered facts) is copied next to it as <time>-facts.md for the boss to copy in.
 2. dev verifies it: the file was written with every section, 1-%d marked key facts each under its own number, every
    open item's #number, the old transcript's path and each --facts fact word for word, and no item of the user's or
    a person's on the list without details. A miss prompts the boss once to fix it; a second stops, nothing cleared.
 3. The checks again (a note that came in during the save refuses it), and right before the /clear: still the
    conversation that saved, no turn since, its box empty. /clear is confirmed by the new session, whose start hook
    gives it its list, notes and the handoff's path.
 4. Proof: dev prompts it to write <time>-proof.md (%s) before touching any worker, and checks
    every section, each key fact restated under its number with its mark, no more facts than the handoff's, each
    open ask's number under Open asks and something under Roles; a miss prompts it once with what it missed.
It prints the handoff, proof and old transcript paths; a failure after the /clear prints them with the proof prompt
to send by hand. dev checks form, not meaning: compare the two files (an unconfirmed fact stated as done, the roles)
and tell the user in one line. The files stay on disk.`,
			bossclear.MinContext, bossclear.Headings(bossclear.Sections), bossclear.MaxFacts, bossclear.MaxFacts,
			bossclear.Headings(bossclear.ProofSections)),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := typeInto(c, args[0])
			if err != nil {
				return err
			}
			if !a.Boss {
				return bossclear.NotBoss(a.Session)
			}
			if err := clearCaller(c, a); err != nil {
				return err
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
	cmd.Flags().DurationVar(&o.Timeout, "timeout", 15*time.Minute, "give up when a prompt's turn (save, proof, a fix) or the /clear takes longer than this")
	return cmd
}

// clearCaller refuses dev agent clear in a Claude Code session other than the one that started or adopted boss a (its
// manager): another boss never clears a peer.
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
