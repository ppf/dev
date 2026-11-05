package desktop

import (
	"cmp"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/inbox"
)

// Worker statuses, finer than its state: the first that holds names the tile.
const (
	// StatusNeedsYou: a dialog waits on the person (a permission prompt, a question; its hook file or live session
	// says so), or did when the app quit and the worker hasn't run a turn since.
	StatusNeedsYou = "needs you"
	// StatusRateLimited: a rate limit stopped its last turn.
	StatusRateLimited = "rate-limited"
	// StatusStuck: busy with no progress for inbox.StuckWorking.
	StatusStuck = "stuck"
	// StatusWorking: busy.
	StatusWorking = "working"
	// StatusWaiting: its last turn ended on `WAITING (…)`: CI or a background job will wake it.
	StatusWaiting = "waiting"
	// StatusBlocked: the app's verdict on its last turn is blocked (it waits on the person).
	StatusBlocked = "blocked"
	// StatusReviewReady: the app's verdict on its last turn is review_ready.
	StatusReviewReady = "review ready"
	// StatusIdle: running between turns.
	StatusIdle = "idle"
	// StatusStopped: no live Claude, and its last verdict isn't blocked.
	StatusStopped = "stopped"
)

// interruptedNote is what a needs-you status says when the dialog was open as the app quit: what it asked
// is gone with it.
const interruptedNote = "a dialog was open when the app quit"

// waitingNote is what a needs-you status says when what the dialog asks is unknown.
const waitingNote = "waiting for you"

// status is w's status and what it says (the question, the tool, what it waits on); w has its state, verdict,
// last lines, tool and notes. h is its hook file (or what the live file says while a dialog waits), interrupted the app record's interruptedAwaitingPerson.
func status(w Worker, h Hook, interrupted bool, now time.Time) (string, string) {
	if w.State == StateStopped {
		if interrupted {
			return StatusStopped, interruptedNote
		}
		return StatusStopped, ""
	}
	busy := w.State == StateBusy
	switch {
	// A dismissed dialog fires no hook but ends the turn: only a busy worker still waits on it
	case busy && agent.Deciding(h.State, h.WaitingFor):
		return StatusNeedsYou, cmp.Or(h.Question, waitingNote)
	// A busy worker runs a turn: whatever the app recorded at its quit is over
	case !busy && interrupted:
		return StatusNeedsYou, interruptedNote
	case !busy && h.RateLimited:
		if h.ResetAt == 0 {
			return StatusRateLimited, "no reset time read"
		}
		return StatusRateLimited, "resumes " + inbox.Clock(time.Unix(h.ResetAt, 0), now)
	case busy:
		for _, n := range w.Notes {
			if n.Kind == inbox.Stuck {
				return StatusStuck, n.Text
			}
		}
		return StatusWorking, w.Tool
	}
	last := ""
	if len(w.LastLines) > 0 {
		last = strings.Trim(w.LastLines[len(w.LastLines)-1], "*_` ")
	}
	if what, ok := strings.CutPrefix(last, "WAITING ("); ok {
		return StatusWaiting, strings.TrimSuffix(what, ")")
	}
	switch {
	case last == "IDLE":
		return StatusIdle, ""
	case w.State == StateBlocked:
		return StatusBlocked, w.NeedsAction
	case w.Verdict == "review_ready":
		return StatusReviewReady, w.Detail
	}
	return StatusIdle, ""
}
