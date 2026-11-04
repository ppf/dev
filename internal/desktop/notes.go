package desktop

import (
	"strconv"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/inbox"
)

// Note kinds besides inbox's stuck, needs a decision and high context.
const (
	RateLimited = "rate-limited"
	ResumeDue   = "resume due"
)

// Note is a worker's health note, as dev boss's inbox has them for tmux workers. Key is the same while the
// same condition holds, so a reader tells a new note from a repeat.
type Note struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// notes are w's health notes at now from its hook file h (the zero Hook when there is none) and seen, its
// last progress the app shows in unix seconds (its transcript's last write, its live status's change): stuck (busy with no progress for inbox.StuckWorking), a decision unanswered for
// inbox.StuckDecision, a context at inbox.ContextFull or above, a rate limit and its resume once it reset.
// A stopped worker has none: its hook file may be a crash's leftover.
func notes(w Worker, cli string, h Hook, seen int64, now time.Time) []Note {
	ns := []Note{}
	if w.State == StateStopped {
		return ns
	}
	add := func(kind, text, key string) {
		ns = append(ns, Note{Key: w.ID + "/" + kind + "/" + key, Kind: kind, Text: text})
	}
	unix := func(sec int64) string { return strconv.FormatInt(sec, 10) }
	switch {
	// A dismissed dialog fires no hook but ends the turn: only a busy worker still waits on it
	case w.State == StateBusy && agent.Deciding(h.State, h.WaitingFor):
		if held := now.Sub(time.Unix(h.Since, 0)); held >= inbox.StuckDecision {
			text := "waiting on a decision for " + inbox.Span(held) + ", unanswered"
			if h.Question != "" {
				text += ": " + h.Question
			}
			add(inbox.Decision, text, unix(h.Since))
		}
	case w.State == StateBusy:
		progress := time.Unix(max(h.Active, h.Since, seen), 0)
		if quiet := now.Sub(progress); progress.Unix() > 0 && quiet >= inbox.StuckWorking {
			add(inbox.Stuck, "no progress for "+inbox.Span(quiet), unix(progress.Unix()))
		}
	case h.RateLimited:
		reset := time.Unix(h.ResetAt, 0)
		switch {
		case h.ResetAt == 0:
			add(RateLimited, "no reset time read", unix(h.Since))
		case now.Before(reset.Add(inbox.ResumeMargin)):
			add(RateLimited, "resets "+inbox.Clock(reset, now), unix(h.ResetAt))
		default:
			add(ResumeDue, "rate limit reset "+inbox.Clock(reset, now)+": resume it", unix(h.ResetAt))
		}
	}
	if w.Context != nil && *w.Context >= inbox.ContextFull && cli != "" {
		// One per conversation: a /clear starts another
		add(inbox.Context, strconv.Itoa(*w.Context)+"% used", cli)
	}
	return ns
}
