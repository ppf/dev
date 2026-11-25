package sidebar

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ppf/dev/internal/wait"
)

// Check is why an open to do item may be out of date, as dev todo --json carries it: its linked PR (PR set) merged,
// closed or moved since the item was written, or can't be looked up; it went unchanged for long; or its boss session
// was closed. Since is when, unix seconds: for a PR's move, when a lookup first saw it so; for a boss closed, its
// list's last change.
type Check struct {
	Kind  string `json:"kind"`
	PR    int    `json:"pr,omitempty"`
	Since int64  `json:"since"`
	// Waiting marks an unchanged check of an item that waits for a person: no action unless there is news
	Waiting bool `json:"waiting,omitempty"`
}

// Check kinds: a PR's (Event), then the others.
const (
	CheckMerged        = "merged"
	CheckClosed        = "closed"
	CheckApproved      = "approved"
	CheckChanges       = "changes requested"
	CheckGreen         = "checks green"
	CheckFailing       = "checks failing"
	CheckUnchanged     = "unchanged"
	CheckBossClosed    = "boss closed"
	CheckLookupFailing = "lookup failing"
)

// Tone is how the check stands out on a row: ToneAlert for a boss closed, else ToneFlag.
func (c Check) Tone() Tone {
	if c.Kind == CheckBossClosed {
		return ToneAlert
	}
	if c.NoAction() {
		return ToneFaint
	}
	return ToneFlag
}

// Event reports a check its linked PR's move made, not its age, its boss or a failing lookup.
func (c Check) Event() bool { return c.PR != 0 && c.Kind != CheckLookupFailing }

// NoAction reports an unchanged check of an item that waits for a person: it needs no action unless there is news.
func (c Check) NoAction() bool { return c.Kind == CheckUnchanged && c.Waiting }

// Short is the check as a row shows it: "#412 approved 2h", "unchanged 5d", "boss closed", "#412 lookup failing".
func (c Check) Short(now time.Time) string {
	switch {
	case c.Kind == CheckLookupFailing:
		return fmt.Sprintf("#%d %s", c.PR, c.Kind)
	case c.Event():
		return fmt.Sprintf("#%d %s %s", c.PR, c.Kind, c.age(now))
	case c.Kind == CheckUnchanged:
		return c.Kind + " " + c.age(now)
	}
	return c.Kind
}

// Bare is Short without its age: "#412 approved", "unchanged".
func (c Check) Bare() string {
	if c.Event() && c.Kind != CheckLookupFailing {
		return fmt.Sprintf("#%d %s", c.PR, c.Kind)
	}
	return c.Kind
}

// Long is the check as dev todo and a boss's reminder say it: "PR #412 approved 2h ago", "unchanged 5d: check it
// at its source", "boss closed", "PR #412 lookup failing for 20m: dev todo check shows why".
func (c Check) Long(now time.Time) string {
	switch {
	case c.Kind == CheckLookupFailing:
		return fmt.Sprintf("PR #%d %s for %s: dev todo check shows why", c.PR, c.Kind, c.age(now))
	case c.Event():
		return fmt.Sprintf("PR #%d %s %s ago", c.PR, c.Kind, c.age(now))
	case c.Kind == CheckUnchanged && c.Waiting:
		return c.Kind + " " + c.age(now) + ": no action unless there is news"
	case c.Kind == CheckUnchanged:
		return c.Kind + " " + c.age(now) + ": check it at its source"
	}
	return c.Kind
}

// age is how long ago, at now, the check's Since was (AgeSpan).
func (c Check) age(now time.Time) string { return AgeSpan(now.Sub(time.Unix(c.Since, 0))) }

// AgeSpan is d as an item's age: wait.Span under two days ("18h 19m"), whole days from there ("5d").
func AgeSpan(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
	return wait.Span(max(d, 0))
}

// CI states a PR's checks roll up to.
const (
	CIPassed  = "passed"
	CIFailed  = "failed"
	CIRunning = "running"
)

// RollupCheck is one of gh's statusCheckRollup entries: a check run (name, status, conclusion) or a commit
// status (context, state).
type RollupCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// failedChecks and runningChecks are the conclusions and states that make a check failed or running; any
// other one passed (success, skipped, neutral, and whatever gh adds).
var (
	failedChecks = []string{"FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "ERROR"}
	// A stale check's result is for an older commit: not passed yet
	runningChecks = []string{"QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED", "EXPECTED", "STALE"}
)

// CIState is checks' state: CIFailed when any failed or was cancelled, else CIRunning while any runs, else CIPassed,
// "" for none; names are the checks that make it (the failed ones, the running ones, else all).
func CIState(checks []RollupCheck) (state string, names []string) {
	var failed, running, all []string
	// A check run on push and pull_request, or re-run, is named once
	add := func(names *[]string, name string) {
		if !slices.Contains(*names, name) {
			*names = append(*names, name)
		}
	}
	for _, c := range checks {
		name := c.Name
		if name == "" {
			name = c.Context
		}
		add(&all, name)
		switch {
		case slices.Contains(failedChecks, c.Conclusion), slices.Contains(failedChecks, c.State):
			add(&failed, name)
		case slices.Contains(runningChecks, c.Status), slices.Contains(runningChecks, c.State), slices.Contains(runningChecks, c.Conclusion):
			add(&running, name)
		}
	}
	switch {
	case len(failed) > 0:
		return CIFailed, failed
	case len(running) > 0:
		return CIRunning, running
	case len(all) > 0:
		return CIPassed, all
	}
	return "", nil
}

// PRView is a PR as a to do item's check looks it up: State OPEN, MERGED or CLOSED, Review gh's reviewDecision,
// Checks its CIState, Ended when it merged or closed (zero while open).
type PRView struct {
	State  string
	Review string
	Checks string
	Ended  time.Time
}

// ViewPR looks the GitHub PR at url up with gh.
func ViewPR(url string) (PRView, error) {
	out, err := RunGH("", "pr", "view", url, "--json", "state,mergedAt,closedAt,reviewDecision,statusCheckRollup")
	if err != nil {
		return PRView{}, err
	}
	return ParsePRView(out)
}

// ParsePRView reads gh pr view --json state,mergedAt,closedAt,reviewDecision,statusCheckRollup's output.
func ParsePRView(out []byte) (PRView, error) {
	var v struct {
		State    string        `json:"state"`
		MergedAt time.Time     `json:"mergedAt"`
		ClosedAt time.Time     `json:"closedAt"`
		Review   string        `json:"reviewDecision"`
		Checks   []RollupCheck `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return PRView{}, fmt.Errorf("gh pr view: %w", err)
	}
	if !slices.Contains([]string{"OPEN", "MERGED", "CLOSED"}, v.State) {
		return PRView{}, fmt.Errorf("gh pr view: state %q", v.State)
	}
	p := PRView{State: v.State, Review: v.Review}
	p.Checks, _ = CIState(v.Checks)
	switch v.State {
	case "MERGED":
		p.Ended = v.MergedAt
	case "CLOSED":
		p.Ended = v.ClosedAt
	}
	return p, nil
}
