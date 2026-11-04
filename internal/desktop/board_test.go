package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const boss = "local_boss"

// deadPID is no running process: kill(2) answers ESRCH.
const deadPID = 1 << 30

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(data))
}

// lines joins transcript rows, one JSON object per line.
func lines(t *testing.T, rows ...any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func assistant(model string, blocks ...map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "isSidechain": false, "message": map[string]any{
		"model": model, "content": blocks,
		"usage": map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 1000, "cache_read_input_tokens": 248998},
	}}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func toolUse(id, name string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}
}

func toolResult(id string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"},
	}}}
}

// fixture lays out a home with a boss and its workers, returning its paths.
func fixture(t *testing.T) Paths {
	t.Helper()
	p := DefaultPaths(t.TempDir())
	org := filepath.Join(p.Records, "acct", "org")
	now := time.Now().UnixMilli()
	rec := func(id string, fields map[string]any) {
		r := map[string]any{"sessionId": id, "isArchived": false, "createdAt": now, "someNewField": []int{1}}
		for k, v := range fields {
			r[k] = v
		}
		writeJSON(t, filepath.Join(org, id+".json"), r)
	}
	from := map[string]any{"sessionId": boss, "name": "w", "sideSession": true}
	rec(boss, map[string]any{"createdAt": now - 60_000, "title": "boss"})
	rec("local_busy", map[string]any{
		"cliSessionId": "cli-busy", "title": "Busy one", "branch": "feature/busy", "worktreePath": "/wt/busy",
		"createdAt": now - 3000, "spawnedFrom": map[string]any{"sessionId": boss, "name": "busy"},
		"prs": []any{map[string]any{"prNumber": 78, "url": "https://github.com/o/r/pull/78", "state": "OPEN", "own": true}},
		// A busy worker's summary is its previous turn's
		"postTurnSummary": map[string]any{"status_category": "blocked", "status_detail": "old", "needs_action": "old"},
	})
	rec("local_blocked", map[string]any{
		"cliSessionId": "cli-blocked", "title": "Blocked one", "createdAt": now - 2000, "spawnedFrom": from, "prs": nil,
		"postTurnSummary": map[string]any{"status_category": "blocked", "status_detail": "asks", "needs_action": "pick one"},
	})
	rec("local_idle", map[string]any{
		"cliSessionId": "cli-old", "title": "Idle one", "createdAt": now - 1000, "spawnedFrom": from,
		"postTurnSummary": map[string]any{"status_category": "completed", "status_detail": "done", "needs_action": ""},
	})
	rec("local_archived", map[string]any{"cliSessionId": "cli-a", "isArchived": true, "spawnedFrom": from})
	rec("local_other", map[string]any{"cliSessionId": "cli-o", "spawnedFrom": map[string]any{"sessionId": "local_x"}})
	// Names the boss but does not parse: reported
	write(t, filepath.Join(org, "local_broken.json"), `{"spawnedFrom": {"sessionId": "`+boss+`"`)
	// Written before the boss started: not read at all, so its broken JSON goes unreported
	old := filepath.Join(org, "local_old.json")
	write(t, old, `{"spawnedFrom": {"sessionId": "`+boss+`"`)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	pid := os.Getpid()
	writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{"pid": pid, "sessionId": "cli-busy", "status": "busy"})
	// Matched on hostSessionId; its sessionId (after a /clear) names the transcript
	writeJSON(t, filepath.Join(p.Live, "2.json"), map[string]any{
		"pid": pid, "sessionId": "cli-idle", "hostSessionId": "local_idle", "status": "idle",
	})
	// A crashed Claude's file
	writeJSON(t, filepath.Join(p.Live, "3.json"), map[string]any{"pid": deadPID, "sessionId": "cli-blocked", "status": "busy"})
	write(t, filepath.Join(p.Live, "4.abc.key"), "not json")

	write(t, filepath.Join(p.Projects, "-wt-busy", "cli-busy.jsonl"), lines(t,
		assistant("claude-opus-5-5", text("Looking."), toolUse("t1", "Read")),
		toolResult("t1"),
		assistant("claude-opus-5-5", text("one\n\ntwo\nthree\nfour"), toolUse("t2", "Bash")),
	))
	write(t, filepath.Join(p.Projects, "-wt-idle", "cli-idle.jsonl"), lines(t,
		assistant("claude-haiku-4-5", text("Done."), toolUse("t1", "Bash")),
	))
	writeJSON(t, filepath.Join(p.Models, "old.json"), map[string]any{"catalog": []any{
		map[string]any{"id": "claude-opus-5-5", "runtime": map[string]any{"max_input_tokens": 1}},
	}})
	writeJSON(t, filepath.Join(p.Models, "new.json"), map[string]any{"catalog": map[string]any{"models": []any{
		map[string]any{"id": "claude-opus-5-5", "runtime": map[string]any{"max_input_tokens": 1_000_000}},
		map[string]any{"id": "claude-haiku-4-5", "runtime": map[string]any{"max_input_tokens": 200_000}},
	}}})
	if err := os.Chtimes(filepath.Join(p.Models, "old.json"), past, past); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBoard(t *testing.T) {
	workers, warnings, err := Board(fixture(t), boss)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "local_broken.json") {
		t.Errorf("warnings = %v, want local_broken.json's", warnings)
	}
	ids := make([]string, len(workers))
	for i, w := range workers {
		ids[i] = w.ID
	}
	if want := []string{"local_busy", "local_blocked", "local_idle"}; !slices.Equal(ids, want) {
		t.Fatalf("workers = %v, want %v (oldest first, archived and others' left out)", ids, want)
	}

	busy, blocked, idle := workers[0], workers[1], workers[2]
	if busy.State != StateBusy || busy.Name != "busy" || busy.Branch != "feature/busy" || busy.Worktree != "/wt/busy" {
		t.Errorf("busy = %+v", busy)
	}
	if want := []PR{{Number: 78, URL: "https://github.com/o/r/pull/78", State: "OPEN"}}; !slices.Equal(busy.PRs, want) {
		t.Errorf("busy PRs = %+v, want %+v", busy.PRs, want)
	}
	if want := []string{"two", "three", "four"}; !slices.Equal(busy.LastLines, want) {
		t.Errorf("busy last lines = %q, want %q", busy.LastLines, want)
	}
	if busy.Tool != "Bash" {
		t.Errorf("busy tool = %q, want Bash", busy.Tool)
	}
	if busy.Context == nil || *busy.Context != 25 {
		t.Errorf("busy context = %v, want 25", busy.Context)
	}

	if blocked.State != StateBlocked || blocked.NeedsAction != "pick one" || blocked.Detail != "asks" {
		t.Errorf("blocked = %+v", blocked)
	}
	if blocked.PRs == nil || blocked.LastLines == nil || blocked.Context != nil {
		t.Errorf("blocked with no PRs or transcript = %+v, want empty lists and no context", blocked)
	}

	if idle.State != StateIdle || idle.Verdict != "completed" {
		t.Errorf("idle = %+v", idle)
	}
	if !slices.Equal(idle.LastLines, []string{"Done."}) {
		t.Errorf("idle last lines = %q, want the live session's transcript", idle.LastLines)
	}
	if idle.Tool != "" {
		t.Errorf("idle tool = %q, want none: a tool use left unanswered is shown only while busy", idle.Tool)
	}
	if idle.Context == nil || *idle.Context != 100 {
		t.Errorf("idle context = %v, want capped at 100", idle.Context)
	}
}

func TestBoardStopped(t *testing.T) {
	p := fixture(t)
	if err := os.RemoveAll(p.Live); err != nil {
		t.Fatal(err)
	}
	workers, _, err := Board(p, boss)
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].State != StateBlocked || workers[0].Tool != "" {
		t.Errorf("busy with no live session = %+v, want its last verdict (blocked) and no tool", workers[0])
	}
	if workers[2].State != StateStopped {
		t.Errorf("idle with no live session = %s, want stopped", workers[2].State)
	}
	if !slices.Equal(workers[2].LastLines, []string{}) {
		t.Errorf("idle with no live session = %q, want its record's transcript (none)", workers[2].LastLines)
	}
}

func TestBoardNoWorkers(t *testing.T) {
	p := fixture(t)
	workers, warnings, err := Board(p, "local_other")
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warnings)
	}
	if workers == nil || len(workers) != 0 {
		t.Errorf("workers = %#v, want an empty list (JSON [])", workers)
	}
}

func TestBoardUnknownBoss(t *testing.T) {
	if _, _, err := Board(fixture(t), "local_nope"); err == nil || !strings.Contains(err.Error(), "local_nope") {
		t.Errorf("err = %v, want no record for local_nope", err)
	}
}

func TestParseTranscript(t *testing.T) {
	sidechain := assistant("claude-opus-5-5", text("subagent text"))
	sidechain["isSidechain"] = true
	synthetic := assistant("<synthetic>", text("API error"))
	data := lines(t,
		assistant("claude-opus-5-5", toolUse("a", "Read"), toolUse("b", "Grep")),
		toolResult("b"),
		map[string]any{"type": "user", "message": map[string]any{"content": "a typed prompt"}},
		assistant("claude-opus-5-5", text("main")),
		sidechain,
		synthetic,
	) + `{"type":"assistant","message":{"content":[{"type":"text","text":"half wr`
	got := parseTranscript([]byte(data))
	if !slices.Equal(got.lines, []string{"API error"}) {
		t.Errorf("lines = %q, want the synthetic row's text (only its usage is skipped)", got.lines)
	}
	if got.tool != "Read" {
		t.Errorf("tool = %q, want Read (Grep answered)", got.tool)
	}
	if got.model != "claude-opus-5-5" || got.tokens != 250_000 {
		t.Errorf("model, tokens = %q, %d", got.model, got.tokens)
	}
}

func TestLastLinesCut(t *testing.T) {
	got := lastLines(strings.Repeat("é", 300))
	if r := []rune(got[0]); len(r) != lineRunes || r[len(r)-1] != '…' {
		t.Errorf("cut line has %d runes, want %d ending in …", len(r), lineRunes)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, "first line\nsecond\nthird\n")
	got, err := tail(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third\n" {
		t.Errorf("tail = %q, want the whole lines in the last 10 bytes", got)
	}
	if got, _ := tail(path, 100); string(got) != "first line\nsecond\nthird\n" {
		t.Errorf("tail of a short file = %q, want all of it", got)
	}
}
