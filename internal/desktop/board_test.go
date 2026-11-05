package desktop

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/inbox"
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
	p := DefaultPaths(t.TempDir(), "")
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
	rec("local_fork", map[string]any{"cliSessionId": "cli-f", "spawnedFrom": map[string]any{"sessionId": boss, "userFork": true, "btw": true}})
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
	// A claude -p the idle worker runs inherits its host session id
	writeJSON(t, filepath.Join(p.Live, "5.json"), map[string]any{
		"pid": pid, "sessionId": "cli-nested", "hostSessionId": "local_idle", "status": "busy", "kind": "print",
	})

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
	// The newest file lists no windows: the newest that does is read
	writeJSON(t, filepath.Join(p.Models, "newest.json"), map[string]any{"floor": 1})
	for name, at := range map[string]time.Time{"old.json": past, "new.json": time.Now().Add(-time.Minute)} {
		if err := os.Chtimes(filepath.Join(p.Models, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestBoard(t *testing.T) {
	workers, warnings, err := Board(fixture(t), boss, time.Now())
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
		t.Fatalf("workers = %v, want %v (oldest first, archived, forks and others' left out)", ids, want)
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
	for _, w := range workers {
		t.Logf("%s: %s %q", w.ID, w.Status, w.StatusText)
	}
	if busy.Status != StatusWorking || busy.StatusText != "Bash" || blocked.Status != StatusBlocked ||
		blocked.StatusText != "pick one" || idle.Status != StatusIdle {
		t.Errorf("statuses = %s %q, %s %q, %s, want working Bash, blocked pick one, idle", busy.Status,
			busy.StatusText, blocked.Status, blocked.StatusText, idle.Status)
	}
}

// Since counts from the live status's change while running, else from the app's last activity.
func TestBoardSince(t *testing.T) {
	p := fixture(t)
	now := time.Now()
	writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{
		"pid": os.Getpid(), "sessionId": "cli-busy", "status": "busy", "statusUpdatedAt": now.Add(-12 * time.Minute).UnixMilli(),
	})
	org := filepath.Join(p.Records, "acct", "org")
	writeJSON(t, filepath.Join(org, "local_blocked.json"), map[string]any{
		// Newer than every fixture worker (theirs are the fixture's now less up to 3 s), however fast it ran
		"sessionId": "local_blocked", "cliSessionId": "cli-blocked", "createdAt": now.UnixMilli(),
		"lastActivityAt": now.Add(-3 * time.Minute).UnixMilli(), "spawnedFrom": map[string]any{"sessionId": boss},
		"interruptedAwaitingPerson": true,
	})
	workers, _, err := Board(p, boss, now)
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].Since != "12m" {
		t.Errorf("busy since = %q, want 12m", workers[0].Since)
	}
	if workers[2].ID != "local_blocked" || workers[2].Since != "3m" {
		t.Errorf("stopped since = %s %q, want local_blocked 3m", workers[2].ID, workers[2].Since)
	}
	if workers[2].State != StateStopped || workers[2].Status != StatusStopped {
		t.Errorf("a stopped worker the app quit on = %s/%s, want stopped: its dialog is gone", workers[2].State, workers[2].Status)
	}
}

func TestBoardStopped(t *testing.T) {
	p := fixture(t)
	if err := os.RemoveAll(p.Live); err != nil {
		t.Fatal(err)
	}
	workers, _, err := Board(p, boss, time.Now())
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

func TestBoardRunningBlocked(t *testing.T) {
	p := fixture(t)
	writeJSON(t, filepath.Join(p.Live, "3.json"), map[string]any{"pid": os.Getpid(), "sessionId": "cli-blocked", "status": "idle"})
	workers, _, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if workers[1].State != StateBlocked {
		t.Errorf("running worker whose last turn is blocked = %s, want blocked", workers[1].State)
	}
}

func TestBoardReusedPID(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("process start times are read on macOS only")
	}
	p := fixture(t)
	// The pid runs, but started after this Claude did: a crashed Claude's pid reused
	writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{
		"pid": os.Getpid(), "sessionId": "cli-busy", "status": "busy", "startedAt": 1000,
	})
	workers, _, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].State == StateBusy {
		t.Errorf("worker on a reused pid = busy, want not running")
	}
}

// TestBoardJSON pins the JSON the dev-board mod reads (mods/dev-board/types/index.d.ts).
func TestBoardJSON(t *testing.T) {
	p := fixture(t)
	writeJSON(t, filepath.Join(p.Records, "acct", "org", "local_new.json"), map[string]any{
		"sessionId": "local_new", "createdAt": time.Now().UnixMilli(), "spawnedFrom": map[string]any{"sessionId": boss},
	})
	workers, _, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(workers)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"ask", "branch", "context", "createdAt", "detail", "id", "lastActivityAt", "lastLines", "name",
		"needsAction", "notes", "plan", "prs", "since", "state", "status", "statusText", "title", "tool", "verdict",
		"worktree"}
	for _, w := range got {
		keys := slices.Sorted(maps.Keys(w))
		if !slices.Equal(keys, want) {
			t.Errorf("keys = %v, want %v", keys, want)
		}
		if _, ok := w["prs"].([]any); !ok {
			t.Errorf("%s: prs = %#v, want a list", w["id"], w["prs"])
		}
		if _, ok := w["lastLines"].([]any); !ok {
			t.Errorf("%s: lastLines = %#v, want a list", w["id"], w["lastLines"])
		}
		if _, ok := w["notes"].([]any); !ok {
			t.Errorf("%s: notes = %#v, want a list", w["id"], w["notes"])
		}
		if _, ok := w["plan"].([]any); !ok {
			t.Errorf("%s: plan = %#v, want a list", w["id"], w["plan"])
		}
	}
	pr := got[0]["prs"].([]any)[0].(map[string]any)
	if keys := slices.Sorted(maps.Keys(pr)); !slices.Equal(keys, []string{"number", "state", "url"}) {
		t.Errorf("PR keys = %v", keys)
	}
}

func TestBoardWarnings(t *testing.T) {
	p := fixture(t)
	writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{"pid": os.Getpid(), "sessionId": "cli-busy", "status": "thinking"})
	write(t, filepath.Join(p.Projects, "-wt-idle", "cli-idle.jsonl"), lines(t, assistant("claude-new-6", text("Hi."))))
	workers, warnings, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].State != StateBusy || workers[0].Status != StatusWorking {
		t.Errorf("worker with an unknown live status = %s/%s, want busy/working: it still runs", workers[0].State, workers[0].Status)
	}
	got := fmt.Sprint(warnings)
	for _, want := range []string{`status this board doesn't know ("thinking")`, "no context window for model claude-new-6", "local_broken.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings = %s, want one naming %s", got, want)
		}
	}
}

// Mod files changed after the boss's Claude started: it runs the copy it loaded then, and the board says so.
func TestBoardStaleMod(t *testing.T) {
	p := fixture(t)
	p.Mod = filepath.Join(t.TempDir(), "dev-board")
	// Its pid is this test's, started before now
	writeJSON(t, filepath.Join(p.Live, "9.json"), map[string]any{
		"pid": os.Getpid(), "sessionId": "cli-boss", "hostSessionId": boss, "status": "idle", "startedAt": time.Now().UnixMilli(),
	})
	write(t, filepath.Join(p.Mod, "hooks", "register.tsx"), "")
	backdate(t, filepath.Join(p.Mod, "hooks", "register.tsx"), -time.Minute)
	write(t, filepath.Join(p.Mod, "README.md"), "")
	backdate(t, filepath.Join(p.Mod, "README.md"), -time.Minute)
	stale := func() bool {
		t.Helper()
		_, warnings, err := Board(p, boss, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(fmt.Sprint(warnings), "start a new session")
	}
	if !stale() {
		t.Errorf("a hook changed after the boss started: no warning")
	}

	// Older than the boss's start; the README and the engine's types are no part of what it loads
	write(t, filepath.Join(p.Mod, ".claude-plugin", "types", "claude-code", "index.d.ts"), "")
	backdate(t, filepath.Join(p.Mod, ".claude-plugin", "types", "claude-code", "index.d.ts"), -time.Minute)
	backdate(t, filepath.Join(p.Mod, "hooks", "register.tsx"), time.Hour)
	if stale() {
		t.Errorf("mod older than the boss: warned")
	}

	write(t, filepath.Join(p.Mod, ".claude-plugin", "plugin.json"), "{}")
	backdate(t, filepath.Join(p.Mod, ".claude-plugin", "plugin.json"), -time.Minute)
	if !stale() {
		t.Errorf("the manifest changed after the boss started: no warning")
	}
	backdate(t, filepath.Join(p.Mod, ".claude-plugin", "plugin.json"), time.Hour)

	write(t, filepath.Join(p.Mod, "skills", "boss", "SKILL.md"), "")
	backdate(t, filepath.Join(p.Mod, "skills", "boss", "SKILL.md"), -time.Minute)
	if !stale() {
		t.Errorf("a skill changed after the boss started: no warning")
	}

	// Its start unknown
	writeJSON(t, filepath.Join(p.Live, "9.json"), map[string]any{"pid": os.Getpid(), "sessionId": "cli-boss", "hostSessionId": boss, "status": "idle"})
	if stale() {
		t.Errorf("boss start unknown: warned")
	}

	writeJSON(t, filepath.Join(p.Live, "9.json"), map[string]any{
		"pid": deadPID, "sessionId": "cli-boss", "hostSessionId": boss, "status": "idle", "startedAt": time.Now().UnixMilli(),
	})
	if stale() {
		t.Errorf("boss not running: warned")
	}
}

func TestModDir(t *testing.T) {
	dirs := "/a/other" + string(filepath.ListSeparator) + "/dev/mods/dev-board/"
	for _, c := range []struct {
		dirs      string
		isWatched bool
		want      string
	}{
		{"", false, ""},
		{"/a/other", false, ""},
		{dirs, false, "/dev/mods/dev-board/"},
		{dirs, true, ""},
	} {
		if got := ModDir(c.dirs, c.isWatched); got != c.want {
			t.Errorf("ModDir(%q, %v) = %q, want %q", c.dirs, c.isWatched, got, c.want)
		}
	}
}

func TestBoardNoWorkers(t *testing.T) {
	p := fixture(t)
	workers, warnings, err := Board(p, "local_other", time.Now())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warnings)
	}
	if workers == nil || len(workers) != 0 {
		t.Errorf("workers = %#v, want an empty list (JSON [])", workers)
	}
}

func TestBoardUnknownBoss(t *testing.T) {
	if _, _, err := Board(fixture(t), "local_nope", time.Now()); err == nil || !strings.Contains(err.Error(), "local_nope") {
		t.Errorf("err = %v, want no record for local_nope", err)
	}
}

func TestParseTranscript(t *testing.T) {
	sidechain := assistant("claude-haiku-4-5", text("subagent text"), toolUse("s", "WebFetch"))
	sidechain["isSidechain"] = true
	sidechain["message"].(map[string]any)["usage"] = map[string]any{"input_tokens": 9}
	synthetic := assistant("<synthetic>", text("API error"))
	prompt := map[string]any{"type": "user", "message": map[string]any{"content": "a typed prompt"}}
	meta := map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"content": "a reminder"}}
	data := lines(t,
		// Left unanswered by a crash: the next prompt ends it
		assistant("claude-opus-5-5", toolUse("x", "Edit")),
		prompt,
		assistant("claude-opus-5-5", toolUse("a", "Read"), toolUse("b", "Grep")),
		toolResult("b"),
		meta,
		assistant("claude-opus-5-5", text("main")),
		synthetic,
		sidechain,
	) + `{"type":"assistant","message":{"content":[{"type":"text","text":"half wr`
	got, _ := parseTranscript([]byte(data))
	if !slices.Equal(got.lines, []string{"API error"}) {
		t.Errorf("lines = %q, want the synthetic row's text (only its usage is skipped)", got.lines)
	}
	if got.tool != "Read" {
		t.Errorf("tool = %q, want Read (Grep answered, Edit's turn over)", got.tool)
	}
	if got.broken != 0 {
		t.Errorf("broken = %d, want 0: the last, unended row is being written", got.broken)
	}
	if got, _ := parseTranscript([]byte("{\"type\":\"assistant\",oops\n")); got.broken != 1 {
		t.Errorf("broken = %d, want 1 for an ended row that does not parse", got.broken)
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
	got, start, err := tail(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third\n" || start != 18 {
		t.Errorf("tail = %q at %d, want the whole lines in the last 10 bytes, at 18", got, start)
	}
	if got, _, _ := tail(path, 6); string(got) != "third\n" {
		t.Errorf("tail cut right after a newline = %q, want the line starting there", got)
	}
	if got, _, _ := tail(path, 3); len(got) != 0 {
		t.Errorf("tail with no whole line = %q, want none", got)
	}
	if got, start, _ := tail(path, 100); string(got) != "first line\nsecond\nthird\n" || start != 0 {
		t.Errorf("tail of a short file = %q, want all of it", got)
	}
}

// A worker's hook file is found by its current conversation (the live session's id after a /clear).
func TestBoardNotes(t *testing.T) {
	p := fixture(t)
	p.Hooks = t.TempDir()
	now := time.Now().Add(20 * time.Minute)
	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "working", Since: time.Now().Add(-time.Hour).Unix(), Active: time.Now().Unix()})
	reset := time.Now().Add(-time.Minute)
	writeJSON(t, filepath.Join(p.Hooks, "cli-idle.json"), Hook{State: "idle", RateLimited: true, ResetAt: reset.Unix()})
	// The idle worker's record id, which a /clear left behind
	writeJSON(t, filepath.Join(p.Hooks, "cli-old.json"), Hook{State: "working", Active: 1})
	workers, warnings, err := Board(p, boss, now)
	if err != nil || len(warnings) != 1 {
		t.Fatal(err, warnings)
	}
	kinds := func(w Worker) []string {
		var ks []string
		for _, n := range w.Notes {
			ks = append(ks, n.Kind)
		}
		return ks
	}
	if got := kinds(workers[0]); !slices.Equal(got, []string{"stuck"}) {
		t.Errorf("busy notes = %+v, want stuck", workers[0].Notes)
	}
	if got := kinds(workers[1]); got != nil {
		t.Errorf("blocked notes = %+v, want none", workers[1].Notes)
	}
	if got := kinds(workers[2]); !slices.Equal(got, []string{"resume due", "high context"}) {
		t.Errorf("idle notes = %+v, want resume due and high context", workers[2].Notes)
	}

	write(t, filepath.Join(p.Hooks, "cli-busy.json"), "{")
	_, warnings, _ = Board(p, boss, now)
	if len(warnings) != 2 || !strings.Contains(warnings[1].Error(), "cli-busy.json") {
		t.Errorf("warnings = %v, want the broken hook file reported", warnings)
	}
}

// A live status change is progress, and its time is in milliseconds.
func TestBoardStatusChangeIsProgress(t *testing.T) {
	p := fixture(t)
	p.Hooks = t.TempDir()
	now := time.Now().Add(20 * time.Minute)
	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "working", Since: 1, Active: 1})
	writeJSON(t, filepath.Join(p.Hooks, "cli-idle.json"), Hook{State: "idle"})
	for changed, stuck := range map[time.Duration]bool{time.Minute: false, 20 * time.Minute: true} {
		writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{"pid": os.Getpid(), "sessionId": "cli-busy", "status": "busy",
			"statusUpdatedAt": now.Add(-changed).UnixMilli()})
		workers, _, err := Board(p, boss, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(workers[0].Notes) == 1; got != stuck {
			t.Errorf("status changed %v ago: notes = %+v, want stuck %v", changed, workers[0].Notes, stuck)
		}
	}
}

// A live session waiting on a dialog needs you: its hook's question, else the live file's waitingFor, else that it
// waits; unanswered, a decision note, never a stuck one.
func TestBoardLiveWaiting(t *testing.T) {
	p := fixture(t)
	p.Hooks = t.TempDir()
	now := time.Now()
	writeJSON(t, filepath.Join(p.Hooks, "cli-idle.json"), Hook{State: "idle"})
	board := func(changed time.Duration, extra map[string]any) Worker {
		t.Helper()
		live := map[string]any{"pid": os.Getpid(), "sessionId": "cli-busy", "status": "waiting", "kind": "interactive",
			"statusUpdatedAt": now.Add(-changed).UnixMilli()}
		maps.Copy(live, extra)
		writeJSON(t, filepath.Join(p.Live, "1.json"), live)
		workers, warnings, err := Board(p, boss, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(warnings); strings.Contains(got, "live session") {
			t.Errorf("warnings = %s, want none for a waiting live session", got)
		}
		return workers[0]
	}
	asked := func(w Worker) string { return fmt.Sprintf("%s/%s %q", w.State, w.Status, w.StatusText) }

	// Its hook lags: still working, an old dialog's question left over
	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "working", Question: "old", Since: now.Unix(), Active: now.Unix()})
	w := board(time.Minute, nil)
	if got, want := asked(w), `busy/needs you "waiting for you"`; got != want {
		t.Errorf("waiting, hook working = %s, want %s", got, want)
	}
	if len(w.Notes) != 0 {
		t.Errorf("waiting 1m notes = %+v, want none", w.Notes)
	}
	if w = board(20*time.Minute, nil); len(w.Notes) != 1 || w.Notes[0].Kind != inbox.Decision {
		t.Errorf("waiting 20m notes = %+v, want an unanswered decision", w.Notes)
	}

	if got, want := asked(board(time.Minute, map[string]any{"waitingFor": "input needed"})), `busy/needs you "input needed"`; got != want {
		t.Errorf("waiting for input, hook working = %s, want %s", got, want)
	}

	// Its hook still on Claude's idle reminder, no dialog
	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "waiting", WaitingFor: "idle_prompt", Since: now.Unix()})
	if got, want := asked(board(time.Minute, nil)), `busy/needs you "waiting for you"`; got != want {
		t.Errorf("waiting, hook on the idle reminder = %s, want %s", got, want)
	}

	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "waiting", WaitingFor: "elicitation_dialog", Question: "pick one", Since: now.Unix()})
	if got, want := asked(board(time.Minute, map[string]any{"waitingFor": "input needed"})), `busy/needs you "pick one"`; got != want {
		t.Errorf("waiting, hook deciding = %s, want %s", got, want)
	}
}

// A running worker without a hook file: its hooks don't run a dev that writes one.
func TestBoardNoHookFile(t *testing.T) {
	p := fixture(t)
	p.Hooks = t.TempDir()
	writeJSON(t, filepath.Join(p.Hooks, "cli-busy.json"), Hook{State: "working"})
	_, warnings, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[1].Error(), "cli-idle.json: no hook file for Idle one") {
		t.Errorf("warnings = %v, want the idle worker's missing hook file (not the stopped one's)", warnings)
	}
}

// The board's own wiring: the transcript's list, notes before status (stuck), the record's quit flag.
func TestBoardStatusWiring(t *testing.T) {
	p := fixture(t)
	now := time.Now()
	old := now.Add(-20 * time.Minute)
	busy := filepath.Join(p.Projects, "-wt-busy", "cli-busy.jsonl")
	write(t, busy, lines(t,
		assistant("claude-opus-5-5", map[string]any{"type": "tool_use", "id": "p1", "name": updatePlanTool, "input": map[string]any{
			"items": []map[string]any{{"content": "one", "status": "completed"}, {"content": "two", "status": "in_progress"}},
		}}),
		toolResult("p1"),
		assistant("claude-opus-5-5", toolUse("t2", "Bash")),
	))
	if err := os.Chtimes(busy, old, old); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(p.Live, "1.json"), map[string]any{
		"pid": os.Getpid(), "sessionId": "cli-busy", "status": "busy", "statusUpdatedAt": old.UnixMilli(),
	})
	writeJSON(t, filepath.Join(p.Records, "acct", "org", "local_idle.json"), map[string]any{
		"sessionId": "local_idle", "cliSessionId": "cli-old", "title": "Idle one", "createdAt": now.Add(-time.Second).UnixMilli(),
		"spawnedFrom": map[string]any{"sessionId": boss}, "interruptedAwaitingPerson": true,
	})
	workers, _, err := Board(p, boss, now)
	if err != nil {
		t.Fatal(err)
	}
	b := workers[0]
	if want := []Step{{"one", StepDone}, {"two", StepCurrent}}; !slices.Equal(b.Plan, want) {
		t.Errorf("busy plan = %v, want %v", b.Plan, want)
	}
	if b.Status != StatusStuck || b.StatusText != "no progress for 20m" {
		t.Errorf("busy status = %s %q, want stuck: no progress for 20m", b.Status, b.StatusText)
	}
	idle := workers[2]
	if idle.ID != "local_idle" || idle.Status != StatusNeedsYou || idle.StatusText != interruptedNote {
		t.Errorf("running idle worker the app quit on = %s %s %q, want needs you", idle.ID, idle.Status, idle.StatusText)
	}
}
