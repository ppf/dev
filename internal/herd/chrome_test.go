package herd

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ppf/dev/internal/agent"
	scr "github.com/ppf/dev/internal/screen"
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
		{"Codex: a Claude box isn't its input", tile{sess: sidebar.Session{Agent: "Codex", State: agent.Idle}}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewLines(capture, tt.t); len(got) != tt.want {
				t.Errorf("previewLines = %q, want %d lines", got, tt.want)
			}
		})
	}
}

// Real Codex v0.163.0 and Grok 1.0.62 screens (tmux capture-pane -p -e, ESC written as \x1b) lose their
// input box and the status lines under it; a › menu stays whole.
func TestPreviewLinesGolden(t *testing.T) {
	// last starts the tile's last line; shown is a line the tile keeps despite the blank screen under it
	tests := []struct{ file, agent, last, shown string }{
		{"codex-idle", "Codex", "Worked for 6s • 11:15", "• Explored"},
		{"codex-draft", "Codex", "~/PhpstormProjects/ppf/dev/.claude/worktrees/try-codex", ">_ OpenAI Codex (v0.163.0)"},
		{"codex-working-draft", "Codex", "• Working (1s • esc to interrupt)", ">_ OpenAI Codex (v0.163.0)"},
		{"codex-interrupted", "Codex", "■ Conversation interrupted - use /feedback if something went wrong", "› rebase onto master"},
		{"codex-trust", "Codex", "enter continue · esc back", "› 1. Trust and continue"}, // a › menu, not the input: whole
		{"grok-idle", "Grok", "Worked for 10s", "Hi. What do you want to work on?"},
		{"grok-start", "Grok", "╰─", "Grok Build  1.0.62"},
		{"grok-draft", "Grok", "/p/t/c/-/b/scratchpad/play", "[Dashboard]"},
		{"grok-working-draft", "Grok", "⠦ Waiting for response…", "❯ rebase onto master"},
		{"grok-working", "Grok", "⠧ Find ppf-dev and tile-crop worktree…", "◆ Thought for 2.4s"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + tt.file + ".txt")
			if err != nil {
				t.Fatal(err)
			}
			capture := strings.ReplaceAll(string(raw), `\x1b`, "\x1b")
			got := tail(previewLines(capture, tile{sess: sidebar.Session{Agent: tt.agent, State: agent.Idle}}), 20)
			if len(got) == 0 {
				t.Fatal("empty tile")
			}
			plain := ansi.Strip(strings.Join(got, "\n"))
			if last := strings.TrimSpace(ansi.Strip(got[len(got)-1])); !strings.HasPrefix(last, tt.last) || !strings.Contains(plain, tt.shown) ||
				strings.Contains(plain, "Help improve Grok") {
				t.Errorf("last line %q, want %q, showing %q; tile:\n%s", last, tt.last, tt.shown, plain)
			}
			deciding := tile{sess: sidebar.Session{Agent: tt.agent, State: agent.Waiting}, deciding: true}
			if got, all := previewLines(capture, deciding), screenLines(capture); !slices.Equal(got, all) {
				t.Errorf("deciding: %d lines, want the whole %d", len(got), len(all))
			}
		})
	}
}

func TestCropCodexInput(t *testing.T) {
	status := []string{"", "  GPT-6.1-Sol high · ~/src/app · Context 3% used", "  ← for agents · ? for shortcuts"}
	conversation := []string{"› fix the test", "", "• Fixed.", ""}
	screen := func(box ...string) []string { return append(append(slices.Clone(conversation), box...), status...) }
	old := append([]string{"› quoted"}, slices.Repeat([]string{"  text"}, scr.MaxBoxLines+scr.MaxBelowBox)...)
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"empty prompt", screen("› Ask Codex to do anything"), conversation[:3]},
		{"multi-line draft", screen("› first line", "  second line"), conversation[:3]},
		{"no status lines", append(slices.Clone(conversation), "› hi"), conversation[:3]},
		{"menu", screen("› 1. Trust and continue", "  2. Back"), screen("› 1. Trust and continue", "  2. Back")},
		{"menu with its selected option unnumbered", screen("› Yes, proceed", "  2. No"), screen("› Yes, proceed", "  2. No")},
		{"an indented › isn't the prompt", []string{"• Fixed.", "  › quoted"}, []string{"• Fixed.", "  › quoted"}},
		{"a sent prompt with its reply isn't the input", conversation[:3], conversation[:3]},
		{"a sent prompt far from the bottom isn't the input", old, old},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cropCodexInput(tt.lines); !slices.Equal(got, tt.want) {
				t.Errorf("cropCodexInput =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestCropGrokInput(t *testing.T) {
	top, bottom := "  ╭"+strings.Repeat("─", 60)+"╮", "  ╰"+strings.Repeat("─", 40)+" Grok 4.7 (high) ─╯"
	row := func(s string) string { return "  │ " + s + strings.Repeat(" ", 20) + "│" }
	conversation := []string{"     ❯ hi", "", "     Hi. What do you want to work on?", ""}
	screen := func(box ...string) []string {
		lines := append(slices.Clone(conversation), top)
		for _, b := range box {
			lines = append(lines, row(b))
		}
		return append(lines, bottom, "", "  Shift+Tab:mode  │  Ctrl+x:shortcuts", "", "  [stable]")
	}
	splash := []string{top, row("Grok Build  1.0.62"), bottom}
	far := append(screen("❯ "), slices.Repeat([]string{"  text"}, scr.MaxBelowBox-3)...)
	tall := screen(slices.Repeat([]string{"❯ line"}, scr.MaxBoxLines+1)...)
	open := append(slices.Clone(conversation), row("❯ "), bottom)
	banner := []string{"  Help improve Grok        [Opt out] [Opt in]", "  Off by default.", "  Change anytime via settings.", "  Read Terms and Privacy Policy.", ""}
	withBanner := append(append(slices.Clone(conversation), banner...), screen("❯ ")[len(conversation):]...)
	wrapped := slices.Insert(slices.Clone(banner), 2, "  for training and", "  debugging purposes.")
	wrappedBanner := append(append(slices.Clone(conversation), wrapped...), screen("❯ ")[len(conversation):]...)
	farBanner := append(append(append(slices.Clone(conversation), banner[0]), slices.Repeat([]string{"  text"}, maxBanner-1)...), banner[3])
	headingOnly := append(append(slices.Clone(conversation), banner[0], "  text"), screen("❯ ")[len(conversation):]...)
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"empty prompt", screen("❯ "), conversation[:3]},
		{"multi-line draft", screen("❯ first line", "  second line"), conversation[:3]},
		{"numbered option", screen("❯ 1. Yes", "  2. No"), screen("❯ 1. Yes", "  2. No")},
		{"a box without a ❯ prompt", append(slices.Clone(conversation), splash...), append(slices.Clone(conversation), splash...)},
		{"no box", conversation[:3], conversation[:3]},
		{"a box far from the bottom", far, far},
		{"a box taller than a draft", tall, tall},
		{"rows without a top border", open, open},
		{"opt-in banner above the box", withBanner, conversation[:3]},
		{"opt-in banner wrapped in a narrow pane", wrappedBanner, conversation[:3]},
		{"a banner heading far above the box stays", append(slices.Clone(farBanner), screen("❯ ")[len(conversation):]...), farBanner},
		{"a banner heading without the banner's last line stays", headingOnly, headingOnly[:len(conversation)+2]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cropGrokInput(tt.lines); !slices.Equal(got, tt.want) {
				t.Errorf("cropGrokInput =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestSqueezeBlank(t *testing.T) {
	got := squeezeBlank([]string{"", "\x1b[2m", "a", "", " ", "\x1b[0m", "b", ""})
	if want := []string{"a", "", "b", ""}; !slices.Equal(got, want) {
		t.Errorf("squeezeBlank = %q, want %q", got, want)
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

// A tile follows its worker session's @dev_cost (global or per session), not the boss's.
func TestSnapshotCostOff(t *testing.T) {
	for _, tc := range []struct{ boss, worker, want string }{
		{"", "", "$1.50"}, {"off", "off", ""}, {"", "off", ""}, {"off", "", "$1.50"},
	} {
		rows := []row{{Session: "boss", SessionID: "$1", ID: "%1", Herd: "1", CostOpt: tc.boss},
			{Session: "w", SessionID: "$2", ID: "%2", Window: 1, Command: "claude", State: agent.Idle, StateCmd: "claude", Cost: "1.5", CostOpt: tc.worker}}
		s, err := buildSnapshot(rows, nil, "%1", &fakeLookups{})
		if err != nil {
			t.Fatal(err)
		}
		if len(s.tiles) != 1 || s.tiles[0].sess.Cost != tc.want {
			t.Errorf("@dev_cost boss %q worker %q: tiles %+v, want cost %q", tc.boss, tc.worker, s.tiles, tc.want)
		}
	}
}
