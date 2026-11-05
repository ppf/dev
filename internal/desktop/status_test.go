package desktop

import (
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

func TestStatus(t *testing.T) {
	now := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 11, 4, 18, 30, 0, 0, time.UTC).Unix()
	asking := Hook{State: "waiting", WaitingFor: "permission_prompt", Question: "allow Bash: rm -rf bin"}
	stuck := []Note{{Kind: "stuck", Text: "no progress for 17m"}}
	ask := func(blocked bool) *Pending {
		return &Pending{Questions: []agent.Ask{{Question: "Merge #68?"}}, Reply: true, Blocked: blocked}
	}
	tests := []struct {
		name        string
		w           Worker
		h           Hook
		interrupted bool
		want, text  string
	}{
		{"a dialog open while busy", Worker{State: StateBusy, Tool: "Bash"}, asking, false, StatusNeedsYou, "allow Bash: rm -rf bin"},
		{"a dialog whose question was not read", Worker{State: StateBusy, Tool: "Bash"}, Hook{State: "waiting", WaitingFor: "permission_prompt"}, false, StatusNeedsYou, waitingNote},
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
		{"waiting, a sentence after it", Worker{State: StateIdle, Verdict: "review_ready", Detail: "d", LastLines: []string{"WAITING (background: sleep + git log, ~2 min)", "I'll check the log then."}}, Hook{}, false, StatusWaiting, "background: sleep + git log, ~2 min"},
		{"IDLE, a sentence after it", Worker{State: StateBlocked, NeedsAction: "x", LastLines: []string{"**IDLE**", "Nothing runs."}}, Hook{}, false, StatusIdle, ""},
		{"the newest marker wins: IDLE after WAITING", Worker{State: StateIdle, LastLines: []string{"WAITING (CI)", "IDLE"}}, Hook{}, false, StatusIdle, ""},
		{"the newest marker wins: WAITING after IDLE", Worker{State: StateIdle, LastLines: []string{"IDLE", "WAITING (CI)", "Back soon."}}, Hook{}, false, StatusWaiting, "CI"},
		{"busy text mentioning WAITING is still work", Worker{State: StateBusy, Tool: "Bash", LastLines: []string{"WAITING (x)"}}, Hook{}, false, StatusWorking, "Bash"},
		{"a blocked ask beats its own IDLE", Worker{State: StateBlocked, NeedsAction: "x", LastLines: []string{"IDLE"}, Ask: ask(true)}, Hook{}, false, StatusNeedsYou, "Merge #68?"},
		{"a blocked ask beats WAITING", Worker{State: StateIdle, LastLines: []string{"WAITING (CI)"}, Ask: ask(true)}, Hook{}, false, StatusNeedsYou, "Merge #68?"},
		{"an ask not blocked leaves IDLE be", Worker{State: StateIdle, LastLines: []string{"IDLE"}, Ask: ask(false)}, Hook{}, false, StatusIdle, ""},
		{"stopped wins over a blocked ask", Worker{State: StateStopped, Ask: ask(true)}, Hook{}, false, StatusStopped, ""},
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
