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
