package inbox

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/wait"
)

// resumeRig fakes a worker pane: its hook state, @dev_resumes and screen; it records the other commands
// and the pasted text.
type resumeRig struct {
	state, waitingFor, prompts, resumes, screen string
	cmds                                        []string
	pasted                                      string
	pasteErr                                    error
}

func (r *resumeRig) client() *tmux.Client {
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "display-message":
			if strings.Contains(args[len(args)-1], OptResumes) {
				return r.resumes + "\n", nil
			}
			return tmux.FormatRow("claude", r.state, "claude", r.prompts, r.waitingFor, "50", "", ""), nil
		case "capture-pane":
			return r.screen, nil
		case "load-buffer":
			b, err := os.ReadFile(args[3])
			r.pasted = string(b)
			return "", err
		}
		r.cmds = append(r.cmds, strings.Join(args, " "))
		if args[0] == "paste-buffer" {
			return "", r.pasteErr
		}
		return "", nil
	})
}

func TestResume(t *testing.T) {
	rule := strings.Repeat("─", 40)
	empty := rule + "\n❯ \n" + rule + "\n"
	now := time.Unix(10_000, 0)
	clk := control.Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	due, later := strconv.FormatInt(now.Add(-time.Second).Unix(), 10), strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	tests := []struct {
		name                  string
		rig                   resumeRig
		at                    string
		resumed, sent, unsets bool
		session               string
	}{
		{"due, idle", resumeRig{state: "idle", prompts: "4", screen: empty}, due, true, true, true, "s1"},
		{"due, sat idle (idle reminder)", resumeRig{state: "waiting", waitingFor: "idle_prompt", prompts: "4", screen: empty}, due, true, true, true, "s1"},
		{"not due", resumeRig{state: "idle", prompts: "4", screen: empty}, later, false, false, false, "s1"},
		{"a draft in the box waits", resumeRig{state: "idle", prompts: "4", screen: rule + "\n❯ half a thought\n" + rule + "\n"}, due, false, false, false, "s1"},
		{"prompted by hand since", resumeRig{state: "idle", prompts: "5", screen: empty}, due, false, false, true, "s1"},
		{"working", resumeRig{state: "working", prompts: "4", screen: empty}, due, false, false, true, "s1"},
		{"on a decision", resumeRig{state: "waiting", waitingFor: "permission_prompt", prompts: "4", screen: empty}, due, false, false, true, "s1"},
		{"another conversation since", resumeRig{state: "idle", prompts: "4", screen: empty}, due, false, false, true, "s2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.rig
			r.resumes = "1"
			resumed, err := resume(r.client(), clk, paneRow{Pane: "%1", ResumeAt: tt.at, ResumePrompts: "4", Resumes: "1", AgentSession: tt.session, ResumeSession: "s1"})
			if err != nil || resumed != tt.resumed {
				t.Errorf("resume = %v, %v; want %v", resumed, err, tt.resumed)
			}
			if sent := r.pasted == ResumePrompt; sent != tt.sent {
				t.Errorf("sent = %v (pasted %q), want %v", sent, r.pasted, tt.sent)
			}
			unset := slices.ContainsFunc(r.cmds, func(c string) bool { return strings.Contains(c, "set-option -pu -t %1 "+OptResumeAt) })
			if unset != tt.unsets {
				t.Errorf("schedule dropped = %v, want %v; cmds %q", unset, tt.unsets, r.cmds)
			}
			counted := slices.ContainsFunc(r.cmds, func(c string) bool { return strings.HasPrefix(c, "set-option -p -t %1 "+OptResumes+" 2 ") })
			if counted != tt.sent {
				t.Errorf("counted = %v, want %v; cmds %q", counted, tt.sent, r.cmds)
			}
		})
	}
}

// A resume prompt that fails puts the schedule back for the next round.
func TestResumePromptFails(t *testing.T) {
	rule := strings.Repeat("─", 40)
	now := time.Unix(10_000, 0)
	clk := control.Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	r := &resumeRig{state: "idle", prompts: "4", screen: rule + "\n❯ \n" + rule + "\n", pasteErr: errors.New("no pane")}
	resumed, err := resume(r.client(), clk, paneRow{Pane: "%1", ResumeAt: "9000", ResumePrompts: "4", ResumeSession: "s1", AgentSession: "s1"})
	restore := "set-option -p -t %1 " + OptResumes + " 0 ; set-option -p -t %1 " + OptResumeAt + " 9000 ; set-option -p -t %1 " + OptResumePrompts +
		" 4 ; set-option -p -t %1 " + OptResumeSession + " s1"
	if resumed || err == nil || !slices.Contains(r.cmds, restore) {
		t.Errorf("resume = %v, %v; want an error and the schedule back; cmds %q", resumed, err, r.cmds)
	}
}

func TestScheduleResume(t *testing.T) {
	now := time.Date(2026, 10, 20, 18, 30, 0, 0, mustZone(t, "Europe/Warsaw"))
	limit := `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 7:10pm (Europe/Warsaw)"}`
	bare := `{"hook_event_name":"StopFailure","error":"rate_limit"}`
	schedule := "set-option -p -t %1 " + OptResumeAt + " " + strconv.FormatInt(time.Date(2026, 10, 20, 19, 11, 0, 0, now.Location()).Unix(), 10) +
		" ; set-option -pF -t %1 " + OptResumePrompts + " #{@agent_prompts} ; set-option -pF -t %1 " + OptResumeSession + " #{@agent_session}"
	rule := strings.Repeat("─", 40)
	tests := []struct {
		name, input, screen, resumes string
		note                         string
		cmds                         []string
	}{
		{"from the hook's message", limit, "", "", "dev resumes it at 19:11", []string{schedule}},
		{"from the screen", bare, "\x1b[1m⎿  You've hit your session limit · resets 7:10pm (Europe/Warsaw)\x1b[0m\n", "", "dev resumes it at 19:11", []string{schedule}},
		{"no reset", bare, "⎿  You've hit your monthly spend limit.\n", "", "no reset time read, not resuming it", nil},
		{"resumed too often", limit, "", "3", "resumed 3 times in a row, not resuming it again",
			[]string{"set-option -pu -t %1 " + OptResumeAt + " ; set-option -pu -t %1 " + OptResumePrompts + " ; set-option -pu -t %1 " + OptResumeSession}},
		{"an earlier limit's reset further up", bare, "⎿  You've hit your session limit · resets 3pm (Europe/Warsaw)\n\n⏺ Done.\n\n❯ go on\n⏺ Working.\n⎿  You've hit your monthly spend limit.\n\n" + rule + "\n❯ \n" + rule + "\n",
			"", "no reset time read, not resuming it", nil},
		{"another error", `{"hook_event_name":"StopFailure","error":"server_error","last_assistant_message":"resets 7:10pm"}`, "", "", "", nil},
		{"a finished turn", `{"hook_event_name":"Stop"}`, "", "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &resumeRig{screen: tt.screen, resumes: tt.resumes}
			note, err := scheduleResume(r.client(), now, "%1", []byte(tt.input))
			if err != nil || note != tt.note {
				t.Errorf("scheduleResume = %q, %v; want %q", note, err, tt.note)
			}
			if !slices.Equal(r.cmds, tt.cmds) {
				t.Errorf("cmds = %q, want %q", r.cmds, tt.cmds)
			}
		})
	}
}

// A resume holds the boss's inbox only while it is still to come, resumeGrace past its time, for the conversation
// and prompts it was scheduled for, and a boss is there to resume it.
func TestResumeAhead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Unix(10_000, 0)
	for _, tt := range []struct {
		name, at, prompts, session, startedBy, off string
		want                                       bool
	}{
		{"ahead", "10060", "4", "s1", "boss-hq", "", true},
		{"in its grace", "9941", "4", "s1", "boss-hq", "", true},
		{"due", "9940", "4", "s1", "boss-hq", "", false},
		{"none", "", "4", "s1", "boss-hq", "", false},
		{"prompted since", "10060", "5", "s1", "boss-hq", "", false},
		{"another conversation", "10060", "4", "s2", "boss-hq", "", false},
		{"no boss above", "10060", "4", "s1", "", "", false},
		{"its boss's inbox off", "10060", "4", "s1", "boss-hq", "off", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := tmux.NewFake(func(args []string) (string, error) {
				switch f := args[len(args)-1]; {
				case strings.Contains(f, "@dev_resume_at"):
					return tmux.FormatRow(tt.at, tt.prompts, "4", tt.session, "s1"), nil
				case strings.Contains(f, "@dev_started_by"):
					return tmux.FormatRow(tt.startedBy, "boss-app", "", "", "", "", "1"), nil
				}
				return tmux.FormatRow("$1", "1700000000", "boss-hq", "1", tt.off, "/tmp/tmux-1/default"), nil
			})
			if got, err := resumeAhead(c, "%1", now); got != tt.want || err != nil {
				t.Errorf("resumeAhead = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestWake(t *testing.T) {
	rule := strings.Repeat("─", 40)
	empty := rule + "\n❯ \n" + rule + "\n"
	now := time.Unix(10_000, 0)
	clk := control.Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	due, later := strconv.FormatInt(now.Add(-time.Second).Unix(), 10), strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	tests := []struct {
		name                     string
		rig                      resumeRig
		until, prompts, setAt    string
		woke, held, sent, unsets bool
	}{
		{"due, idle", resumeRig{state: "idle", screen: empty}, due, "4", "4", true, false, true, true},
		{"due, sat idle (idle reminder)", resumeRig{state: "waiting", waitingFor: "idle_prompt", screen: empty}, due, "4", "4", true, false, true, true},
		{"restored: prompts not compared", resumeRig{state: "idle", screen: empty}, due, "1", "", true, false, true, true},
		{"not due", resumeRig{state: "idle", screen: empty}, later, "4", "4", false, true, false, false},
		{"prompted since it was set", resumeRig{state: "idle", screen: empty}, later, "5", "4", false, false, false, true},
		{"due, on a decision", resumeRig{state: "waiting", waitingFor: "permission_prompt", screen: empty}, due, "4", "4", false, false, false, true},
		{"due, working (its own background wait)", resumeRig{state: "working", screen: empty}, due, "4", "4", false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.rig
			r.prompts = tt.prompts
			woke, held, err := wake(r.client(), clk, paneRow{Pane: "%1", Prompts: tt.prompts, WaitUntil: tt.until, WaitReason: "Codex limit resets", WaitPrompts: tt.setAt})
			if err != nil || woke != tt.woke || held != tt.held {
				t.Errorf("wake = %v, %v, %v; want %v, %v", woke, held, err, tt.woke, tt.held)
			}
			want := "Your planned wait until " + wait.Clock(now.Add(-time.Second), now) + " (Codex limit resets) is over: continue where you left off."
			if sent := r.pasted == want; sent != tt.sent {
				t.Errorf("sent = %v (pasted %q), want %v", sent, r.pasted, tt.sent)
			}
			unset := slices.ContainsFunc(r.cmds, func(c string) bool { return strings.HasPrefix(c, "set-option -pu -t %1 "+wait.OptUntil) })
			if unset != tt.unsets {
				t.Errorf("wait dropped = %v, want %v; cmds %q", unset, tt.unsets, r.cmds)
			}
		})
	}
}

// A wake prompt that fails puts the wait back for the next round, no longer held (muting nothing).
func TestWakePromptFails(t *testing.T) {
	rule := strings.Repeat("─", 40)
	now := time.Unix(10_000, 0)
	clk := control.Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	r := &resumeRig{state: "idle", prompts: "4", screen: rule + "\n❯ \n" + rule + "\n", pasteErr: errors.New("no pane")}
	woke, held, err := wake(r.client(), clk, paneRow{Pane: "%1", Prompts: "4", WaitUntil: "9000", WaitReason: "x", WaitPrompts: "4"})
	restore := "set-option -p -t %1 " + wait.OptUntil + " 9000 ; set-option -p -t %1 " + wait.OptReason + " x ; set-option -p -t %1 " + wait.OptPrompts + " 4"
	if woke || held || err == nil || !slices.Contains(r.cmds, restore) {
		t.Errorf("wake = %v, %v, %v; want an error and the wait back; cmds %q", woke, held, err, r.cmds)
	}
}

// Due, a draft in the input box keeps the wait for the next round with errDraft, no longer held; a prompt since the
// round's list-panes (its turn over) drops it without a wake.
func TestWakeDueEdges(t *testing.T) {
	rule := strings.Repeat("─", 40)
	now := time.Unix(10_000, 0)
	clk := control.Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	draft := &resumeRig{state: "idle", prompts: "4", screen: rule + "\n❯ half a thought\n" + rule + "\n"}
	woke, held, err := wake(draft.client(), clk, paneRow{Pane: "%1", Prompts: "4", WaitUntil: "9000", WaitReason: "x", WaitPrompts: "4"})
	if woke || held || !errors.Is(err, errDraft) || draft.pasted != "" || len(draft.cmds) > 0 {
		t.Errorf("draft: wake = %v, %v, %v; cmds %q, pasted %q; want errDraft, the wait kept", woke, held, err, draft.cmds, draft.pasted)
	}
	since := &resumeRig{state: "idle", prompts: "5", screen: rule + "\n❯ \n" + rule + "\n"}
	woke, held, err = wake(since.client(), clk, paneRow{Pane: "%1", Prompts: "4", WaitUntil: "9000", WaitReason: "x", WaitPrompts: "4"})
	if woke || held || err != nil || since.pasted != "" || !slices.ContainsFunc(since.cmds, func(c string) bool { return strings.HasPrefix(c, "set-option -pu -t %1 "+wait.OptUntil) }) {
		t.Errorf("prompted since list-panes: wake = %v, %v, %v; cmds %q, pasted %q; want the wait dropped, no wake", woke, held, err, since.cmds, since.pasted)
	}
}
