package cmd

import (
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
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/notify"
	"github.com/ppf/dev/internal/spent"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

var states = []string{agent.Working, agent.Waiting, agent.Idle, agent.Clear, agent.ToolFailed}

func newStateCmd() *cobra.Command {
	var codex bool
	cmd := &cobra.Command{
		Use:   "state <working|waiting|idle|clear|tool-failed>",
		Short: "Claude, Codex or Grok hook: record the agent state on its tmux pane (or a desktop app session's hook file)",
		Args: cobra.MatchAll(cobra.ExactArgs(1), func(_ *cobra.Command, args []string) error {
			if !slices.Contains(states, args[0]) {
				return fmt.Errorf("unknown state %q, want one of %v", args[0], states)
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" && !codex && !desktop.InApp() {
				return nil
			}
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if pane == "" && !codex {
				// A desktop app session: its hook file stands in for the pane's options
				dir, err := desktop.HookDir()
				if err == nil {
					err = desktop.RecordState(dir, args[0], input, time.Now())
				}
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
				}
				out, err := desktopBossTodo(cmd, input)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: todo: "+err.Error())
				}
				if out != "" {
					fmt.Fprintln(cmd.OutOrStdout(), out)
				}
				return nil
			}
			state, ok, err := agent.Resolve(args[0], input)
			if err != nil {
				// A failing hook would show as an error on every Claude event; report and leave the pane alone.
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
				return nil
			}
			c := tmux.New()
			// Grok sets GROK_HOOK_EVENT on every hook, also on the Claude hooks it imports from ~/.claude
			grok := os.Getenv("GROK_HOOK_EVENT") != ""
			var pid string
			if !codex && !grok {
				pid = os.Getenv("CLAUDE_PID")
				nested, err := agent.Nested(c, pane, pid)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if nested {
					return nil
				}
			}
			if !ok || grok && agent.GrokSkip(input) {
				// A subagent's tool call (or Grok's session end): progress for the boss inbox, though not the pane's state
				if pane != "" && !codex {
					if err := agent.Touch(c, pane); err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					}
				}
				return nil
			}
			hook := agent.ParseHook(input)
			if grok {
				stale, err := agent.GrokStale(c, pane, hook)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if stale {
					return nil
				}
			}
			// Restore resumes Claude conversations only: a Codex or Grok id would make it type `claude --resume`
			var session agent.Session
			if !codex && !grok {
				session = agent.HookSession(input)
				hook.Socket, hook.PID = os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"), pid
			}
			if codex {
				if pane, ok, err = agent.CodexPane(c, input); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
					return nil
				}
				if !ok {
					return nil
				}
			}
			var out string
			busy := false
			// Grok reads a Stop hook's stdout as a reply, and is never a boss
			if !codex && !grok {
				// Before Record: a Stop blocked with inbox notes keeps the boss working
				if out, busy, err = inbox.BossHook(c, pane, input, func(s todo.Store) string { return takeOverHint(cmd, s, pane, "", time.Now()) }); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
				}
				// So does a worker's Stop waiting on background tasks: Claude carries on by itself when they end,
				// and @agent_since stays put, so a long wait still makes a stuck note. A coordinator stays idle,
				// as Deliver pastes only into an idle boss and its background waits can outlast its workers
				if !busy && agent.Backgrounded(input) {
					_, boss, err := inbox.Self(c, pane)
					if err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
					}
					busy = !boss
				}
				if busy {
					state = agent.Working
				}
			}
			if state == agent.Clear && !codex && !grok {
				// Before Record unsets the cost: the boss's total keeps the ended conversation's
				if err := spent.Ended(c, pane, session.ID); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: cost: "+err.Error())
				}
			}
			recErr := agent.Record(c, pane, state, session, hook)
			if out != "" {
				// The notes are already out of the inbox: print them even if Record failed. Claude reads a
				// hook's stdout as JSON, so nothing else may go there
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			if recErr != nil {
				return recErr
			}
			if busy {
				// A blocked or backgrounded Stop isn't a finished turn, for notify or for a boss above this one
				return nil
			}
			if err := notify.Run(c, pane, input, os.Getenv("__CFBundleIdentifier")); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: "+err.Error())
			}
			if err := inbox.WorkerHook(c, control.RealClock, pane, input); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "❌ dev state: inbox: "+err.Error())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&codex, "codex", false, "Codex hook: find the pane by the hook's cwd instead of TMUX_PANE")
	return cmd
}

// desktopBossTodo is a desktop app boss's open to do list as hook output, on worker notes (so it ticks what they
// closed), and on resume or /clear with its notes; "" otherwise.
func desktopBossTodo(cmd *cobra.Command, input []byte) (string, error) {
	stderr := cmd.ErrOrStderr()
	var e struct {
		Name   string `json:"hook_event_name"`
		Prompt string `json:"prompt"`
		Source string `json:"source"`
	}
	id := os.Getenv("CLAUDE_CODE_HOST_SESSION_ID")
	if json.Unmarshal(input, &e) != nil || id == "" {
		return "", nil
	}
	if e.Name == "SessionStart" {
		// A new app session has no list yet, and a claude -p the boss runs inherits its host session id
		if e.Source == "startup" {
			return "", nil
		}
		// Its own list, read without its app record: a start takes over nothing, and a worker has no list
		s, err := todo.Desktop(id)
		if err != nil {
			return "", err
		}
		hint := func(s todo.Store) string { return desktopHint(cmd, s, id) }
		return inbox.StartContext(s, func() ([]todo.Session, error) { return todoWorkers(id, stderr) }, hint, e.Source, time.Now())
	}
	if e.Name != "UserPromptSubmit" || !strings.Contains(e.Prompt, "</side-sessions-event>") {
		return "", nil
	}
	s, err := desktopTodo(id, true, stderr)
	if errors.Is(err, errDesktopWorker) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	now := time.Now()
	l, err := s.Read(now)
	if err != nil {
		return "", err
	}
	if err := l.MarkGone(func() ([]todo.Session, error) { return todoWorkers(id, stderr) }); err != nil {
		return "", err
	}
	// Taken last: a reminder that doesn't go out leaves them for the next
	ticked, err := s.TakeTicked(now, nil, nil)
	if err != nil {
		return "", err
	}
	text, err := s.Remind(l, ticked, now)
	if text == "" || err != nil {
		return "", err
	}
	return inbox.PromptContext(text)
}
