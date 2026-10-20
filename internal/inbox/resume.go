package inbox

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/tmux"
)

// Pane options of a worker a rate limit stopped: when dev resumes it (unix seconds), its @agent_prompts
// and @agent_session then, and how many times in a row it was resumed.
const (
	OptResumeAt      = "@dev_resume_at"
	OptResumePrompts = "@dev_resume_prompts"
	OptResumeSession = "@dev_resume_session"
	OptResumes       = "@dev_resumes"
)

const (
	// maxResumes stops resuming a worker that keeps hitting its limit: each resume's failure is a new note.
	maxResumes = 3
	// resumeMargin is how long after its reset a worker is resumed.
	resumeMargin = time.Minute
	// ResumePrompt is what dev sends a worker once its rate limit reset.
	ResumePrompt = "Continue where you left off: your last turn stopped on a rate limit, which has reset."
)

// resetsAt is a rate limit message's reset: "resets 7:10pm (Europe/Warsaw)", "resets Oct 22 at 8pm (Europe/Warsaw)".
var resetsAt = regexp.MustCompile(`resets (?:([A-Z][a-z]{2}) (\d{1,2}) at )?(\d{1,2})(?::(\d\d))?\s?([ap]m)(?: \(([^)]+)\))?`)

// ResetTime is when the last reset in msg falls, in its time zone (else now's): the next such time from an
// hour before now, as it is shown to the minute. ok=false without one, or with a zone or date Go can't read.
func ResetTime(msg string, now time.Time) (time.Time, bool) {
	all := resetsAt.FindAllStringSubmatch(msg, -1)
	if all == nil {
		return time.Time{}, false
	}
	m := all[len(all)-1]
	loc := now.Location()
	if m[6] != "" {
		l, err := time.LoadLocation(m[6])
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	hour, _ := strconv.Atoi(m[3])
	minute, _ := strconv.Atoi(m[4])
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if m[5] == "pm" {
		hour += 12
	}
	// The day (or year) of the earliest time taken, so a reset just past midnight resolves to the day before
	from := now.Add(-time.Hour)
	local := from.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if t.Before(from) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	month, err := time.Parse("Jan", m[1])
	day, _ := strconv.Atoi(m[2])
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}
	t := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
	if t.Before(from) {
		t = t.AddDate(1, 0, 0)
	}
	return t, true
}

// scheduleResume records when dev resumes pane after a rate limit stopped its turn (a StopFailure of
// error rate_limit): the reset in the hook's message, else in the failure's lines on its screen (failureLines),
// plus resumeMargin. note is what
// the worker's failed note adds: when it resumes, or why it doesn't; "" for another event.
func scheduleResume(c *tmux.Client, now time.Time, pane string, input []byte) (note string, err error) {
	var e hookEvent
	if json.Unmarshal(input, &e) != nil || e.Name != "StopFailure" || e.Error != "rate_limit" {
		return "", nil
	}
	reset, ok := ResetTime(e.Last+" "+e.ErrorDetails, now)
	if !ok {
		out, err := c.Run("capture-pane", "-p", "-J", "-t", pane)
		if err != nil {
			return "", err
		}
		if reset, ok = ResetTime(failureLines(out), now); !ok {
			return "no reset time read, not resuming it", nil
		}
	}
	out, err := c.Run("display-message", "-p", "-t", pane, "#{"+OptResumes+"}")
	if err != nil {
		return "", err
	}
	if n, _ := strconv.Atoi(strings.TrimSpace(out)); n >= maxResumes {
		return fmt.Sprintf("resumed %d times in a row, not resuming it again", n), unsetResume(c, pane, false)
	}
	at := reset.Add(resumeMargin)
	if _, err := c.Run("set-option", "-p", "-t", pane, OptResumeAt, strconv.FormatInt(at.Unix(), 10), ";",
		"set-option", "-pF", "-t", pane, OptResumePrompts, "#{@agent_prompts}", ";",
		"set-option", "-pF", "-t", pane, OptResumeSession, "#{@agent_session}"); err != nil {
		return "", err
	}
	return "dev resumes it at " + clock(at, now), nil
}

// failureLines is the end of the conversation on a captured screen, where Claude shows the turn's error: the
// last few lines above its input box (else the screen's), not an earlier limit's message further up.
func failureLines(capture string) string {
	plain := screen.Plain(strings.Split(strings.TrimRight(capture, "\n"), "\n"))
	end := len(plain)
	if top, _, ok := screen.ClaudeInput(plain); ok {
		end = top
	}
	return strings.Join(plain[max(0, end-failureRows):end], "\n")
}

// failureRows is how many lines above Claude's input box failureLines takes: the error, maybe wrapped, and
// a status line.
const failureRows = 4

// clock is t as a note shows it: the time, and the day unless it's now's.
func clock(t, now time.Time) string {
	t = t.In(now.Location())
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// unsetResume drops pane's scheduled resume, and its count of resumes in a row when all is set.
func unsetResume(c *tmux.Client, pane string, all bool) error {
	args := []string{"set-option", "-pu", "-t", pane, OptResumeAt, ";", "set-option", "-pu", "-t", pane, OptResumePrompts,
		";", "set-option", "-pu", "-t", pane, OptResumeSession}
	if all {
		args = append(args, ";", "set-option", "-pu", "-t", pane, OptResumes)
	}
	_, err := c.Run(args...)
	return err
}

// resume sends ResumePrompt to r's worker once its scheduled resume (OptResumeAt) is due, if nobody
// prompted it since its rate limit (its @agent_prompts unchanged), it still runs that conversation, it's idle
// and its input box is empty; ok reports a resume. A worker prompted since, in another conversation, or not
// idle (working, on a decision, exited) has its schedule dropped. One whose input box holds a draft waits for the next round.
func resume(c *tmux.Client, clk control.Clock, r paneRow) (ok bool, err error) {
	at, err := strconv.ParseInt(r.ResumeAt, 10, 64)
	if err != nil || clk.Now().Before(time.Unix(at, 0)) {
		return false, nil
	}
	st, err := control.ReadChecked(c, r.Pane)
	if err != nil {
		return false, err
	}
	if !resting(st.Trusted(), st.WaitingFor) || st.Prompts != r.ResumePrompts || r.AgentSession != r.ResumeSession {
		return false, unsetResume(c, r.Pane, false)
	}
	shot, err := c.Run("capture-pane", "-p", "-e", "-t", r.Pane)
	if err != nil || !InputEmpty(shot) {
		return false, err
	}
	// Before the prompt: a turn failing again at once must see the count and schedule its own resume
	n, _ := strconv.Atoi(r.Resumes)
	if _, err := c.Run("set-option", "-p", "-t", r.Pane, OptResumes, strconv.Itoa(n+1), ";",
		"set-option", "-pu", "-t", r.Pane, OptResumeAt, ";", "set-option", "-pu", "-t", r.Pane, OptResumePrompts,
		";", "set-option", "-pu", "-t", r.Pane, OptResumeSession); err != nil {
		return false, err
	}
	if _, err := control.Prompt(c, clk, r.Pane, ResumePrompt); err != nil {
		// Not sent: the next round tries again
		_, rerr := c.Run("set-option", "-p", "-t", r.Pane, OptResumes, cmp.Or(r.Resumes, "0"), ";",
			"set-option", "-p", "-t", r.Pane, OptResumeAt, r.ResumeAt, ";", "set-option", "-p", "-t", r.Pane, OptResumePrompts, r.ResumePrompts,
			";", "set-option", "-p", "-t", r.Pane, OptResumeSession, r.ResumeSession)
		return false, errors.Join(err, rerr)
	}
	return true, nil
}
