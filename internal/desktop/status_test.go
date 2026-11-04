package desktop

import (
	"testing"
	"time"
)

func TestStatus(t *testing.T) {
	now := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 11, 4, 18, 30, 0, 0, time.UTC).Unix()
	asking := Hook{State: "waiting", WaitingFor: "permission_prompt", Question: "allow Bash: rm -rf bin"}
	stuck := []Note{{Kind: "stuck", Text: "no progress for 17m"}}
	tests := []struct {
		name        string
		w           Worker
		h           Hook
		interrupted bool
		want, text  string
	}{
		{"a dialog open while busy", Worker{State: StateBusy, Tool: "Bash"}, asking, false, StatusNeedsYou, "allow Bash: rm -rf bin"},
		{"a dismissed dialog ended the turn", Worker{State: StateIdle}, asking, false, StatusIdle, ""},
		{"Claude's idle reminder is no dialog", Worker{State: StateBusy, Tool: "Read"}, Hook{State: "waiting", WaitingFor: "idle_prompt"}, false, StatusWorking, "Read"},
		{"the app quit on a dialog", Worker{State: StateIdle}, Hook{}, true, StatusNeedsYou, interruptedNote},
		{"stopped wins over a quit dialog, saying so", Worker{State: StateStopped}, asking, true, StatusStopped, interruptedNote},
		{"a busy worker ran a turn since the quit", Worker{State: StateBusy, Tool: "Bash"}, Hook{}, true, StatusWorking, "Bash"},
		{"blocked, the app quit on its dialog", Worker{State: StateBlocked, NeedsAction: "x"}, Hook{}, true, StatusNeedsYou, interruptedNote},
		{"a busy worker's old rate limit is over", Worker{State: StateBusy, Tool: "Bash"}, Hook{RateLimited: true, ResetAt: reset}, false, StatusWorking, "Bash"},
		{"rate-limited", Worker{State: StateIdle}, Hook{RateLimited: true, ResetAt: reset}, false, StatusRateLimited, "resumes 18:30"},
		{"rate-limited, no reset read", Worker{State: StateIdle}, Hook{RateLimited: true}, false, StatusRateLimited, "no reset time read"},
		{"stuck", Worker{State: StateBusy, Tool: "Bash", Notes: stuck}, Hook{}, false, StatusStuck, "no progress for 17m"},
		{"working", Worker{State: StateBusy, Tool: "Bash"}, Hook{}, false, StatusWorking, "Bash"},
		{"waiting on Auto-fix", Worker{State: StateIdle, LastLines: []string{"PR open.", "WAITING (Auto-fix: PR #80)"}}, Hook{}, false, StatusWaiting, "Auto-fix: PR #80"},
		{"waiting, in bold", Worker{State: StateBlocked, LastLines: []string{"**WAITING (background: CI, ~5 min)**"}}, Hook{}, false, StatusWaiting, "background: CI, ~5 min"},
		{"IDLE says nothing runs", Worker{State: StateBlocked, NeedsAction: "x", LastLines: []string{"`IDLE`"}}, Hook{}, false, StatusIdle, ""},
		{"busy text mentioning WAITING is still work", Worker{State: StateBusy, Tool: "Bash", LastLines: []string{"WAITING (x)"}}, Hook{}, false, StatusWorking, "Bash"},
		{"blocked", Worker{State: StateBlocked, NeedsAction: "pick one"}, Hook{}, false, StatusBlocked, "pick one"},
		{"review ready", Worker{State: StateIdle, Verdict: "review_ready", Detail: "PR #80 green"}, Hook{}, false, StatusReviewReady, "PR #80 green"},
		{"idle", Worker{State: StateIdle, Verdict: "completed"}, Hook{}, false, StatusIdle, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, text := status(tt.w, tt.h, tt.interrupted, now)
			if got != tt.want || text != tt.text {
				t.Errorf("status = %s %q, want %s %q", got, text, tt.want, tt.text)
			}
		})
	}
}
