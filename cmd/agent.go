package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
		newAgentSendKeysCmd(), newAgentStartCmd(), newAgentSkillCmd())
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
			a, err := control.Resolve(tmux.New(), args[0])
			if err != nil {
				return err
			}
			return printAgent(cmd.OutOrStdout(), asJSON, a)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	return cmd
}

// printAgents prints agents as a JSON array or as tab-separated rows (state and cost "-" when unknown).
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
		if a.CostUSD != nil {
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
prompt or question is refused: the text would answer it (use send-keys).

--wait (Claude only) needs an idle agent and returns once the hook saw the prompt and the agent settled
again (idle or waiting), printing that state. Exit 3 when the prompt isn't picked up within
--stall (it may still sit in the input box: read the pane), exit 2 on --timeout.`,
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
				return control.Prompt(c, control.RealClock, a.Pane, text)
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
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			return control.SendKeys(c, a.Pane, args[1:])
		},
	}
}

func newAgentStartCmd() *cobra.Command {
	var asJSON bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "start <project> [worktree]",
		Short: "Open a project (or its worktree) in the background and wait until its agent is registered",
		Long: `Open a project's session, or its worktree's (created when missing), without attaching to it, and
wait until its agent's first hook registers it. An existing session is reused. Prints the agent like get.`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wt, err := worktreeArg(args)
			if err != nil {
				return err
			}
			c, session := tmux.New(), open.SessionName(args[0], wt)
			_, missing := c.Run("has-session", "-t", tmux.SessionTarget(session))
			if err := openProject(cmd, args[0], wt, true); err != nil {
				return err
			}
			// A herd's x only kills workers its coordinator started, never a reused session
			if pane := os.Getenv("TMUX_PANE"); missing != nil && pane != "" {
				if err := herd.MarkStartedBy(c, session, pane); err != nil {
					return err
				}
			}
			a, err := control.AwaitAgent(c, control.RealClock, session, timeout)
			if err != nil {
				return err
			}
			return printAgent(cmd.OutOrStdout(), asJSON, a)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "give up when no agent registered within this")
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
