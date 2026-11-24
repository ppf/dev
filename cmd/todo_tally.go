package cmd

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

func newTodoTallyCmd(boss *string) *cobra.Command {
	var days int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "tally",
		Short: "Count asks per boss list and the manager's own over the last days: answered, settled, overruled, open",
		Long: `Count, for each boss's list (closed bosses' too) and the manager's own, over the last --days (7) days:
  answered: asks put to the user and answered by them
  boss settled: asks the boss settled itself (dev todo ask --decided)
  manager marked / manager unmarked: asks the manager settled, its boss's marked obvious / unmarked, for a --why
  overruled boss / manager marked / manager unmarked: settled asks the user overruled, of each kind
  open: asks open now, whenever put (needs a running tmux server, tmux bosses' lists only: a desktop app boss's events count, its open asks don't)
and a total line. An overruled ask counts as overruled and, as it was, settled, never as answered. The counts come from an append-only log,
one line per event (time, list, item number and text hash, kind, and for an overrule the kind overruled), kept under the dev state directory in todo/tally.jsonl;
it starts at the release that ships it, so a window reaching further back counts only from its first line, which the
output names. The log is per dev state directory, not per tmux server. A test item (--quiet) is left out. The manager's hand-on tally is another count: dev agent handoffs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if *boss != "" {
				return errors.New("tally counts every list: leave out --boss")
			}
			if days < 1 {
				return fmt.Errorf("--days is %d: at least 1", days)
			}
			return printTally(cmd.OutOrStdout(), cmd.ErrOrStderr(), tmux.New(), days, asJSON, time.Now())
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "how many days back to count")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {days, since, recorded_since, lists: [{boss, answered, boss_settled, manager_marked, manager_unmarked, overruled_boss, overruled_manager_marked, overruled_manager_unmarked, open}], total} (unix seconds; recorded_since 0 before the first event)")
	return cmd
}

type tallyOut struct {
	Days          int              `json:"days"`
	Since         int64            `json:"since"`
	RecordedSince int64            `json:"recorded_since"`
	Lists         []todo.ListCount `json:"lists"`
	Total         todo.Count       `json:"total"`
}

// printTally prints the tally of the last days days at now; a list that can't be read is reported on stderr, its open
// asks not counted.
func printTally(out, stderr io.Writer, c *tmux.Client, days int, asJSON bool, now time.Time) error {
	events, err := todo.ReadEvents()
	if err != nil {
		return err
	}
	lists, err := todo.All(c, now, nil, true, false)
	if err != nil {
		return err
	}
	open := map[string]int{}
	for _, l := range lists {
		if l.Err != nil {
			fmt.Fprintf(stderr, "⚠ dev todo: %s's list not read, its open asks not counted: %v\n", l.Boss, l.Err)
			continue
		}
		if n := l.OpenAsks(l.Boss); n > 0 {
			open[l.Boss] = n
		}
	}
	since := now.AddDate(0, 0, -days).Unix()
	rows, total := todo.Tally(events, open, since)
	t := tallyOut{Days: days, Since: since, Lists: rows, Total: total}
	if rows == nil {
		t.Lists = []todo.ListCount{}
	}
	for _, e := range events {
		if t.RecordedSince == 0 || e.At < t.RecordedSince {
			t.RecordedSince = e.At
		}
	}
	if asJSON {
		return printJSON(out, t)
	}
	from := time.Unix(since, 0).Format("2006-01-02 15:04")
	switch {
	case t.RecordedSince == 0:
		fmt.Fprintf(out, "last %d days (since %s): no events recorded yet, counts start at the release that ships this; open asks only\n", days, from)
	case t.RecordedSince > since:
		fmt.Fprintf(out, "last %d days (since %s): counted from %s, when recording began\n", days, from, time.Unix(t.RecordedSince, 0).Format("2006-01-02 15:04"))
	default:
		fmt.Fprintf(out, "last %d days (since %s)\n", days, from)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "list\tanswered\tboss settled\tmanager marked\tmanager unmarked\toverruled boss\toverruled manager marked\toverruled manager unmarked\topen")
	for _, r := range append(rows, todo.ListCount{Boss: "total", Count: total}) {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", r.Boss, r.Answered, r.BossSettled, r.ManagerMarked, r.ManagerUnmarked, r.OverruledBoss, r.OverruledMarked, r.OverruledUnmark, r.Open)
	}
	return w.Flush()
}
