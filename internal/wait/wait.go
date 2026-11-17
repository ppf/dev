// Package wait is a worker's planned wait (dev agent wait-until), kept in its pane options or hook file, so a boss
// doesn't report it stuck and it's woken then, with no background sleep.
package wait

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ppf/dev/internal/tmux"
)

// Pane options of a worker's planned wait: when it ends (unix seconds), why, and its @agent_prompts when it was
// set ("" after a restore, which doesn't compare them).
const (
	OptUntil   = "@dev_wait_until"
	OptReason  = "@dev_wait_reason"
	OptPrompts = "@dev_wait_prompts"
)

// Max is the longest wait: a longer one could hide a real problem.
const Max = 4 * time.Hour

// Wait is a worker's planned wait; the zero Wait is none.
type Wait struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// Held reports a wait.
func (w Wait) Held() bool { return !w.Until.IsZero() }

// Due reports a wait whose time came by now.
func (w Wait) Due(now time.Time) bool { return w.Held() && !now.Before(w.Until) }

// Text is the wait as boards show it: "until 16:30 · Codex limit resets".
func (w Wait) Text(now time.Time) string {
	return "until " + Clock(w.Until, now) + " · " + w.Reason
}

// Phrase is the wait as notes say it: "until 16:30 (Codex limit resets)".
func (w Wait) Phrase(now time.Time) string {
	return "until " + Clock(w.Until, now) + " (" + w.Reason + ")"
}

// FromOptions is the wait pane options until (unix seconds) and reason hold; the zero Wait for none or half a one.
func FromOptions(until, reason string) Wait {
	sec, err := strconv.ParseInt(until, 10, 64)
	if err != nil || sec <= 0 || reason == "" {
		return Wait{}
	}
	return Wait{Until: time.Unix(sec, 0), Reason: reason}
}

// Clock is t as a note shows it: the time, and the day unless it's now's.
func Clock(t, now time.Time) string {
	t = t.In(now.Location())
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

var clockArg = regexp.MustCompile(`^(\d{1,2}):(\d\d)$`)

// Parse reads when a wait ends: "+90m" (a Go duration) from now, or a local "16:30", the next such minute (00:30
// at 23:00 is tomorrow's). It refuses a time not ahead of now or more than Max away.
func Parse(arg string, now time.Time) (time.Time, error) {
	if d, ok := strings.CutPrefix(arg, "+"); ok {
		dur, err := time.ParseDuration(d)
		switch {
		case err != nil:
			return time.Time{}, fmt.Errorf("%q: want +<duration> (+90m, +1h30m) or HH:MM", arg)
		case dur <= 0:
			return time.Time{}, fmt.Errorf("%s: not ahead of now", arg)
		case dur > Max:
			return time.Time{}, fmt.Errorf("%s: more than %s", arg, Span(Max))
		}
		return now.Add(dur), nil
	}
	m := clockArg.FindStringSubmatch(arg)
	if m == nil {
		return time.Time{}, fmt.Errorf("%q: want HH:MM or +<duration> (+90m, +1h30m)", arg)
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	if hour > 23 || minute > 59 {
		return time.Time{}, fmt.Errorf("%q: no such time", arg)
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	passed := !t.After(now)
	if passed {
		t = t.AddDate(0, 0, 1)
	}
	if t.Sub(now) > Max {
		if passed {
			return time.Time{}, fmt.Errorf("%s has passed (tomorrow's is more than %s away)", arg, Span(Max))
		}
		return time.Time{}, fmt.Errorf("%s is more than %s away", arg, Span(Max))
	}
	return t, nil
}

// Span is d in whole minutes: "4h", "16h 58m".
func Span(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// New is a wait until until (from Parse) for reason, its whitespace collapsed to one line. It refuses an empty
// reason, one with a control character (tmux.Sep would break every pane row) and one ending in ";", tmux's command
// separator.
func New(until time.Time, reason string) (Wait, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	switch {
	case until.IsZero():
		return Wait{}, errors.New("a wait needs a time")
	case reason == "":
		return Wait{}, errors.New("a wait needs a reason")
	case strings.ContainsFunc(reason, unicode.IsControl):
		return Wait{}, fmt.Errorf("reason %q: no control characters", reason)
	case strings.HasSuffix(reason, ";"):
		return Wait{}, fmt.Errorf("reason %q: can't end in ;", reason)
	}
	return Wait{Until: until, Reason: reason}, nil
}

// Set records w (from New) on pane, with its @agent_prompts then, replacing a wait it holds.
func Set(c *tmux.Client, pane string, w Wait) error {
	_, err := c.Run("set-option", "-p", "-t", pane, OptUntil, strconv.FormatInt(w.Until.Unix(), 10), ";",
		"set-option", "-p", "-t", pane, OptReason, w.Reason, ";",
		"set-option", "-pF", "-t", pane, OptPrompts, "#{@agent_prompts}")
	return err
}

// Unset drops pane's wait.
func Unset(c *tmux.Client, pane string) error {
	_, err := c.Run("set-option", "-pu", "-t", pane, OptUntil, ";", "set-option", "-pu", "-t", pane, OptReason, ";",
		"set-option", "-pu", "-t", pane, OptPrompts)
	return err
}
