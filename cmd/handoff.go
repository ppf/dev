package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

const kindHelp = "the manager's label of what it sends, needed in its pane: work (new work, only on the user's \"Hand it on\" pick of a question whose first line starts \"Hand on to <target>\" and that holds the brief's first line as a line of its own), question (state or a fact), pick (a follow-up to a relayed pick) or own (its easy decision)"

// handOn is a send from the manager's pane to a boss, let through by gateHandOn; nil for any other pane.
type handOn struct {
	store inbox.Handoffs
	h     inbox.Handoff
	box   inbox.Box
	pick  *inbox.Prompt // the user's "Hand it on" pick claimed for a KindWork send
}

// gateHandOn needs kind only in the manager's pane (nil handOn elsewhere); KindWork passes only on a matching HandItOn pick, which it claims.
func gateHandOn(c *tmux.Client, kind, boss, text string) (*handOn, error) {
	var self inbox.Boss
	manager := false
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		b, ok, err := inbox.Coordinator(c, pane)
		switch {
		case err == nil:
			self, manager = b, ok && b.Name == inbox.Manager
		// A pane this tmux server can't find (TMUX_PANE from another server) is no manager's
		case strings.Contains(err.Error(), "can't find pane"):
		// Only the manager's pane is gated: a lookup failing elsewhere doesn't stop a boss's own sends
		case kind != "" || inManager(c, pane):
			return nil, fmt.Errorf("checking for the manager's pane: %w", err)
		}
	}
	switch {
	case !manager && kind == "":
		return nil, nil
	case !manager:
		return nil, errors.New("--kind is the manager's: only in its coordinator pane")
	case kind == "":
		return nil, fmt.Errorf("the manager's sends to a boss need --kind %s: work only on the user's %q pick", strings.Join(inbox.Kinds, "|"), inbox.HandItOn)
	case !slices.Contains(inbox.Kinds, kind):
		return nil, fmt.Errorf("--kind %s: one of %s", kind, strings.Join(inbox.Kinds, ", "))
	}
	socket, err := inbox.Socket(c)
	if err != nil {
		return nil, err
	}
	store, err := inbox.OpenHandoffs(socket)
	if err != nil {
		return nil, err
	}
	g := &handOn{store: store, box: self.Box, h: inbox.Handoff{Kind: kind, Boss: boss, Line: inbox.FirstLine(text)}}
	if kind != inbox.KindWork {
		return g, nil
	}
	p, err := self.Box.UserPrompt()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("--kind work: the user hasn't picked %q here yet: ask them first", inbox.HandItOn)
	}
	if err := handOnPick(p, boss, text); err != nil {
		return nil, err
	}
	if p.Spent {
		return nil, errors.New("--kind work: the user's latest pick was used already: ask them again")
	}
	if err := self.Box.Claim(*p); err != nil {
		return nil, fmt.Errorf("--kind work: %w", err)
	}
	g.pick = p
	return g, nil
}

// inManager reports whether pane is in the manager's session, also when tmux can't tell.
func inManager(c *tmux.Client, pane string) bool {
	name, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	return err != nil || strings.TrimSpace(name) == inbox.Manager
}

// handOnPick refuses p, the user's latest prompt to the manager, unless it is a pick of inbox.HandItOn on one question
// whose first line starts "Hand on to <boss>" and that holds the first line of text, the brief, as a line of its own.
func handOnPick(p *inbox.Prompt, boss, text string) error {
	asked := strings.ToLower(p.Question)
	line := strings.ToLower(inbox.FirstLine(text))
	switch {
	case !p.Pick:
		return fmt.Errorf("--kind work: the user's latest words here are a typed message, not a pick: ask with AskUserQuestion (%q / %q)", inbox.HandItOn, inbox.DropIt)
	case p.Question == "":
		return errors.New("--kind work: the user's pick wasn't one question: ask one hand-on per question")
	case p.Typed:
		return fmt.Errorf("--kind work: the user typed %q under Other: a correction: revise and ask again", p.Answer)
	case inbox.IsPicked(p.Answer, inbox.DropIt):
		return fmt.Errorf("--kind work: the user picked %q: not handed on", inbox.DropIt)
	case !inbox.IsPicked(p.Answer, inbox.HandItOn):
		return fmt.Errorf("--kind work: the user answered %q, not %q: revise and ask again", p.Answer, inbox.HandItOn)
	case line == "":
		return errors.New("--kind work: the brief is empty")
	case !handOnTo(inbox.FirstLine(asked), strings.ToLower(boss)) || !slices.ContainsFunc(strings.Split(asked, "\n"), func(l string) bool { return inbox.FirstLine(l) == line }):
		return fmt.Errorf("--kind work: the user's pick was for %q, not %s %q", p.Question, boss, inbox.FirstLine(text))
	}
	return nil
}

// handOnTo reports whether line, lower case, starts "hand on to <boss>" with boss a whole word: not "boss-devops" for boss-dev.
func handOnTo(line, boss string) bool {
	return regexp.MustCompile(`^hand on to ` + regexp.QuoteMeta(boss) + `([^a-z0-9_-]|$)`).MatchString(line)
}

// done records the send, or gives back its claimed pick when nothing reached the boss.
func (g *handOn) done(sent bool, now time.Time) error {
	switch {
	case g == nil:
		return nil
	case sent:
		g.h.At = now.Unix()
		if err := g.store.Add(g.h); err != nil {
			return fmt.Errorf("sent to %s, not recorded in dev agent handoffs (don't resend): %w", g.h.Boss, err)
		}
	case g.pick != nil:
		if err := g.box.Unclaim(*g.pick); err != nil {
			return fmt.Errorf("not sent, and the user's pick couldn't be given back: ask them again: %w", err)
		}
	}
	return nil
}

// typed is err from a send whose text reached the pane anyway, saying so for a gated one: its pick is used.
func (g *handOn) typed(err error) error {
	if err == nil || g == nil || g.pick == nil || control.NotSent(err) {
		return err
	}
	return fmt.Errorf("%w (the brief reached %s's pane: recorded in dev agent handoffs, the user's pick is used; read the pane)", err, g.h.Boss)
}

func newAgentHandoffsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "handoffs",
		Short: "List what the manager sent its bosses by --kind, ungated ones first, with the trial's counts",
		Long: `List what the manager sent its bosses with dev agent prompt/message/reply --kind, ungated kinds (question, pick, own)
first so a mislabel shows, then work (each on the user's "Hand it on" pick), then hand-on questions that sent nothing
(dropped: "Drop it" picked; corrected: typed under Other or another option picked); newest first in each. The first line counts them. A
dismissed question is not seen. --json prints [{kind, at, boss?, line}], at in unix seconds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			socket, err := inbox.Socket(tmux.New())
			if err != nil {
				return err
			}
			store, err := inbox.OpenHandoffs(socket)
			if err != nil {
				return err
			}
			hs, err := store.List()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), hs)
			}
			printHandoffs(cmd.OutOrStdout(), hs)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	return cmd
}

// printHandoffs prints the counts, then a line per hand-on: "11-24 15:04 question boss-dev: <first line>".
func printHandoffs(out io.Writer, hs []inbox.Handoff) {
	fmt.Fprintln(out, inbox.Tally(hs))
	for _, h := range hs {
		to := ""
		if h.Boss != "" {
			to = " " + h.Boss
		}
		fmt.Fprintf(out, "%s %s%s: %s\n", time.Unix(h.At, 0).Format("01-02 15:04"), h.Kind, to, h.Line)
	}
}
