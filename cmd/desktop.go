package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/desktop"
)

func newDesktopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "desktop",
		Short: "Commands for a boss session in the Claude desktop app",
	}
	cmd.AddCommand(newDesktopBoardCmd(), newDesktopAnswerCmd())
	return cmd
}

func newDesktopAnswerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "answer <local_id> <tool_use_id>",
		Short: "Answer a desktop worker's open question with the person's pick",
		Long: `Write the person's answers to a desktop worker's open AskUserQuestion, for its dev-board mod to
return as the answer: stdin is a JSON object of question text to the chosen label (a multi-select's
labels comma-joined). Refused once the worker no longer waits on that tool use (answered in its own
dialog, or another question asked since).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var answers map[string]string
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&answers); err != nil {
				return fmt.Errorf("answers on stdin: %w", err)
			}
			hooks, err := desktop.HookDir()
			if err != nil {
				return err
			}
			return desktop.WriteAnswer(desktop.QuestionsDir(hooks), args[0], args[1], answers)
		},
	}
}

func newDesktopBoardCmd() *cobra.Command {
	var asJSON bool
	var boss string
	cmd := &cobra.Command{
		Use:   "board",
		Short: "List a desktop boss session's workers: state, branch, PRs, context use",
		Long: `List the unarchived sessions a Claude desktop app session started (its workers): live state
(busy, blocked, idle, stopped), title, branch, PRs and the share of the context window in use. --json adds
the app's verdict on the last turn and what it needs, the last lines of the newest assistant text, the
tool in flight, the question its open dialog asks (from its dev-board mod's questions/ file, which dev
desktop answer answers, else its transcript) or the wrap_up ask its last turn ended on (listening while its
mod's fresh questions/<id>.listen heartbeat names it: its reply file is then submitted as its next prompt), a finer status (needs you, rate-limited, stuck, working,
waiting, blocked, review ready, idle, stopped) and what it says, how long it has been in its state, its task list (update_plan) and health
notes, each with a key that stays the same while its condition holds: stuck (busy,
no progress for 15 min), a decision unanswered for 3 min, context at 80% or more, a rate limit, and its
resume once the limit reset. Reads the app's session records, ~/.claude/sessions, the transcripts, Claude
Code's model catalog and the files dev state and dev question write in desktop sessions (desktop/ in
dev's state directory); a file that fails to parse is reported on stderr, as is a dev-board mod
(CLAUDE_CODE_PLUGIN_DIRS) changed on disk since the boss's Claude started: it runs the copy it loaded then
(not checked with CLAUDE_CODE_PLUGIN_DIR_WATCH=1, which reloads it).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if boss == "" {
				boss = os.Getenv("CLAUDE_CODE_HOST_SESSION_ID")
			}
			if boss == "" {
				return errors.New("no boss session: pass --boss <local_id> or run in a desktop app session")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			p := desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR"))
			if p.Hooks, err = desktop.HookDir(); err != nil {
				return err
			}
			p.Questions = desktop.QuestionsDir(p.Hooks)
			p.Mod = desktop.ModDir(os.Getenv("CLAUDE_CODE_PLUGIN_DIRS"), os.Getenv("CLAUDE_CODE_PLUGIN_DIR_WATCH") == "1")
			workers, warnings, err := desktop.Board(p, boss, time.Now())
			for _, w := range warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev desktop board: "+w.Error())
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(workers)
			}
			for _, w := range workers {
				fmt.Fprintln(out, boardLine(w))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array, one object per worker")
	cmd.Flags().StringVar(&boss, "boss", "", "the boss's app session id, local_… (default $CLAUDE_CODE_HOST_SESSION_ID)")
	return cmd
}

// boardLine is one worker as text: state, title, branch, PRs and context use.
func boardLine(w desktop.Worker) string {
	parts := []string{w.State, w.Title}
	if w.Branch != "" {
		parts = append(parts, w.Branch)
	}
	for _, pr := range w.PRs {
		parts = append(parts, fmt.Sprintf("PR #%d %s", pr.Number, strings.ToLower(pr.State)))
	}
	if w.Context != nil {
		parts = append(parts, fmt.Sprintf("context %d%%", *w.Context))
	}
	return strings.Join(parts, "\t")
}
