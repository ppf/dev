package inbox

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// LeaveOpen is the option an item's question offers beside its one option: picked, the item stays open.
const LeaveOpen = "Leave it open"

// itemQuestion matches an item's question in the manager's dialog: its first line "<project> #<n>: <item text>".
var itemQuestion = regexp.MustCompile(`^\S+ #\d+: `)

// compactOwed is owedLines of the coordinator in pane, "" for none.
func compactOwed(c *tmux.Client, pane string, now time.Time) (string, error) {
	b, ok, err := Coordinator(c, pane)
	if !ok || err != nil {
		return "", err
	}
	return owedLines(b.Box, now)
}

// owedLines is what the coordinator of inbox b still owes that a compaction's summary may lose, one line each: the user's
// pick not yet relayed on an item or a hand-on question (each question of several on its own), with what they typed
// since, and the items marked asked in it now (dev todo asking); "" for none. Only the manager has picks and marks. A
// failed read leaves out only its own lines.
func owedLines(b Box, now time.Time) (string, error) {
	var lines []string
	pick, perr := b.LatestPick()
	switch {
	case perr != nil || pick == nil || !pick.Pick:
	case pick.HandOnMixed:
		lines = append(lines, "- The user's last dialog held a hand-on question beside others: nothing in it is relayed; ask its items again, the hand-on question on its own.")
	case len(pick.Asks) == 0:
		lines = append(lines, owedPick(Asked{Question: pick.Question, Answer: pick.Answer, Typed: pick.Typed, Spent: pick.Spent})...)
	default:
		for _, a := range pick.Asks {
			lines = append(lines, owedPick(a)...)
		}
	}
	if len(lines) > 0 {
		// A message typed since may withdraw or change the pick, and the summary may have lost it
		if typed, err := b.UserPrompt(); err != nil {
			perr = err
		} else if typed != nil && !typed.Pick && typed.At >= pick.At {
			lines = append(lines, fmt.Sprintf("- The user typed after that pick: %q: if it withdraws or changes the pick, ask again instead.", typed.Text))
		}
	}
	asking, aerr := b.Asking(now)
	for _, a := range asking {
		lines = append(lines, fmt.Sprintf("- %s #%d %q is marked asked in manager now, with no pick since: re-check it (dev todo show --print) and ask it again with AskUserQuestion if still open.", a.Boss, a.ID, a.Text))
	}
	err := errors.Join(perr, aerr)
	if len(lines) == 0 {
		return "", err
	}
	return "[dev] Owed from before the compaction:\n" + strings.Join(lines, "\n"), err
}

// owedPick is the line for a, one question of the user's pick, when it is owed: unspent, answered, on an item (other
// than LeaveOpen, as dev todo answer takes it) or a hand-on question (other than DropIt).
func owedPick(a Asked) []string {
	line := FirstLine(a.Question)
	switch {
	case a.Spent || a.Answer == "":
	case IsHandOn(a.Question, nil):
		switch {
		case !a.Typed && IsPicked(a.Answer, HandItOn):
			return []string{fmt.Sprintf("- The user picked %q on this hand-on question, not sent yet: send the brief it holds with --kind work: %q", HandItOn, a.Question)}
		case a.Typed || !IsPicked(a.Answer, DropIt):
			return []string{fmt.Sprintf("- The user corrected the hand-on question %q with %q: revise and ask again.", line, a.Answer)}
		}
	case itemQuestion.MatchString(line) && !strings.EqualFold(a.Answer, LeaveOpen):
		return []string{fmt.Sprintf("- The user picked %q on %q, not relayed yet: relay it with dev todo answer --via manager.", a.Answer, line)}
	}
	return nil
}
