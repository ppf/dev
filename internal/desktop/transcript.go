package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// tailBytes is how much of a transcript's end is read: rows run to hundreds of KB (attachments).
const tailBytes = 1 << 20

// lastLineCount is how many of the newest reply's lines are kept; lineRunes is what cut cuts a line to.
const (
	lastLineCount = 3
	lineRunes     = 200
)

type transcript struct {
	written time.Time
	lines   []string
	tool    string
	// toolDescription is what the newest unanswered tool use says it does (a describedTools input's description), else ""
	toolDescription string
	// ask is the newest unanswered tool use's questions when it is an AskUserQuestion, else nil
	ask *Pending
	// wrapAsk is the ask the newest wrap_up ended its turn on, nil once a prompt came after it or the call failed
	wrapAsk *Pending
	// answered holds the ids of the tool uses with a result
	answered map[string]bool
	model    string
	tokens   int
	// plan is the task list its update_plan and wrap_up calls leave
	plan []Step
	// broken counts rows that failed to parse, but for a last one still being written
	broken int
}

type row struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	// A /compact summary, written as a user row
	IsCompactSummary bool `json:"isCompactSummary"`
	Message          struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int `json:"input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Input     json.RawMessage `json:"input"`
}

// readTranscript reads the end of session id's transcript, found under any project; none yet is no error.
func readTranscript(projects, id string) (transcript, error) {
	paths, err := filepath.Glob(filepath.Join(projects, "*", id+".jsonl"))
	if err != nil || len(paths) == 0 {
		return transcript{lines: []string{}}, err
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		return transcript{lines: []string{}}, err
	}
	data, start, err := tail(paths[0], tailBytes)
	if err != nil {
		return transcript{lines: []string{}}, err
	}
	t, ops := parseTranscript(data)
	t.written = info.ModTime()
	var scanErr error
	if !hasItems(ops) && start > 0 {
		// The list was set before the tail: a long turn
		before, broken, err := planOpsBefore(paths[0], start)
		ops, t.broken, scanErr = append(before, ops...), t.broken+broken, err
	}
	t.plan = replay(ops)
	if t.broken > 0 {
		scanErr = errors.Join(scanErr, fmt.Errorf("%s: %d rows did not parse", paths[0], t.broken))
	}
	return t, scanErr
}

// Tail is what a transcript's end says, for a tmux worker's card: the newest reply's last lines, the tool
// call in flight ("" for none) and what it says it does ("" for none, or a tool not in describedTools), and
// how many rows did not parse.
type Tail struct {
	Lines           []string
	Tool            string
	ToolDescription string
	Broken          int
}

// ReadTail reads the end of the transcript at path, as the board does but without its task list: no scan
// before the tail, so it is cheap enough to read every second. The error is the read's; rows that don't
// parse are counted in Broken.
func ReadTail(path string) (Tail, error) {
	data, _, err := tail(path, tailBytes)
	if err != nil {
		return Tail{}, err
	}
	t, _ := parseTranscript(data)
	return Tail{Lines: t.lines, Tool: t.tool, ToolDescription: t.toolDescription, Broken: t.broken}, nil
}

// tail reads the whole lines in the last n bytes of path and where they start; none when no whole line fits.
func tail(path string, n int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	// One byte more: a cut right after a newline keeps the line that starts there
	off := max(0, info.Size()-n-1)
	data := make([]byte, info.Size()-off)
	read, err := f.ReadAt(data, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	data = data[:read]
	if off > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, 0, nil
		}
		data, off = data[i+1:], off+int64(i)+1
	}
	return data, off, nil
}

// parseTranscript reads the main thread's rows: the newest assistant text and usage, the newest tool use
// without a result, and the task list calls. A line that does not parse is skipped, and counted unless it is
// the last, unended one (being written).
func parseTranscript(data []byte) (transcript, []planOp) {
	t := transcript{lines: []string{}, answered: map[string]bool{}}
	var pending []block
	var ops []planOp
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		// Most bytes are other rows (attachments, request shapes): only these two are parsed
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) && !bytes.Contains(line, []byte(`"type":"user"`)) {
			continue
		}
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			if i < len(lines)-1 {
				t.broken++
			}
			continue
		}
		if r.IsSidechain || r.IsMeta || (r.Type != "assistant" && r.Type != "user") {
			continue
		}
		var blocks []block
		isBlocks := json.Unmarshal(r.Message.Content, &blocks) == nil
		if isBlocks {
			ops = append(ops, planOps(blocks)...)
		}
		if r.Type == "user" && !(isBlocks && slices.ContainsFunc(blocks, isToolResult)) {
			// A prompt starts a new turn: a tool use left unanswered before it (a crash) runs no more, a wrap_up ask is answered
			pending, t.wrapAsk = nil, nil
			if isPrompt(r, blocks) {
				// The last turn's text (its IDLE or WAITING marker too) is no longer what it says
				t.lines = []string{}
			}
			continue
		}
		if !isBlocks {
			continue
		}
		// Synthetic rows (interrupts, API errors) carry zero usage
		if r.Type == "assistant" && r.Message.Usage != nil && r.Message.Model != "<synthetic>" {
			u := r.Message.Usage
			t.model, t.tokens = r.Message.Model, u.Input+u.CacheCreation+u.CacheRead
		}
		for _, b := range blocks {
			switch {
			case r.Type == "assistant" && b.Type == "text" && strings.TrimSpace(b.Text) != "":
				t.lines = lastLines(b.Text)
			case r.Type == "assistant" && b.Type == "tool_use":
				pending = append(pending, b)
				if b.Name == wrapUpTool {
					// A newer box replaces the older one, an ask or none
					t.wrapAsk = nil
					if p, ok := wrapUpAsk(b); ok {
						t.wrapAsk = &p
					}
				}
			case r.Type == "user" && b.Type == "tool_result":
				pending = without(pending, b.ToolUseID)
				t.answered[b.ToolUseID] = true
				if b.IsError && t.wrapAsk != nil && t.wrapAsk.ToolUseID == b.ToolUseID {
					// A call the app rejected (its input invalid) showed no question
					t.wrapAsk = nil
				}
			}
		}
	}
	if len(pending) > 0 {
		last := pending[len(pending)-1]
		t.tool = last.Name
		var in struct {
			Description string `json:"description"`
		}
		if describedTools[last.Name] && json.Unmarshal(last.Input, &in) == nil {
			t.toolDescription = cut(strings.Join(strings.Fields(in.Description), " "))
		}
		if p, ok := askInput(last); ok {
			t.ask = &p
		}
	}
	return t, ops
}

// describedTools are the tools whose input's description says what the call does; others' (an MCP tool's issue
// body) may be content.
var describedTools = map[string]bool{"Bash": true, "Agent": true, "Task": true}

func isToolResult(b block) bool { return b.Type == "tool_result" }

// nonPrompts start the user rows Claude Code writes that start no turn: an interrupt, a local slash command and its output.
var nonPrompts = []string{"[Request interrupted", "<command-name>", "<command-message>", "<local-command-"}

// isPrompt is whether user row r, its content blocks, is a prompt that starts a turn.
func isPrompt(r row, blocks []block) bool {
	if r.IsCompactSummary {
		return false
	}
	var text string
	if json.Unmarshal(r.Message.Content, &text) != nil {
		for _, b := range blocks {
			if b.Type == "text" {
				text = b.Text
				break
			}
		}
	}
	text = strings.TrimSpace(text)
	return !slices.ContainsFunc(nonPrompts, func(p string) bool { return strings.HasPrefix(text, p) })
}

func without(pending []block, id string) []block {
	for i, b := range pending {
		if b.ID == id {
			return append(pending[:i], pending[i+1:]...)
		}
	}
	return pending
}

// lastLines is text's last non-blank lines, whole: a long question still ends in its "?".
func lastLines(text string) []string {
	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines[max(0, len(lines)-lastLineCount):]
}

// cut cuts s to lineRunes.
func cut(s string) string {
	if r := []rune(s); len(r) > lineRunes {
		return string(r[:lineRunes-1]) + "…"
	}
	return s
}

// contextWindows maps model ids to their context windows (runtime.max_input_tokens) across the files of
// Claude Code's model catalog (one per account and org, most listing none), a newer file's window winning;
// nil when there is no catalog.
func contextWindows(dir string) (map[string]int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	modTimes := map[string]time.Time{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		modTimes[p] = info.ModTime()
	}
	slices.SortFunc(paths, func(a, b string) int { return modTimes[a].Compare(modTimes[b]) })
	windows := map[string]int{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return windows, err
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return windows, fmt.Errorf("%s: %w", p, err)
		}
		collectWindows(doc, windows)
	}
	return windows, nil
}

// collectWindows walks a catalog document for objects holding an id and runtime.max_input_tokens.
func collectWindows(v any, windows map[string]int) {
	switch v := v.(type) {
	case map[string]any:
		id, _ := v["id"].(string)
		if rt, ok := v["runtime"].(map[string]any); ok && id != "" {
			if n, ok := rt["max_input_tokens"].(float64); ok && n > 0 {
				windows[id] = int(n)
			}
		}
		for _, x := range v {
			collectWindows(x, windows)
		}
	case []any:
		for _, x := range v {
			collectWindows(x, windows)
		}
	}
}
