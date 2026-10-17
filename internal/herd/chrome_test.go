package herd

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
)

func TestCropInput(t *testing.T) {
	rule := strings.Repeat("─", 60)
	status := []string{"  ~/src/app main Opus 5.5 (low) 12%", "  ⏵⏵ auto mode on (shift+tab to cycle)"}
	conversation := []string{"⏺ Done: the tests pass.", "", "✻ Worked for 6s · done 18:53", ""}
	screen := func(box ...string) []string {
		lines := slices.Clone(conversation)
		lines = append(lines, rule)
		lines = append(lines, box...)
		lines = append(lines, rule)
		return append(lines, status...)
	}
	cropped := conversation[:3] // the blank line above the box goes too
	permission := []string{"Bash command", "", "  rm -rf build", "", "Do you want to proceed?", "❯ 1. Yes", "  2. Yes, and don't ask again", "  3. No (esc)"}
	menu := []string{"❯ 1. Postgres", "     The usual", "  2. SQLite", "  3. Type something."}
	spinner := "✻ Thinking… (esc to interrupt)"
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"typed draft", screen("❯ rebase onto master"), cropped},
		{"empty prompt", screen("❯ "), cropped},
		{"multi-line draft", screen("❯ first line", "  second line"), cropped},
		{"coloured box", append(append(append(slices.Clone(conversation), "\x1b[2m"+rule+"\x1b[0m"), "\x1b[1m❯\x1b[0m hi", "\x1b[2m"+rule+"\x1b[0m"), status...), cropped},
		{"permission dialog stays: it needs answering", screen("❯ 1. Yes", "  2. No, and tell Claude what to do differently"), screen("❯ 1. Yes", "  2. No, and tell Claude what to do differently")},
		{"permission dialog with its question", screen(permission...), screen(permission...)},
		{"question menu: its ❯ marks the selected option", screen(menu...), screen(menu...)},
		{"menu option starting the box", screen("❯ Option A", "  Option B", "", "Do you want to use option A?"), screen("❯ Option A", "  Option B", "", "Do you want to use option A?")},
		{"mid-turn: the spinner above the box stays", append(append(slices.Clone(conversation[:3]), spinner, ""), screen("❯ ")[len(conversation):]...), append(slices.Clone(conversation[:3]), spinner)},
		{"no box", conversation[:3], conversation[:3]},
		{"a rule in the conversation, far from the bottom, isn't the box", append([]string{rule, "❯ quoted", rule}, append(slices.Clone(conversation[:3]), "a", "b", "c", "d", "e", "f", "g", "h")...),
			append([]string{rule, "❯ quoted", rule}, append(slices.Clone(conversation[:3]), "a", "b", "c", "d", "e", "f", "g", "h")...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cropInput(tt.lines); !slices.Equal(got, tt.want) {
				t.Errorf("cropInput =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
	if got := cropInput([]string{"text", "──", "❯ x", "──", "status"}); len(got) != 5 {
		t.Errorf("short dashes taken for the box: %q", got)
	}
}

func TestPreviewLines(t *testing.T) {
	rule := strings.Repeat("─", 60)
	capture := strings.Join([]string{"⏺ Done.", rule, "❯ ", rule, "  status"}, "\n")
	tests := []struct {
		name string
		t    tile
		want int
	}{
		{"idle Claude: cropped", tile{sess: sidebar.Session{Agent: "Claude", State: agent.Idle}}, 1},
		{"idle reminder: cropped", tile{sess: sidebar.Session{Agent: "Claude", State: agent.Waiting}}, 1},
		{"deciding Claude: whole, it needs answering", tile{sess: sidebar.Session{Agent: "Claude", State: agent.Waiting}, deciding: true}, 5},
		{"Codex: whole", tile{sess: sidebar.Session{Agent: "Codex", State: agent.Idle}}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewLines(capture, tt.t); len(got) != tt.want {
				t.Errorf("previewLines = %q, want %d lines", got, tt.want)
			}
		})
	}
}

// A tile waits on a decision only for a waiting state that isn't Claude's idle reminder.
func TestSnapshotDeciding(t *testing.T) {
	rows := []row{{Session: "boss", SessionID: "$1", ID: "%1", Herd: "1"}}
	for _, w := range []struct{ name, state, waitingFor string }{
		{"asks", agent.Waiting, "permission_prompt"}, {"reminded", agent.Waiting, agent.IdlePrompt}, {"busy", agent.Working, ""},
	} {
		rows = append(rows, row{Session: w.name, SessionID: "$" + w.name, ID: "%" + w.name, Window: 1, Command: "claude",
			State: w.state, StateCmd: "claude", WaitingFor: w.waitingFor})
	}
	s, err := buildSnapshot(rows, nil, "%1", &fakeLookups{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, t := range s.tiles {
		got[t.sess.Name] = t.deciding
	}
	if want := map[string]bool{"asks": true, "reminded": false, "busy": false}; !maps.Equal(got, want) {
		t.Errorf("deciding = %v, want %v", got, want)
	}
}
