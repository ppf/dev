package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	lineUnknown   = `{"type":"user","uuid":"n1","timestamp":"2026-11-24T10:02:00.000Z","message":{"content":"[Request interrupted by user]"}}`
	lineBroken    = `{"type":"user","uuid":"b1","origin":"human","message":{"content":"Tak"}}`
	lineAsk       = `{"type":"assistant","uuid":"a2","timestamp":"2026-11-24T10:02:00.000Z","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{}}]}}`
	lineGrep      = `{"type":"assistant","uuid":"a3","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -n \"name\":\"AskUserQuestion\" x"}}]}}`
	lineNested    = `{"type":"assistant","uuid":"a4","message":{"content":[{"type":"tool_use","name":"mcp","input":{"filter":{"type":"user"}}}]}}`
	lineQuestion  = `{"type":"user","uuid":"q2","origin":{"kind":"human"},"promptSource":"typed","message":{"content":"Tak czy nie, ruszamy?"}}`
	lineSplit     = `{"type":"user","uuid":"w1","origin":{"kind":"human"},"promptSource":"typed","message":{"content":"Ta[Image #1]k"}}`
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
		{name: "a line of a kind dev doesn't know since", words: "Tak", lines: []string{lineTyped, lineUnknown}, want: "of a kind dev doesn't know"},
		{name: "an unreadable line since", words: "Tak", lines: []string{lineTyped, lineBroken}, want: "can't be read"},
		{name: "a question asked since", words: "Tak", lines: []string{lineTyped, lineAsk}, want: "a question was asked after the user's latest message"},
		{name: "a question dismissed since", words: "Tak", lines: []string{lineTyped, lineAsk, lineTool}, want: "a question was asked after"},
		{name: "a question before the go", words: "Tak", lines: []string{lineAsk, linePick, lineTyped}, uuid: "u1"},
		{name: "part of a word", words: "dzi", lines: []string{lineTyped}, want: `"dzi" is not in the user's latest message`},
		{name: "no words", words: ` „” `, lines: []string{lineTyped}, want: "no words quoted"},
		{name: "an unreadable line before the go", words: "Tak", lines: []string{lineBroken, lineTyped}, uuid: "u1"},
		{name: "a tool's text naming AskUserQuestion", words: "Tak", lines: []string{lineTyped, lineGrep}, uuid: "u1"},
		{name: "words across an image", words: "Tak", lines: []string{lineSplit}, want: `"Tak" is not in the user's latest message`},
		{name: "a tool's input holding a user type", words: "Tak", lines: []string{lineTyped, lineNested}, uuid: "u1"},
		{name: "a question", words: "Tak", lines: []string{lineQuestion}, want: "is a question, not a go"},
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
	partial := filepath.Join(t.TempDir(), "partial.jsonl")
	if err := os.WriteFile(partial, []byte(lineTyped+"\n"+`{"type":"user","origin":{"ki`), 0o600); err != nil {
		t.Fatal(err)
	}
	if g, err := LatestGo(partial, "Tak"); err != nil || g.UUID != "u1" {
		t.Errorf("❌ a last line still being written: %+v, %v", g, err)
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
	g := Go{UUID: "u1"}
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
	if err := b.ClaimGo(Go{UUID: "u2"}); err != nil {
		t.Errorf("❌ claim of a newer go: %v", err)
	}
	if err := b.ClaimGo(g); err == nil {
		t.Error("❌ an older go claimed again after a newer one")
	}
}

// cmdLine is the user's typed message with text, as Claude Code writes it.
func cmdLine(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"user","uuid":"c1","timestamp":"2026-11-24T10:03:00.000Z","origin":{"kind":"human"},"promptSource":"typed","message":{"content":` + string(b) + `}}`
}

// A plain command passes as a typed go does, and is refused also when a sentence of it ends in "?".
func TestLatestCommand(t *testing.T) {
	for _, tc := range []struct {
		name, words, want string
		lines             []string
	}{
		{name: "a command", words: "start nocworx morning routine", lines: []string{lineReply, cmdLine("Start nocworx morning routine")}},
		{name: "a command with a link's query", words: "zrób PR", lines: []string{cmdLine("zrób PR wg https://x.dev/a?b=1 teraz")}},
		{name: "a command beside a paste", words: "zrób PR", lines: []string{cmdLine("zrób PR <pasted_content id=\"a\">\nczy?\n</pasted_content id=\"a\">")}},
		{name: "an idea asked", words: "dodamy X", lines: []string{cmdLine("a może dodamy X?")}, want: "holds a question, not a plain command"},
		{name: "a question mid-message", words: "zrób PR", lines: []string{cmdLine("czy to głupi pomysł? zrób PR")}, want: "holds a question, not a plain command"},
		{name: "a question before a paste", words: "zrób PR", lines: []string{cmdLine("zrób PR, ok?<pasted_content id=\"a\">\nlog\n</pasted_content id=\"a\"> i dalej")}, want: "holds a question"},
		{name: "a question run into the next sentence", words: "zrób PR", lines: []string{cmdLine("ok?zrób PR")}, want: "holds a question"},
		{name: "a link's encoded query", words: "zrób PR", lines: []string{cmdLine("zrób PR wg https://x.dev/?%71=1")}},
		{name: "a question in brackets", words: "zrób PR", lines: []string{cmdLine("zrób PR (ok?) teraz")}, want: "holds a question"},
		{name: "a question in quotes", words: "zrób PR", lines: []string{cmdLine(`zrób PR "czy?" teraz`)}, want: "holds a question"},
		{name: "a question with ?!", words: "zrób PR", lines: []string{cmdLine("zrób PR?! teraz")}, want: "holds a question"},
		{name: "a full-width question mark", words: "zrób PR", lines: []string{cmdLine("zrób PR？")}, want: "holds a question"},
		{name: "words not in the message", words: "start the release", lines: []string{cmdLine("zrób PR")}, want: `"start the release" is not in the user's latest message`},
		{name: "an inbox batch since", words: "zrób PR", lines: []string{cmdLine("zrób PR"), linePeer}, want: "another session's"},
		{name: "a plugin since", words: "zrób PR", lines: []string{cmdLine("zrób PR"), linePlugin}, want: "a plugin's prompt"},
		{name: "an accepted suggestion", words: "Tak ruszaj", lines: []string{lineSuggested}, want: `came as "suggestion_accepted"`},
		{name: "only pasted", words: "Tak ruszaj", lines: []string{linePasted}, want: "pasted text, not typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := LatestCommand(transcript(t, tc.lines...), tc.words)
			switch {
			case tc.want == "" && (err != nil || g.UUID != "c1"):
				t.Errorf("❌ LatestCommand = %+v, %v; want c1", g, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "--command: ") || !strings.HasSuffix(err.Error(), askAgain)):
				t.Errorf("❌ LatestCommand = %+v, %v; want %q", g, err, tc.want)
			}
		})
	}
	// --go keeps its check: a question mark mid-message is no question there
	if _, err := LatestGo(transcript(t, cmdLine("czy to głupi pomysł? zrób PR")), "zrób PR"); err != nil {
		t.Errorf("❌ LatestGo on a question mid-message: %v", err)
	}
}
