package desktop

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestNotes(t *testing.T) {
	now := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	ago := func(m int) int64 { return now.Add(-time.Duration(m) * time.Minute).Unix() }
	pct := func(n int) *int { return &n }
	reset := time.Date(2026, 11, 4, 17, 55, 0, 0, time.UTC).Unix()
	tests := []struct {
		name string
		w    Worker
		h    Hook
		seen int64
		want []Note
	}{
		{"busy with recent progress", Worker{State: StateBusy}, Hook{State: "working", Since: ago(30), Active: ago(1)}, ago(20), []Note{}},
		{"busy, no progress for 17 min", Worker{State: StateBusy}, Hook{State: "working", Since: ago(30), Active: ago(17)}, ago(20),
			[]Note{{Key: "local_w/stuck/" + itoa(ago(17)), Kind: "stuck", Text: "no progress for 17m"}}},
		{"the transcript's write is progress", Worker{State: StateBusy}, Hook{State: "working", Since: ago(30), Active: ago(17)}, ago(2), []Note{}},
		{"no hook file: the transcript's write alone", Worker{State: StateBusy}, Hook{}, ago(16),
			[]Note{{Key: "local_w/stuck/" + itoa(ago(16)), Kind: "stuck", Text: "no progress for 16m"}}},
		{"nothing known: no stuck note", Worker{State: StateBusy}, Hook{}, 0, []Note{}},
		{"idle for long: not stuck", Worker{State: StateIdle}, Hook{State: "idle", Since: ago(60), Active: ago(60)}, ago(60), []Note{}},
		{"a decision unanswered for 4 min", Worker{State: StateBusy},
			Hook{State: "waiting", Since: ago(4), Active: ago(4), WaitingFor: "permission_prompt", Question: "allow Bash: ls"}, ago(40),
			[]Note{{Key: "local_w/needs a decision/" + itoa(ago(4)), Kind: "needs a decision", Text: "waiting on a decision for 4m, unanswered: allow Bash: ls"}}},
		{"a decision of 2 min: not yet", Worker{State: StateBusy}, Hook{State: "waiting", Since: ago(2), WaitingFor: "permission_prompt"}, ago(40), []Note{}},
		{"a dismissed dialog ended the turn", Worker{State: StateIdle}, Hook{State: "waiting", Since: ago(10), WaitingFor: "permission_prompt"}, ago(40), []Note{}},
		{"Claude's idle reminder is no decision", Worker{State: StateBusy}, Hook{State: "waiting", Since: ago(10), WaitingFor: "idle_prompt"}, ago(1), []Note{}},
		{"high context", Worker{State: StateIdle, Context: pct(85)}, Hook{}, 0,
			[]Note{{Key: "local_w/high context/cli-w", Kind: "high context", Text: "85% used"}}},
		{"context below the line", Worker{State: StateBusy, Context: pct(79)}, Hook{}, ago(1), []Note{}},
		{"rate-limited", Worker{State: StateIdle}, Hook{State: "idle", Since: ago(10), RateLimited: true, ResetAt: reset + 3600}, 0,
			[]Note{{Key: "local_w/rate-limited/" + itoa(reset+3600), Kind: "rate-limited", Text: "resets 18:55"}}},
		{"rate-limited, no reset time read", Worker{State: StateBlocked}, Hook{State: "idle", Since: ago(10), RateLimited: true}, 0,
			[]Note{{Key: "local_w/rate-limited/" + itoa(ago(10)), Kind: "rate-limited", Text: "no reset time read"}}},
		{"reset, inside the margin", Worker{State: StateIdle}, Hook{State: "idle", RateLimited: true, ResetAt: now.Unix() - 30}, 0,
			[]Note{{Key: "local_w/rate-limited/" + itoa(now.Unix()-30), Kind: "rate-limited", Text: "resets 17:59"}}},
		{"resume due", Worker{State: StateIdle}, Hook{State: "idle", RateLimited: true, ResetAt: reset}, 0,
			[]Note{{Key: "local_w/resume due/" + itoa(reset), Kind: "resume due", Text: "rate limit reset 17:55: resume it"}}},
		{"resumed by someone: busy again", Worker{State: StateBusy}, Hook{State: "idle", RateLimited: true, ResetAt: reset}, ago(1), []Note{}},
		{"stopped: none", Worker{State: StateStopped, Context: pct(95)}, Hook{State: "working", Active: ago(60), RateLimited: true}, ago(60), []Note{}},
	}
	for _, tt := range tests {
		tt.w.ID = "local_w"
		if got := notes(tt.w, "cli-w", tt.h, tt.seen, now); !slices.Equal(got, tt.want) {
			t.Errorf("%s: notes = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// Keys hold while the condition does: the minutes in a stuck note's text move, its key doesn't.
func TestNoteKeysStable(t *testing.T) {
	now := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	w := Worker{ID: "local_w", State: StateBusy}
	h := Hook{State: "working", Since: now.Add(-time.Hour).Unix(), Active: now.Add(-20 * time.Minute).Unix()}
	a, b := notes(w, "c", h, 0, now), notes(w, "c", h, 0, now.Add(5*time.Minute))
	if len(a) != 1 || len(b) != 1 || a[0].Key != b[0].Key || a[0].Text == b[0].Text {
		t.Errorf("notes = %+v then %+v, want one key, newer minutes", a, b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
