package herd

import (
	"slices"
	"strings"
	"testing"
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
