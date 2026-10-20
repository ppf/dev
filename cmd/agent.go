package cmd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/skill"
	"github.com/ppf/dev/internal/tmux"
)

// Exit codes of dev agent besides 0 and 1, for coordinators to branch on.
const (
	exitTimeout = 2
	exitStalled = 3
)

// exitCode maps an error to dev's exit status.
func exitCode(err error) int {
	switch {
	case errors.Is(err, control.ErrTimeout):
		return exitTimeout
	case errors.Is(err, control.ErrStalled):
		return exitStalled
	}
	return 1
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Drive agent sessions from a coordinator agent (targets: session name or pane id)",
		Long: `Drive agent sessions from a coordinator agent. A target is a session name (exact) or an agent
pane id (%N), never a sidebar position. Exit status: 0 ok, 1 error, 2 timeout, 3 stalled (prompt not picked up).
Run "dev agent skill" for the coordinator skill.`,
	}
	cmd.AddCommand(newAgentListCmd(), newAgentGetCmd(), newAgentReadCmd(), newAgentPromptCmd(), newAgentWaitCmd(),
		newAgentSendKeysCmd(), newAgentStartCmd(), newAgentCloseCmd(), newAgentSkillCmd())
	return cmd
}

func newAgentListCmd() *cobra.Command {
	var asJSON, mine bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every session's agent: session, pane, agent, state, branch, path, cost",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := tmux.New()
			agents, err := control.List(c)
			if err != nil {
				return err
			}
			if mine {
				pane := os.Getenv("TMUX_PANE")
				if pane == "" {
					return errors.New("--mine lists the workers of the session it runs in: run it inside tmux")
				}
				if agents, err = control.StartedBy(c, agents, pane); err != nil {
					return err
				}
			}
			return printAgents(cmd.OutOrStdout(), asJSON, agents)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	cmd.Flags().BoolVar(&mine, "mine", false, "only the workers this session started with dev agent start (its child sessions)")
	return cmd
}

func newAgentGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get <target>",
		Short: "Show one agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := control.Resolve(c, args[0])
			if err != nil {
				return err
			}
			if err := a.ReadQueued(c); err != nil {
				return err
			}
			return printAgent(cmd.OutOrStdout(), asJSON, a)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	return cmd
}

// printAgents prints agents as a JSON array or as tab-separated rows (state "-" when unknown, cost "-" when unknown or hidden).
func printAgents(out io.Writer, asJSON bool, agents []control.Agent) error {
	if asJSON {
		return printJSON(out, agents)
	}
	for _, a := range agents {
		state := a.State
		if state == "" {
			state = "-"
		}
		cost := "-"
		if a.CostUSD != nil && !a.HideCost {
			cost = sidebar.FormatCost(*a.CostUSD)
		}
		fmt.Fprintln(out, strings.Join([]string{a.Session, a.Pane, a.Label, state, a.Branch, a.Path, cost}, "\t"))
	}
	return nil
}

// printAgent prints one agent as a JSON object or as a printAgents row.
func printAgent(out io.Writer, asJSON bool, a control.Agent) error {
	if asJSON {
		return printJSON(out, a)
	}
	return printAgents(out, false, []control.Agent{a})
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newAgentReadCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "read <target>",
		Short: "Print the last lines of the agent's pane (its screen, plus scrollback when it has any)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := control.Resolve(c, args[0])
			if err != nil {
				return err
			}
			text, err := control.Read(c, a.Pane, lines)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 50, "number of lines")
	return cmd
}

// resolveOther resolves target and refuses this process's own pane: a coordinator waiting on itself
// would never see its turn end.
func resolveOther(c *tmux.Client, target string) (control.Agent, error) {
	a, err := control.Resolve(c, target)
	if err == nil && a.Pane == os.Getenv("TMUX_PANE") {
		return a, fmt.Errorf("%s is this agent's own pane", target)
	}
	return a, err
}

func newAgentPromptCmd() *cobra.Command {
	var wait bool
	var stall, timeout time.Duration
	cmd := &cobra.Command{
		Use:   "prompt <target> [text...]",
		Short: "Paste a prompt into the agent and submit it (text from the arguments, else stdin)",
		Long: `Paste a prompt into the agent as one bracketed paste and press Enter, so multi-line text submits
as a single prompt. Without text arguments the prompt is read from stdin. An agent showing a permission
prompt or question is refused: the text would answer it (use send-keys). To a working agent it is
queued (printed): Claude reads it at its turn's next step. A one-line slash command to an idle Claude is
watched up to 3s: the dialog /effort and /model open mid-conversation is confirmed with 1, any other
dialog is an error.

--wait (Claude only) needs an idle agent and returns once the hook saw the prompt and the agent settled
again (idle or waiting), printing that state; a slash command Claude runs itself, once its screen shows
it ran. Exit 3 when the prompt isn't picked up within --stall (it may still sit in the input box: read
the pane), exit 2 on --timeout.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args[1:], " ")
			if len(args) == 1 {
				in, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = strings.TrimRight(string(in), "\n")
			}
			c := tmux.New()
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			if !wait {
				queued, err := control.Prompt(c, control.RealClock, a.Pane, text)
				if queued {
					fmt.Fprintf(cmd.OutOrStdout(), "queued: %s is working; it reads the prompt at its turn's next step\n", a.Session)
				}
				return err
			}
			st, err := control.PromptWait(c, control.RealClock, a.Pane, text, stall, timeout)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), st.Trusted())
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the agent finished its turn")
	cmd.Flags().DurationVar(&stall, "stall", 10*time.Second, "with --wait: give up when the prompt isn't picked up within this")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "with --wait: give up after this")
	return cmd
}

func newAgentWaitCmd() *cobra.Command {
	var until string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <target>",
		Short: "Wait until the agent is idle, waiting, or either (settled); prints the state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			done, ok := map[string]func(control.Status) bool{
				"settled":     control.Status.Settled,
				agent.Idle:    func(s control.Status) bool { return s.Trusted() == agent.Idle },
				agent.Waiting: func(s control.Status) bool { return s.Trusted() == agent.Waiting },
			}[until]
			if !ok {
				return fmt.Errorf("unknown --until %q, want idle, waiting or settled", until)
			}
			c := tmux.New()
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			st, err := control.Wait(c, control.RealClock, a.Pane, timeout, done)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), st.Trusted())
			return nil
		},
	}
	cmd.Flags().StringVar(&until, "until", "settled", "idle, waiting, or settled (idle or waiting)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "give up after this")
	return cmd
}

func newAgentSendKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "send-keys <target> <key>...",
		Short: "Press keys in the agent's pane: " + strings.Join(control.KeyNames(), ", ") + ", or one character",
		Long: `Press keys in the agent's pane: ` + strings.Join(control.KeyNames(), ", ") + `, or one character.
Esc to a working Claude returns once its screen shows the turn interrupted (up to 3s), recording it idle:
Claude fires no hook for that.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			if err := control.SendKeys(c, a.Pane, args[1:]); err != nil {
				return err
			}
			if a.Label == "Claude" && a.State == agent.Working && slices.ContainsFunc(args[1:], func(k string) bool { return strings.EqualFold(k, "esc") }) {
				if err := control.AfterEsc(c, control.RealClock, a.Pane); err != nil {
					// Not to be retried: a second Esc opens Claude's rewind
					return fmt.Errorf("esc sent; recording the interrupted turn failed: %w", err)
				}
			}
			return nil
		},
	}
}

func newAgentStartCmd() *cobra.Command {
	var asJSON bool
	var agentCmd string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "start <project> [worktree]",
		Short: "Open a project (or its worktree) in the background and wait until its agent is registered",
		Long: `Open a project's session, or its worktree's (created when missing), without attaching to it, and
wait until its agent's first hook registers it (a Codex or Grok pane counts once its CLI runs, so read its
screen before the first prompt). An existing session is reused; one running another agent than an explicit
--agent is an error. Prints the agent like get.`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentCmd != agent.Claude && agentCmd != agent.Codex && agentCmd != agent.Grok {
				return fmt.Errorf("unknown --agent %q, want claude, codex or grok", agentCmd)
			}
			wt, err := worktreeArg(args)
			if err != nil {
				return err
			}
			c, session := tmux.New(), open.SessionName(args[0], wt)
			_, missing := c.Run("has-session", "-t", tmux.SessionTarget(session))
			// A herd's x only kills workers its coordinator started, never a reused session; workers start
			// without Remote Control
			pane := os.Getenv("TMUX_PANE")
			worker := missing != nil && pane != ""
			if err := openProject(cmd, args[0], wt, true, worker, agentCmd); err != nil {
				return err
			}
			if worker {
				if err := herd.MarkStartedBy(c, session, pane); err != nil {
					return err
				}
			}
			a, err := control.AwaitAgent(c, control.RealClock, session, timeout)
			if err != nil {
				return err
			}
			if missing == nil && cmd.Flags().Changed("agent") && !strings.EqualFold(a.Label, agentCmd) {
				return fmt.Errorf("session %s already runs %s, not %s", session, a.Label, agentCmd)
			}
			return printAgent(cmd.OutOrStdout(), asJSON, a)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	cmd.Flags().StringVar(&agentCmd, "agent", agent.Claude, "agent a new session starts: claude, codex or grok")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "give up when no agent registered within this")
	return cmd
}

func newAgentCloseCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "close <target>",
		Short: "Close a finished worker this session started: kill its session, remove its worktree and branch",
		Long: `Close a worker this session started with dev agent start, once its work is delivered. Refused,
naming the failed check, unless the worker is idle (or waiting only because it sat idle), its worktree has
no changes to tracked files and its HEAD is in origin's default branch (fetched first). Its untracked,
not ignored files (e.g. RESULT.md) are copied to $XDG_STATE_HOME/dev/results/<session>/ first. Then clients watching it
move to this session, its session is killed, its worktree removed and its branch force-deleted.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" {
				return errors.New("close only closes workers of the session it runs in: run it inside tmux")
			}
			c := tmux.New()
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			done, closeErr := control.Close(c, pane, a)
			// Once files are archived, print where even if a later step failed
			if closeErr != nil && done.Results == "" {
				return closeErr
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return cmp.Or(printJSON(out, done), closeErr)
			}
			if done.Results != "" {
				fmt.Fprintf(out, "Saved %s's untracked files to %s: %s\n", done.Session, done.Results, strings.Join(done.Files, ", "))
			}
			if closeErr != nil {
				return closeErr
			}
			fmt.Fprintf(out, "Closed %s, removed worktree %s", done.Session, done.Worktree)
			if done.Branch != "" {
				fmt.Fprintf(out, " and branch %s", done.Branch)
			}
			fmt.Fprintln(out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	return cmd
}

func newAgentSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the coordinator skill (SKILL.md) that teaches an agent to drive others with dev agent",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprint(cmd.OutOrStdout(), skill.Text)
		},
	}
}
