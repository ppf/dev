package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/desktop"
)

func newDesktopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "desktop",
		Short: "Commands for a boss session in the Claude desktop app",
	}
	cmd.AddCommand(newDesktopBoardCmd())
	return cmd
}

func newDesktopBoardCmd() *cobra.Command {
	var asJSON bool
	var boss string
	cmd := &cobra.Command{
		Use:   "board",
		Short: "List a desktop boss session's workers: state, branch, PRs, context use",
		Long: `List the unarchived sessions a Claude desktop app session started (its workers): live state
(busy, blocked, idle, stopped), title, branch, PRs and the share of the context window in use. --json adds
the app's verdict on the last turn and what it needs, the last lines of the newest assistant text and the
tool in flight. Reads the app's session records, ~/.claude/sessions, the transcripts and Claude Code's model
catalog; a file that fails to parse is reported on stderr.`,
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
			workers, warnings, err := desktop.Board(desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR")), boss)
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
