package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

var dbQuestion = agent.Ask{Question: "Which DB?", Header: "DB", Options: []agent.Choice{
	{Label: "Postgres", Description: "relational"}, {Label: "Mongo", Description: "documents"},
}}

func TestReadPending(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := readPending(dir, "local_w"); ok || err != nil {
		t.Fatalf("no file: ok=%v err=%v", ok, err)
	}
	writeJSON(t, filepath.Join(dir, "local_w.json"), map[string]any{
		"session": "local_w", "toolUseId": "tu1", "questions": []agent.Ask{dbQuestion}, "askedAt": 5,
	})
	p, ok, err := readPending(dir, "local_w")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.ToolUseID != "tu1" || !p.Answerable || p.AskedAt != 5 || p.Questions[0].Options[1].Label != "Mongo" {
		t.Errorf("got %+v", p)
	}

	write(t, filepath.Join(dir, "local_x.json"), `{"toolUseId":"tu2","questions":[]}`)
	if _, ok, err := readPending(dir, "local_x"); ok || err == nil {
		t.Errorf("no questions: ok=%v err=%v, want an error", ok, err)
	}
	write(t, filepath.Join(dir, "local_y.json"), `{"toolUse`)
	if _, _, err := readPending(dir, "local_y"); err == nil {
		t.Error("torn file: want an error")
	}
}

func TestWriteAnswer(t *testing.T) {
	dir := t.TempDir()
	answers := map[string]string{"Which DB?": "Mongo"}
	if err := WriteAnswer(dir, "local_w", "tu1", answers); err == nil || !strings.Contains(err.Error(), "no longer waits") {
		t.Errorf("no question: err=%v", err)
	}
	writeJSON(t, filepath.Join(dir, "local_w.json"), map[string]any{"toolUseId": "tu1", "questions": []agent.Ask{dbQuestion}})
	if err := WriteAnswer(dir, "local_w", "tu0", answers); err == nil {
		t.Error("an older question's answer: want an error")
	}
	for name, bad := range map[string]map[string]string{
		"none":         nil,
		"another key":  {"Which ORM?": "Mongo"},
		"empty":        {"Which DB?": ""},
		"one too many": {"Which DB?": "Mongo", "Which ORM?": "none"},
	} {
		if err := WriteAnswer(dir, "local_w", "tu1", bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := WriteAnswer(dir, "local_w", "tu1", answers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "local_w.answer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolUseID string            `json:"toolUseId"`
		Answers   map[string]string `json:"answers"`
	}
	if err := json.Unmarshal(data, &got); err != nil || got.ToolUseID != "tu1" || got.Answers["Which DB?"] != "Mongo" {
		t.Errorf("answer file %s (err %v)", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local_w.answer.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("temp file left: %v", err)
	}
}

func TestRemovePending(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "local_w.json"), "{}")
	write(t, filepath.Join(dir, "local_w.answer.json"), "{}")
	if err := removePending(dir, "local_w", "tu1"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
	if err := removePending(dir, "local_w", "tu1"); err != nil {
		t.Errorf("none left: %v", err)
	}
	// Another question asked since is another dialog's
	writeJSON(t, filepath.Join(dir, "local_w.json"), map[string]any{"toolUseId": "tu2", "questions": []agent.Ask{dbQuestion}})
	if err := removePending(dir, "local_w", "tu1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local_w.json")); err != nil {
		t.Errorf("tu2's question removed: %v", err)
	}
}

func askUse(id string, questions ...agent.Ask) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": agent.AskUserQuestion, "input": map[string]any{"questions": questions}}
}

func TestTranscriptAsk(t *testing.T) {
	multi := agent.Ask{Question: "Which features?", Header: "Features", MultiSelect: true, Options: []agent.Choice{{Label: "A"}, {Label: "B"}}}
	cases := []struct {
		name string
		rows []any
		want string
	}{
		{"open question", []any{assistant("m", text("Asking."), askUse("tu1", dbQuestion, multi))}, "tu1"},
		{"answered", []any{assistant("m", askUse("tu1", dbQuestion)), toolResult("tu1")}, ""},
		{"another tool after it", []any{assistant("m", askUse("tu1", dbQuestion)), toolResult("tu1"), assistant("m", toolUse("tu2", "Bash"))}, ""},
		{"no questions", []any{assistant("m", askUse("tu1"))}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, _ := parseTranscript([]byte(lines(t, c.rows...)))
			got := ""
			if tr.ask != nil {
				got = tr.ask.ToolUseID
				if tr.ask.Answerable {
					t.Error("a transcript's question is not answerable from the board")
				}
				if len(tr.ask.Questions) != 2 || !tr.ask.Questions[1].MultiSelect || tr.ask.Questions[0].Options[0].Description != "relational" {
					t.Errorf("questions %+v", tr.ask.Questions)
				}
			}
			if got != c.want {
				t.Errorf("ask %q, want %q", got, c.want)
			}
		})
	}
}

func TestBoardAsk(t *testing.T) {
	p := fixture(t)
	p.Questions = filepath.Join(t.TempDir(), "questions")
	// The busy worker's mod wrote its open question; the blocked one's session is gone, its file left behind
	writeJSON(t, filepath.Join(p.Questions, "local_busy.json"), map[string]any{"toolUseId": "tu9", "questions": []agent.Ask{dbQuestion}})
	writeJSON(t, filepath.Join(p.Questions, "local_blocked.json"), map[string]any{"toolUseId": "tu1", "questions": []agent.Ask{dbQuestion}})
	write(t, filepath.Join(p.Questions, "local_blocked.answer.json"), `{}`)

	workers, _, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	busy, blocked := workers[0], workers[1]
	if busy.Ask == nil || busy.Ask.ToolUseID != "tu9" || !busy.Ask.Answerable {
		t.Errorf("busy ask = %+v, want the mod's tu9, answerable", busy.Ask)
	}
	if blocked.Ask != nil {
		t.Errorf("blocked ask = %+v, want none", blocked.Ask)
	}
	for _, name := range []string{"local_blocked.json", "local_blocked.answer.json"} {
		if _, err := os.Stat(filepath.Join(p.Questions, name)); !os.IsNotExist(err) {
			t.Errorf("%s of a gone session kept: %v", name, err)
		}
	}

	// No file: the busy worker's transcript holds the open question, for its own dialog only
	if err := os.Remove(filepath.Join(p.Questions, "local_busy.json")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p.Projects, "-wt-busy", "cli-busy.jsonl"), lines(t, assistant("claude-opus-5-5", askUse("tu8", dbQuestion))))
	workers, _, err = Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a := workers[0].Ask; a == nil || a.ToolUseID != "tu8" || a.Answerable {
		t.Errorf("busy ask from the transcript = %+v, want tu8, not answerable", a)
	}
	// An idle worker's unanswered tool use (the idle fixture's Bash) is no question
	if workers[2].Ask != nil {
		t.Errorf("idle ask = %+v", workers[2].Ask)
	}
}

func TestBoardAskStale(t *testing.T) {
	p := fixture(t)
	p.Questions = filepath.Join(t.TempDir(), "questions")
	ask := func(id, toolUseID string) {
		writeJSON(t, filepath.Join(p.Questions, id+".json"), map[string]any{"toolUseId": toolUseID, "questions": []agent.Ask{dbQuestion}})
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(p.Questions, id+".json"))
		return err == nil
	}
	idleAt := time.Now().Add(-time.Minute).UnixMilli()
	writeJSON(t, filepath.Join(p.Live, "2.json"), map[string]any{
		"pid": os.Getpid(), "sessionId": "cli-idle", "hostSessionId": "local_idle", "status": "idle", "statusUpdatedAt": idleAt,
	})
	// Asked after it began (its turn starting since the live file was read): the next poll's to judge
	writeJSON(t, filepath.Join(p.Questions, "local_idle.json"), map[string]any{"toolUseId": "tu4", "questions": []agent.Ask{dbQuestion}, "askedAt": idleAt + 1})
	if _, _, err := Board(p, boss, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists("local_idle") {
		t.Error("a question asked after the idle stretch began was removed")
	}
	// Left by a mod reload or a crash: the idle worker's turn ended since; the busy one's transcript has t1's answer
	ask("local_idle", "tu5")
	ask("local_busy", "t1")
	workers, _, err := Board(p, boss, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if workers[0].Ask != nil || workers[2].Ask != nil {
		t.Errorf("asks = %+v, %+v, want none", workers[0].Ask, workers[2].Ask)
	}
	if exists("local_idle") || exists("local_busy") {
		t.Error("stale question files kept")
	}

	// A live file that does not read may be any worker's: nothing is removed on that read
	write(t, filepath.Join(p.Live, "9.json"), `{"pid":`)
	ask("local_idle", "tu5")
	ask("local_blocked", "tu6")
	if _, _, err := Board(p, boss, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists("local_idle") || !exists("local_blocked") {
		t.Error("question files removed despite an unread live file")
	}
}

func TestBoardPrunesOldQuestions(t *testing.T) {
	p := fixture(t)
	p.Questions = filepath.Join(t.TempDir(), "questions")
	old, fresh := filepath.Join(p.Questions, "local_other.json.tmp"), filepath.Join(p.Questions, "local_mine.json")
	write(t, old, "{}")
	write(t, fresh, "{}")
	past := time.Now().Add(-staleHooks - time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Board(p, boss, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old file kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file of a session no worker of this boss: %v", err)
	}
}
