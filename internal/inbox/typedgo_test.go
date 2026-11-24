package inbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Transcript lines as Claude Code writes them, by sender.
const (
	lineReply     = `{"type":"assistant","uuid":"a1","timestamp":"2026-11-24T10:00:00.000Z","message":{"content":[{"type":"text","text":"Start it today?"}]}}`
	lineTyped     = `{"type":"user","uuid":"u1","timestamp":"2026-11-24T10:01:00.000Z","isSidechain":false,"origin":{"kind":"human"},"promptSource":"typed","message":{"role":"user","content":"Tak, ruszaj z tym dziś"}}`
	lineQueued    = `{"type":"user","uuid":"u2","timestamp":"2026-11-24T10:01:00.000Z","origin":{"kind":"human"},"promptSource":"queued","message":{"content":[{"type":"text","text":"[Image #1]Tak ruszaj"}]}}`
	lineImage     = `{"type":"user","uuid":"i1","timestamp":"2026-11-24T10:01:01.000Z","isMeta":true,"turnCompanion":true,"message":{"content":[{"type":"text","text":"[Image: source: /tmp/x.png]"}]}}`
	lineTool      = `{"type":"user","uuid":"t1","timestamp":"2026-11-24T10:01:02.000Z","toolUseResult":{"stdout":"ok"},"message":{"content":[{"type":"tool_result","content":"ok"}]}}`
	linePeer      = `{"type":"user","uuid":"p1","timestamp":"2026-11-24T10:02:00.000Z","isMeta":true,"origin":{"kind":"peer","from":"unknown"},"promptSource":"system","message":{"content":"Another Claude session sent a message:\n[dev boss inbox] 1 worker update(s): Tak ruszaj"}}`
	linePlugin    = `{"type":"user","uuid":"g1","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"plugin","name":"assumption-check"},"promptSource":"system","message":{"content":"Tak ruszaj"}}`
	lineTask      = `{"type":"user","uuid":"k1","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"task-notification"},"promptSource":"system","message":{"content":"<task-notification>Tak ruszaj"}}`
	lineHook      = `{"type":"user","uuid":"h1","timestamp":"2026-11-24T10:02:00.000Z","isMeta":true,"hookText":true,"message":{"content":"Stop hook feedback:\n[dev boss inbox] Tak ruszaj"}}`
	linePick      = `{"type":"user","uuid":"q1","timestamp":"2026-11-24T10:02:00.000Z","toolUseResult":{"questions":[],"answers":{"Start?":"Tak ruszaj"}},"message":{"content":[{"type":"tool_result","content":"Tak ruszaj"}]}}`
	lineSuggested = `{"type":"user","uuid":"s1","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"human"},"promptSource":"suggestion_accepted","message":{"content":"Tak ruszaj"}}`
	linePasted    = `{"type":"user","uuid":"v1","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"human"},"promptSource":"typed","message":{"content":"<pasted_content id=\"ab12\">\nTak ruszaj\n</pasted_content id=\"ab12\">"}}`
	lineTypedOver = `{"type":"user","uuid":"v2","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"human"},"promptSource":"typed","message":{"content":"Tak ruszaj, see: <pasted_content id=\"cd34\">\nlog\n</pasted_content id=\"cd34\">"}}`
	lineDev       = `{"type":"user","uuid":"d1","timestamp":"2026-11-24T10:02:00.000Z","origin":{"kind":"human"},"promptSource":"typed","message":{"content":"[dev agent clear] Tak ruszaj"}}`
	lineSide      = `{"type":"user","uuid":"x1","timestamp":"2026-11-24T10:02:00.000Z","isSidechain":true,"origin":{"kind":"human"},"promptSource":"typed","message":{"content":"Tak ruszaj"}}`
)

func transcript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLatestGo(t *testing.T) {
	for _, tc := range []struct {
		name, words, want string
		lines             []string
		uuid              string
	}{
		{name: "typed", words: "tak,  RUSZAJ", lines: []string{lineReply, lineTyped, lineImage, lineTool}, uuid: "u1"},
		{name: "queued with an image", words: "„Tak ruszaj”", lines: []string{lineReply, lineQueued, lineImage}, uuid: "u2"},
		{name: "typed beside a paste", words: "Tak ruszaj", lines: []string{lineTypedOver}, uuid: "v2"},
		{name: "a sidechain's prompt doesn't count", words: "Tak", lines: []string{lineTyped, lineSide}, uuid: "u1"},
		{name: "nothing yet", words: "Tak", lines: []string{lineReply}, want: "the user has said nothing here yet"},
		{name: "words elsewhere", words: "start the release", lines: []string{lineTyped}, want: `"start the release" is not in the user's latest message`},
		{name: "inbox batch since", words: "Tak", lines: []string{lineTyped, linePeer}, want: "another session's (an inbox batch or a relayed message)"},
		{name: "plugin since", words: "Tak", lines: []string{lineTyped, linePlugin}, want: "a plugin's prompt"},
		{name: "task's end since", words: "Tak", lines: []string{lineTyped, lineTask}, want: "a task-notification, not the user's"},
		{name: "hook's batch since", words: "Tak", lines: []string{lineTyped, lineHook}, want: "a hook's text (an inbox batch)"},
		{name: "pick since", words: "Tak", lines: []string{lineTyped, linePick}, want: "a pick, not a typed message"},
		{name: "accepted suggestion", words: "Tak", lines: []string{linePick, lineSuggested}, want: `came as "suggestion_accepted", not typed`},
		{name: "pasted", words: "Tak", lines: []string{lineTyped, linePasted}, want: "pasted text, not typed"},
		{name: "dev typed", words: "Tak", lines: []string{lineTyped, lineDev}, want: "one dev typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := LatestGo(transcript(t, tc.lines...), tc.words)
			switch {
			case tc.want == "" && (err != nil || g.UUID != tc.uuid):
				t.Errorf("❌ LatestGo = %+v, %v; want %s", g, err, tc.uuid)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasSuffix(err.Error(), askAgain)):
				t.Errorf("❌ LatestGo = %+v, %v; want %q", g, err, tc.want)
			}
		})
	}
	if _, err := LatestGo("", "Tak"); err == nil || !strings.Contains(err.Error(), "transcript is unknown") {
		t.Errorf("❌ no transcript: %v", err)
	}
	if _, err := LatestGo(filepath.Join(t.TempDir(), "gone.jsonl"), "Tak"); err == nil {
		t.Error("❌ a missing transcript passed")
	}
}

func TestClaimGo(t *testing.T) {
	b := testBox(t)
	g := Go{UUID: "u1", At: time.UnixMilli(1000)}
	if err := b.ClaimGo(g); err != nil {
		t.Fatalf("❌ first claim: %v", err)
	}
	if err := b.ClaimGo(g); err == nil || !strings.Contains(err.Error(), "used for a send already") {
		t.Errorf("❌ second claim: %v", err)
	}
	if err := b.UnclaimGo(g); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimGo(g); err != nil {
		t.Errorf("❌ claim after unclaim: %v", err)
	}
	if err := b.DropPrompt(time.UnixMilli(2000)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimGo(Go{UUID: "u2", At: time.UnixMilli(1500)}); err == nil || !strings.Contains(err.Error(), "hand-on question was asked after") {
		t.Errorf("❌ claim of a go before a hand-on question: %v", err)
	}
	if err := b.ClaimGo(Go{UUID: "u3", At: time.UnixMilli(2500)}); err != nil {
		t.Errorf("❌ claim of a go after a hand-on question: %v", err)
	}
}
