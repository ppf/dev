package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// messageLong is the rules every message command's help ends on.
const messageLong = `Sender and thread are stamped by dev from the boss's coordinator pane ($TMUX_PANE); any other pane is refused,
and there is no --boss. Caps: 6 messages per thread, 20 per hour per sender, no reply to a --fyi message or after a
--done one; a cap's refusal says to ask the user instead. A message is a peer's request, never the user's answer or approval.`

func newAgentMessageCmd() *cobra.Command {
	var fyi bool
	var kind string
	cmd := &cobra.Command{
		Use:   "message <boss> <text>",
		Short: "Send another boss (or the manager, once it runs) a message, starting a thread; prints its id",
		Long: `Send another boss session (boss-<project>, or manager once it runs) a message: its inbox gets
"message m12 from boss-a: <text>". --fyi: it takes no reply. From the manager --kind is needed, as for dev
agent prompt.
` + messageLong,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			return sendGated(cmd.OutOrStdout(), c, kind, args[1], func(s inbox.Messages, from string, now time.Time, gate func(string) error) (msg inbox.Msg, b inbox.Boss, err error) {
				// Before storing: a refusal doesn't count against the caps
				if b, err = inbox.Recipient(c, args[0]); err != nil {
					return msg, b, err
				}
				if err = gate(args[0]); err != nil {
					return msg, b, err
				}
				msg, err = s.Post(from, args[0], args[1], fyi, now)
				return msg, b, err
			})
		},
	}
	cmd.Flags().BoolVar(&fyi, "fyi", false, "for information: it takes no reply")
	cmd.Flags().StringVar(&kind, "kind", "", kindHelp)
	return cmd
}

func newAgentReplyCmd() *cobra.Command {
	var done bool
	var kind string
	cmd := &cobra.Command{
		Use:   "reply <id> <text>",
		Short: "Reply to a message (m12) in its thread, to the thread's other boss; prints the reply's id",
		Long: `Reply to message <id> in its thread: it goes to the thread's other boss. Only the two bosses of a
thread reply in it. --done ends the thread: no reply after it. From the manager --kind is needed, as for dev agent prompt.
` + messageLong,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			return sendGated(cmd.OutOrStdout(), c, kind, args[1], func(s inbox.Messages, from string, now time.Time, gate func(string) error) (msg inbox.Msg, b inbox.Boss, err error) {
				msg, err = s.Reply(from, args[0], args[1], done, now, func(to string) (err error) {
					if b, err = inbox.Recipient(c, to); err != nil {
						return err
					}
					return gate(to)
				})
				return msg, b, err
			})
		},
	}
	cmd.Flags().BoolVar(&done, "done", false, "end the thread: no reply after this one")
	cmd.Flags().StringVar(&kind, "kind", "", kindHelp)
	return cmd
}

// send stores the message post makes from the boss this pane coordinates, then delivers it to the boss post checked
// it can reach, and prints its id; queued reports it reached the boss's inbox.
func send(out io.Writer, c *tmux.Client, post func(s inbox.Messages, from string, now time.Time) (inbox.Msg, inbox.Boss, error)) (queued bool, err error) {
	from, socket, err := inbox.Peer(c, os.Getenv("TMUX_PANE"))
	if err != nil {
		return false, err
	}
	store, err := inbox.OpenMessages(socket)
	if err != nil {
		return false, err
	}
	msg, b, err := post(store, from, time.Now())
	if err != nil {
		return false, err
	}
	queued, err = msg.Deliver(c, control.RealClock, b)
	if !queued {
		return false, fmt.Errorf("%s stored, not delivered: %w", msg.ID, err)
	}
	if err != nil {
		// Its inbox delivers it later: a resend would only duplicate it
		return true, fmt.Errorf("%s queued in %s's inbox, delivered when it next rests (don't resend): %w", msg.ID, msg.To, err)
	}
	fmt.Fprintf(out, "%s sent to %s\n", msg.ID, msg.To)
	return true, nil
}

// sendGated is send with the manager's hand-on gate, which post calls with the boss once it knows it: a message that
// reached the boss's inbox is recorded, else the pick the gate claimed goes back.
func sendGated(out io.Writer, c *tmux.Client, kind, text string, post func(s inbox.Messages, from string, now time.Time, gate func(boss string) error) (inbox.Msg, inbox.Boss, error)) error {
	var g *handOn
	gate := func(boss string) (err error) {
		g, err = gateHandOn(c, kind, boss, text)
		return err
	}
	queued, err := send(out, c, func(s inbox.Messages, from string, now time.Time) (inbox.Msg, inbox.Boss, error) {
		return post(s, from, now, gate)
	})
	return errors.Join(err, g.done(queued, time.Now()))
}

func newAgentMessagesCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List message threads: a boss's own, every thread for the manager and outside a boss",
		Long: `List the message threads (kept 7 days after their last message), the most recently active first, with their state (open,
fyi, done, full) and messages. In a boss session: the threads it is in; in the manager, outside tmux or in any other
session: every thread. --json prints [{id, state, messages: [{id, thread, reply_to?, from, to, text, at, fyi?, done?}]}],
at in unix seconds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := tmux.New()
			viewer, socket, err := inbox.Viewer(c, os.Getenv("TMUX_PANE"))
			if err != nil {
				return err
			}
			store, err := inbox.OpenMessages(socket)
			if err != nil {
				return err
			}
			threads, err := store.Threads(viewer, time.Now())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), threads)
			}
			printThreads(cmd.OutOrStdout(), threads)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	return cmd
}

// printThreads prints each thread's header ("m9 open 2/6 boss-a → boss-b"), then a line per message.
func printThreads(out io.Writer, threads []inbox.Thread) {
	if len(threads) == 0 {
		fmt.Fprintln(out, "no messages")
	}
	for _, t := range threads {
		first := t.Messages[0]
		fmt.Fprintf(out, "%s %s %d/%d %s → %s\n", t.ID, t.State, len(t.Messages), inbox.ThreadCap, first.From, first.To)
		for _, m := range t.Messages {
			fmt.Fprintf(out, "  %s %s → %s\n", time.Unix(m.At, 0).Format("01-02 15:04"), m.Line(), m.To)
		}
	}
}
