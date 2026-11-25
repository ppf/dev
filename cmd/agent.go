package cmd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/skill"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// Exit codes of dev agent besides 0 and 1, for coordinators to branch on.
const (
	exitTimeout = 2
	exitStalled = 3
)

// timedOut turns the timeout of a wait on a working agent into one neutral stdout line that still exits 2: it is
// not failing. Any other error, a timeout in another state included, passes through.
func timedOut(cmd *cobra.Command, st control.Status, err error, timeout time.Duration) error {
	if !errors.Is(err, control.ErrTimeout) || st.Trusted() != agent.Working {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "working: still at it after %s; wait again or read it\n", timeout)
	return fmt.Errorf("%w: %w", open.ErrShown, err)
}

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
pane id (%N), never a sidebar position. Exit status: 0 ok, 1 error, 2 timeout (a neutral line on stdout while it still works), 3 stalled (prompt not picked up).
Run "dev agent skill [dev-agents|desktop-boss]" for a coordinator skill.`,
	}
	cmd.AddCommand(newAgentListCmd(), newAgentGetCmd(), newAgentReadCmd(), newAgentPromptCmd(), newAgentWaitCmd(), newAgentWaitUntilCmd(),
		newAgentSendKeysCmd(), newAgentStartCmd(), newAgentCloseCmd(), newAgentSkillCmd(), newAgentMessageCmd(), newAgentReplyCmd(),
		newAgentMessagesCmd(), newAgentHandoffsCmd(), newAgentClearCmd())
	return cmd
}

func newAgentListCmd() *cobra.Command {
	var asJSON, mine bool
	var boss string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every session's agent: session, pane, agent, state, branch, path, cost",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if boss != "" && !mine {
				return errors.New("--boss needs --mine")
			}
			c := tmux.New()
			agents, err := control.List(c)
			if err != nil {
				return err
			}
			if mine {
				pane, err := callerPane(c, boss)
				if err != nil {
					return err
				}
				if pane == "" {
					return errors.New("--mine lists the workers of the session it runs in: run it inside tmux, or pass --boss <session>")
				}
				if agents, err = control.StartedBy(c, agents, pane); err != nil {
					return err
				}
			}
			return printAgents(cmd.OutOrStdout(), asJSON, agents)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	cmd.Flags().BoolVar(&mine, "mine", false, "only the workers this session (or --boss's) started with dev agent start (its child sessions)")
	addBossFlag(cmd, &boss)
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
			if err := a.ReadInput(c); err != nil {
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
			text, err := control.Read(c, a.Pane, lines, a.Label == "Claude")
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

// addBossFlag adds --boss, naming the boss session a coordinator outside tmux acts as.
func addBossFlag(cmd *cobra.Command, boss *string) {
	cmd.Flags().StringVar(boss, "boss", "", "act as this boss session, not the one it runs in (for a coordinator outside tmux)")
}

// callerPane is the pane of the session dev agent acts as: --boss's session, else its own ("" outside tmux).
func callerPane(c *tmux.Client, boss string) (string, error) {
	if boss == "" {
		return os.Getenv("TMUX_PANE"), nil
	}
	return control.BossPane(c, boss)
}

// outsideBoss reports a --boss coordinator outside tmux, whose workers the boss's own coordinator leaves alone.
func outsideBoss(boss string) bool {
	return boss != "" && os.Getenv("TMUX_PANE") == ""
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

// typeInto is resolveOther for a command that types into the pane, refusing a Claude Code session the manager: a key
// there could make the user's pick in its AskUserQuestion (dev todo answer --via manager).
func typeInto(c *tmux.Client, target string) (control.Agent, error) {
	a, err := resolveOther(c, target)
	if err == nil && a.Session == herd.Manager && os.Getenv("CLAUDECODE") != "" {
		return a, errors.New("refused in a Claude Code session: the manager's prompts are the user's; use dev agent message manager")
	}
	return a, err
}

func newAgentPromptCmd() *cobra.Command {
	var wait bool
	var kind, words, command string
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

--wait needs an idle agent whose dev state hooks run (Claude, Codex, Grok; agy's count the prompt at its
first model call) and returns once the hook saw the prompt and the agent settled again (idle or waiting),
printing that state; another slash command Claude runs itself, once its screen shows it ran (Claude only).
Exit 3 when the prompt isn't picked up within --stall (it may still sit in the input box: read the pane),
exit 2 on --timeout. "/clear" (or /reset, /new) starts no turn: --wait returns once the agent's new
conversation started (its hook recorded a new session). Refused for Codex and Grok, whose hooks record none, and for agy, whose
hook records one only from its first turn.

From the manager's coordinator pane --kind is needed (see dev agent handoffs): work only passes on the user's
latest pick there, typed messages since or not, being "Hand it on" on one question whose first line starts "Hand on to <target>"
and that holds the brief's first line as a line of its own, once per pick; a new hand-on question drops it. Or, with
--go '<the user's words>', on their latest message there when they typed it themselves (by the transcript: not an inbox
batch, another session's or a plugin's prompt, an accepted suggestion, pasted text or a pick) and it holds the words, once
per message, listed as a typed go. Or, with --command '<the user's words>', the same on their plain command, also refused when
that message holds a "?" outside a URL, listed as a command.`,
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
			a, err := typeInto(c, args[0])
			if err != nil {
				return err
			}
			g, err := gateHandOn(c, kind, words, command, a.Session, text)
			if err != nil {
				return err
			}
			if !wait {
				queued, err := control.Prompt(c, control.RealClock, a.Pane, text)
				if queued {
					fmt.Fprintf(cmd.OutOrStdout(), "queued: %s is working; it reads the prompt at its turn's next step\n", a.Session)
				}
				return errors.Join(g.reached(err), g.done(!control.NotSent(err), time.Now()))
			}
			// Recorded once typed: the wait may run long or be cut off
			var gerr error
			st, err := control.PromptWaitSent(c, control.RealClock, a.Pane, text, stall, timeout, func() { gerr = g.done(true, time.Now()) })
			if control.NotSent(err) {
				gerr = g.done(false, time.Now())
			}
			// A gated hand-on keeps its error (it says the brief reached the pane), and so does a timeout before the stall
			// check could confirm the pickup
			if gerr == nil && (g == nil || g.pick == nil) && timeout >= stall {
				err = timedOut(cmd, st, err, timeout)
			}
			if err != nil || gerr != nil {
				return errors.Join(g.reached(err), gerr)
			}
			fmt.Fprintln(cmd.OutOrStdout(), st.Trusted())
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the agent finished its turn")
	cmd.Flags().StringVar(&kind, "kind", "", kindHelp)
	cmd.Flags().StringVar(&words, "go", "", goHelp)
	cmd.Flags().StringVar(&command, "command", "", commandHelp)
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
				return timedOut(cmd, st, err, timeout)
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
Claude fires no hook for that. A worker's decision dev handed to the manager (its note ends "leave it to the
manager") takes a Claude's keys only from the manager's coordinator pane until it is answered, while the manager runs;
the user's own, outside Claude Code, still pass.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c := tmux.New()
			a, err := typeInto(c, args[0])
			if err != nil {
				return err
			}
			if err := gateSendKeys(c, a); err != nil {
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

// addStartFlags adds --model and --effort; who names what they start, for the flag help.
func addStartFlags(cmd *cobra.Command, start *agent.Start, who string) {
	cmd.Flags().StringVar(&start.Model, "model", "", "model a new "+who+" starts with (claude --model: an alias or a model id); default: the user's settings")
	cmd.Flags().StringVar(&start.Effort, "effort", "", "effort a new "+who+" starts with: "+strings.Join(agent.Efforts, ", ")+"; default: the user's settings")
}

// checkStart refuses a --effort not in efforts and a --model that is empty, has whitespace or a control
// character, starts with "-" (the agent would take it for a flag) or ends in ";" (tmux's command separator): it is
// typed into a shell and kept in tmux options and the tab-separated resurrect state.
func checkStart(cmd *cobra.Command, start agent.Start, efforts []string) error {
	if cmd.Flags().Changed("effort") && !slices.Contains(efforts, start.Effort) {
		return fmt.Errorf("unknown --effort %q, want %s", start.Effort, strings.Join(efforts, ", "))
	}
	blank := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }
	if m := start.Model; cmd.Flags().Changed("model") && (m == "" || strings.ContainsFunc(m, blank) || strings.HasPrefix(m, "-") || strings.HasSuffix(m, ";")) {
		return fmt.Errorf("invalid --model %q, want a model alias or id", start.Model)
	}
	return nil
}

func newAgentStartCmd() *cobra.Command {
	var asJSON bool
	var agentCmd, boss, from string
	var start agent.Start
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "start <project> [worktree] | --from <worker>",
		Short: "Open a project (or its worktree) in the background and wait until its agent is registered",
		Long: `Open a project's session, or its worktree's (created when missing), without attaching to it, and
wait until its agent's first hook registers it (a Codex or Grok pane counts once its CLI runs, so read its
screen before the first prompt). An agy pane counts once its input box shows, recorded idle (agy runs no hook
before its first turn); dev confirms its trust dialog only in a worktree this start made, and leaves any other
screen (a login, a dialog) to the user, saying so on stderr. An existing session is reused; one running another
agent than an explicit --agent is an error. A new session started from a tmux pane, or with --boss, is that
session's worker.
--model and --effort start a new session's Claude (or agy) with them (list --json and get --json show them, and
a Claude keeps them when dev restarts it); agy's --model must be one agy models lists. With an existing session
they are an error unless they equal what it was started with, and with --agent grok always. With --agent codex
they start codex with -m and -c model_reasoning_effort= (efforts: ` + strings.Join(agent.CodexEfforts, ", ") + `);
--model must be one codex debug models lists, and --effort one that model takes (a new Codex session's --effort
needs --model).
--from <worker> hands a worker's half-done branch to a fresh Claude: refused unless this session (or
--boss's) started the worker, it is idle (or waiting only because it sat idle) and no other session has a pane
in its worktree. Its session is closed without dev agent close's delivered and changes checks, its worktree,
branch and uncommitted changes kept, and a new session of the same name starts there with --model and
--effort; its to do items stay linked to it. It takes no <project> or [worktree].
Prints the agent like get (with --from, --json adds took_over: session, worktree, branch, base, ahead, dirty, transcript).`,
		Args:              cobra.RangeArgs(0, 2),
		ValidArgsFunction: completeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if from != "" && len(args) > 0 {
				return fmt.Errorf("--from takes no <project> or [worktree]: the new session takes %s's", from)
			}
			if from == "" && len(args) == 0 {
				return errors.New("requires <project> [worktree], or --from <worker>")
			}
			if !slices.Contains([]string{agent.Claude, agent.Codex, agent.Grok, agent.Agy}, agentCmd) {
				return fmt.Errorf("unknown --agent %q, want claude, codex, grok or agy", agentCmd)
			}
			efforts := agent.Efforts
			if agentCmd == agent.Codex {
				efforts = agent.CodexEfforts
			}
			if err := checkStart(cmd, start, efforts); err != nil {
				return err
			}
			if start != (agent.Start{}) && agentCmd != agent.Claude && agentCmd != agent.Codex && agentCmd != agent.Agy {
				return fmt.Errorf("--model and --effort only apply to Claude, Codex and agy, not --agent %s", agentCmd)
			}
			if from != "" {
				if agentCmd != agent.Claude {
					return fmt.Errorf("--from starts a fresh Claude, not --agent %s", agentCmd)
				}
				return startFrom(cmd, from, boss, start, timeout, asJSON)
			}
			wt, err := worktreeArg(args)
			if err != nil {
				return err
			}
			c, session := tmux.New(), sidebar.SessionName(args[0], wt)
			pane, err := callerPane(c, boss)
			if err != nil {
				return err
			}
			_, missing := c.Run("has-session", "-t", tmux.SessionTarget(session))
			// Before openProject, which would fetch for the worktree first
			if missing == nil {
				if err := start.CheckExisting(c, session); err != nil {
					return err
				}
			} else if agentCmd == agent.Codex && start.Effort != "" && start.Model == "" {
				// The default model is codex's to pick, and the levels differ per model
				return errors.New("--effort for --agent codex needs --model: each model takes its own efforts")
			} else if agentCmd == agent.Codex && start.Model != "" {
				if err := checkCodexStart(start, timeout); err != nil {
					return err
				}
			}
			// A new agy session: its --model checked, and whether this start makes its worktree, where alone dev
			// confirms agy's trust dialog
			made := false
			if missing != nil && agentCmd == agent.Agy {
				if start.Model != "" {
					if err := checkAgyModel(start.Model, timeout); err != nil {
						return err
					}
				}
				if made, err = worktreeMissing(cmd, args[0], wt); err != nil {
					return err
				}
			}
			// A herd's x only kills workers its coordinator started, never a reused session; workers start
			// without Remote Control
			worker := missing != nil && pane != ""
			if err := openProject(cmd, args[0], wt, true, worker, agentCmd, boss, start); err != nil {
				return err
			}
			if worker {
				if err := herd.MarkStartedBy(c, session, pane, outsideBoss(boss)); err != nil {
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
			// Also a reused agy session that got no state yet: its start left a screen to the user
			if a.Label == "Agy" && a.State == "" {
				hooked, herr := agyHooked(a.Path)
				if herr != nil {
					// Read as no hooks: its start records nothing, and says so
					fmt.Fprintln(cmd.ErrOrStderr(), "⚠ "+herr.Error())
				}
				left, err := control.AgyReady(c, control.RealClock, a.Pane, made, hooked, timeout)
				if err != nil {
					return err
				}
				if left != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "⚠ "+left)
				}
				if a, err = control.Resolve(c, session); err != nil {
					return err
				}
			}
			return printAgent(cmd.OutOrStdout(), asJSON, a)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON object")
	cmd.Flags().StringVar(&agentCmd, "agent", agent.Claude, "agent a new session starts: claude, codex, grok or agy")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "give up when no agent registered within this")
	cmd.Flags().StringVar(&from, "from", "", "worker whose worktree and branch a fresh session takes over (closes the worker, keeps its work)")
	addBossFlag(cmd, &boss)
	addStartFlags(cmd, &start, "session's Claude")
	return cmd
}

// checkAgyModel refuses a --model that agy models doesn't list within timeout: agy would start on its stored
// model instead, saying so only on its screen.
func checkAgyModel(model string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, agent.Agy, "models").Output()
	if e, ok := errors.AsType[*exec.ExitError](err); ok && len(e.Stderr) > 0 {
		return fmt.Errorf("checking --model: agy models: %s", strings.TrimSpace(string(e.Stderr)))
	}
	if err != nil {
		return fmt.Errorf("checking --model: agy models: %w", err)
	}
	models := agent.AgyModels(string(out))
	if models == nil {
		return fmt.Errorf("checking --model: agy models listed no model: %q", strings.TrimSpace(string(out)))
	}
	if !slices.Contains(models, model) {
		return fmt.Errorf("unknown --model %q for agy, want one agy models lists: %s", model, strings.Join(models, ", "))
	}
	return nil
}

// agyHooked reports agy's global hooks file or the workspace's at dir running dev state --agy at a turn's start
// (PreInvocation) and end (Stop), enabled: else no turn would count, and prompt --wait would stall. dev only reads
// them; one it can't read or parse is err, the other still read.
func agyHooked(dir string) (hooked bool, err error) {
	files := []string{filepath.Join(dir, ".agents", "hooks.json")}
	if home, herr := os.UserHomeDir(); herr == nil {
		files = append(files, filepath.Join(home, ".gemini", "config", "hooks.json"))
	}
	found := map[string]bool{}
	for _, f := range files {
		raw, rerr := os.ReadFile(f)
		if errors.Is(rerr, fs.ErrNotExist) {
			continue
		}
		events, perr := agyStateHooks(raw)
		if perr = cmp.Or(rerr, perr); perr != nil {
			err = errors.Join(err, fmt.Errorf("agy hooks file %s: %w", f, perr))
		}
		for _, event := range events {
			found[event] = true
		}
	}
	return found["PreInvocation"] && found["Stop"], err
}

// agyStateHooks are the events an agy hooks file (named groups of events, each a list of hooks or of matchers
// holding hooks) runs an enabled dev state --agy hook for; a group, matcher or hook "enabled": false runs none.
// Other keys and values that aren't such lists are skipped; a file that isn't a JSON object is an error.
func agyStateHooks(raw []byte) ([]string, error) {
	type hook struct {
		Command string          `json:"command"`
		Enabled json.RawMessage `json:"enabled"`
		Hooks   json.RawMessage `json:"hooks"`
	}
	on := func(enabled json.RawMessage) bool { return string(enabled) != "false" }
	// The entries of a list that are hooks; none for a value that isn't a list
	hooks := func(list json.RawMessage) []hook {
		var raws []json.RawMessage
		_ = json.Unmarshal(list, &raws)
		var hs []hook
		for _, raw := range raws {
			var h hook
			if json.Unmarshal(raw, &h) == nil {
				hs = append(hs, h)
			}
		}
		return hs
	}
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, err
	}
	if groups == nil {
		return nil, errors.New("null, not an object")
	}
	var events []string
	for _, rawGroup := range groups {
		var group map[string]json.RawMessage
		if json.Unmarshal(rawGroup, &group) != nil || !on(group["enabled"]) {
			continue
		}
		for event, list := range group {
			if slices.ContainsFunc(hooks(list), func(h hook) bool {
				return on(h.Enabled) && slices.ContainsFunc(append(hooks(h.Hooks), h), func(h hook) bool {
					return on(h.Enabled) && strings.Contains(h.Command, "state --agy")
				})
			}) {
				events = append(events, event)
			}
		}
	}
	return events, nil
}

// worktreeMissing reports that project's worktree wt isn't on disk, so the start that follows makes it; false
// for the main checkout (no wt) and an unknown project, which openProject reports.
func worktreeMissing(cmd *cobra.Command, project, wt string) (bool, error) {
	if wt == "" {
		return false, nil
	}
	projects, _, err := loadProjects(cmd)
	if err != nil {
		return false, err
	}
	p, ok := config.Find(projects, project)
	if !ok {
		return false, nil
	}
	dir, err := p.Dir()
	if err != nil {
		return false, err
	}
	root, ok := worktree.Root(dir)
	if !ok {
		return false, nil
	}
	_, err = os.Stat(filepath.Join(root, ".claude", "worktrees", wt))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// checkCodexStart refuses a --model codex's model catalog doesn't list, and a --effort that model doesn't take:
// codex would start anyway, on an unlisted model with only a warning on its screen, on an effort the model
// doesn't take silently on another model or effort.
func checkCodexStart(start agent.Start, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, agent.Codex, "debug", "models").Output()
	if ctx.Err() != nil {
		return fmt.Errorf("checking --model and --effort: codex debug models didn't answer within %s", timeout)
	}
	if e, ok := errors.AsType[*exec.ExitError](err); ok && len(e.Stderr) > 0 {
		return fmt.Errorf("checking --model and --effort: codex debug models: %s", strings.TrimSpace(string(e.Stderr)))
	}
	if err != nil {
		return fmt.Errorf("checking --model and --effort: codex debug models: %w", err)
	}
	models, err := agent.CodexModels(out)
	if err != nil {
		return err
	}
	efforts, ok := models[start.Model]
	if !ok {
		return fmt.Errorf("unknown --model %q for codex, want one codex debug models lists: %s", start.Model, strings.Join(slices.Sorted(maps.Keys(models)), ", "))
	}
	if start.Effort != "" && len(efforts) == 0 {
		return fmt.Errorf("--model %s takes no --effort", start.Model)
	}
	if start.Effort != "" && !slices.Contains(efforts, start.Effort) {
		return fmt.Errorf("--model %s doesn't take --effort %s, want %s", start.Model, start.Effort, strings.Join(efforts, ", "))
	}
	return nil
}

// startFrom is dev agent start --from: worker from's session closed by the coordinator in this (or boss's) pane,
// keeping its worktree, and a fresh Claude started in it under the same name, with the worker's to do items.
func startFrom(cmd *cobra.Command, from, boss string, start agent.Start, timeout time.Duration, asJSON bool) error {
	c := tmux.New()
	pane, err := callerPane(c, boss)
	if err != nil {
		return err
	}
	if pane == "" {
		return errors.New("--from takes over workers of the session it runs in: run it inside tmux, or pass --boss <session>")
	}
	a, err := resolveOther(c, from)
	if err != nil {
		return err
	}
	if a.Outside && !outsideBoss(boss) {
		return fmt.Errorf("%s was started by a coordinator outside tmux (--boss): leave it to that coordinator", a.Session)
	}
	took, err := control.TakeOver(c, pane, a, func(name string) (string, error) {
		projects, path, err := loadProjects(cmd)
		if err != nil {
			return "", err
		}
		p, ok := config.Find(projects, name)
		if !ok {
			return "", fmt.Errorf("%s's project %q isn't in %s", a.Session, name, path)
		}
		return p.Dir()
	})
	stderr := cmd.ErrOrStderr()
	branch := "detached HEAD"
	if took.Branch != "" {
		branch = "branch " + took.Branch
	}
	// failed is err after the worker's session closed, naming the plain start that brings it back
	failed := func(err error) error {
		retry := []string{"dev", "agent", "start", a.Project, took.Name}
		for _, f := range [][2]string{{"--model", start.Model}, {"--effort", start.Effort}, {"--boss", boss}} {
			if f[1] != "" {
				retry = append(retry, f[0], f[1])
			}
		}
		msg := fmt.Sprintf("closed %s and kept its worktree and %s, but the new session failed: retry with\n   %s", a.Session, branch, shellLine(retry))
		if _, gone := c.Run("has-session", "-t", tmux.SessionTarget(a.Session)); gone == nil {
			// A retry would only wait on it again
			msg = fmt.Sprintf("closed %s and kept its worktree and %s, but the new session's agent didn't register: answer its screen (dev agent read %s), "+
				"or have the user close it (× → keep worktree) and retry with\n   %s", a.Session, branch, a.Session, shellLine(retry))
		}
		// open.Run printed err already, so Execute prints nothing
		if errors.Is(err, open.ErrShown) {
			fmt.Fprintln(stderr, "❌ "+msg)
		}
		return fmt.Errorf("%s: %w", msg, err)
	}
	if errors.Is(err, control.ErrKilled) {
		return failed(err)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "ℹ  Taking over %s (%s, %s): %s, %s ahead of %s, %s (kept)\n", a.Session, cmp.Or(a.Model, "default model"),
		cmp.Or(a.Effort, "default effort"), branch, plural(took.Ahead, "commit"), took.Base, plural(len(took.Dirty), "uncommitted file"))
	fmt.Fprintf(stderr, "Closed %s, kept worktree %s\n", a.Session, took.Worktree)
	if took.Transcript != "" {
		fmt.Fprintf(stderr, "Handover: its transcript %s\n", took.Transcript)
	}
	b, err := startTaken(cmd, c, pane, a.Project, took, boss, start, timeout)
	if err != nil {
		return failed(err)
	}
	if asJSON {
		return printJSON(cmd.OutOrStdout(), struct {
			control.Agent
			TookOver control.TookOver `json:"took_over"`
		}{b, took})
	}
	return printAgent(cmd.OutOrStdout(), false, b)
}

// startTaken starts the session took closed again in its worktree, as the worker of the coordinator in pane. Its
// to do items stay linked: a tmux worker's link is its session name.
func startTaken(cmd *cobra.Command, c *tmux.Client, pane, project string, took control.TookOver, boss string, start agent.Start, timeout time.Duration) (control.Agent, error) {
	target := tmux.SessionTarget(took.Session)
	if _, err := c.Run("has-session", "-t", target); err == nil {
		return control.Agent{}, fmt.Errorf("a session %s was opened again meanwhile, not by this takeover", took.Session)
	}
	openErr := openProject(cmd, project, took.Name, true, true, agent.Claude, boss, start)
	// Also one open.Run made before failing, which a retry reuses; never one another coordinator started meanwhile
	if by, err := c.Run("show-option", "-qv", "-t", target, herd.OptStartedBy); err == nil && strings.TrimSpace(by) == "" {
		if err := herd.MarkStartedBy(c, took.Session, pane, outsideBoss(boss)); err != nil {
			return control.Agent{}, errors.Join(openErr, err)
		}
	}
	if openErr != nil {
		return control.Agent{}, openErr
	}
	return control.AwaitAgent(c, control.RealClock, took.Session, timeout)
}

// shellLine joins words into a command line to paste into a shell, quoting only the words that need it.
func shellLine(words []string) string {
	plain := func(r rune) bool {
		return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./=:@%+,", r))
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = w
		if w == "" || strings.ContainsFunc(w, func(r rune) bool { return !plain(r) }) {
			quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// plural is n with one, made plural with an s unless n is 1.
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func newAgentCloseCmd() *cobra.Command {
	var asJSON bool
	var boss string
	cmd := &cobra.Command{
		Use:   "close <target>",
		Short: "Close a finished worker this session (or --boss's) started, or nobody did: kill its session, remove its worktree and branch",
		Long: `Close a worker this session (or --boss's) started with dev agent start, once its work is delivered. A worker nobody
started (from a plain terminal or the desktop app) is closed by any coordinator, or from a plain terminal; one another
session started (or whose starter is gone) is refused. From a plain terminal, clients watching it are detached.
Refused, naming the failed check, unless the worker is idle (or waiting only because it sat idle), its worktree has
no changes to tracked files and its HEAD is in origin's default branch (fetched first; without an origin remote, the
branch checked out in the main checkout), or is the head commit of a PR (gh) or MR (glab) found through origin's
repository (on GitHub also origin's PR to its upstream) that is open, or merged into that branch. A worker started
with --boss outside tmux is refused unless closed that way.
Its untracked, not ignored files (e.g. RESULT.md) are copied to $XDG_STATE_HOME/dev/results/<session>/ first. A
RESULT.md that git ignores is refused unless that folder already holds the same text: copy it there, then close again.
Then clients watching it move to this (or --boss's) session, its session is killed, its worktree removed and its
branch force-deleted.
A boss (dev boss) is closed only by the manager that started or adopted it, as the sidebar's × closes it: refused
unless it is idle, no session it started is left and its to do list has no open ask; its list stays.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.New()
			pane, err := callerPane(c, boss)
			if err != nil {
				return err
			}
			a, err := resolveOther(c, args[0])
			if err != nil {
				return err
			}
			if pane == "" && (a.StartedBy != "" || a.Boss) {
				return errors.New("close only closes workers of the session it runs in (or ones nobody started): run it inside tmux, or pass --boss <session>")
			}
			if a.Outside && !outsideBoss(boss) {
				return fmt.Errorf("%s was started by a coordinator outside tmux (--boss): leave it to that coordinator", a.Session)
			}
			if a.Boss {
				return closeBoss(cmd, c, pane, a, asJSON)
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
	addBossFlag(cmd, &boss)
	return cmd
}

// closeBoss is dev agent close of boss a, run in pane, the manager's.
func closeBoss(cmd *cobra.Command, c *tmux.Client, pane string, a control.Agent, asJSON bool) error {
	own, err := c.Run("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(own) != herd.Manager {
		return fmt.Errorf("%s is a boss: only the manager closes a boss; the user closes it with × on its sidebar row", a.Session)
	}
	if err := control.CloseBoss(c, pane, a); err != nil {
		return err
	}
	if asJSON {
		return printJSON(cmd.OutOrStdout(), control.Closed{Session: a.Session})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Closed %s; its to do list stays\n", a.Session)
	return nil
}

func newAgentSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skill [dev-agents|desktop-boss]",
		Short: "Print a coordinator skill (SKILL.md): dev-agents (default) drives others with dev agent, desktop-boss with the desktop app's tools",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := skill.Default
			if len(args) == 1 {
				name = args[0]
			}
			text, err := skill.Text(name)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		},
	}
}
