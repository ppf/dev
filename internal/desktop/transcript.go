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

// lastLineCount and lineRunes trim the newest assistant text for a tile.
const (
	lastLineCount = 3
	lineRunes     = 200
)

type transcript struct {
	// written is when the transcript was last written
	written time.Time
	lines   []string
	tool    string
	model   string
	tokens  int
	// broken counts rows that failed to parse, but for a last one still being written
	broken int
}

type row struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
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
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
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
	data, err := tail(paths[0], tailBytes)
	if err != nil {
		return transcript{lines: []string{}}, err
	}
	t := parseTranscript(data)
	t.written = info.ModTime()
	if t.broken > 0 {
		return t, fmt.Errorf("%s: %d rows did not parse", paths[0], t.broken)
	}
	return t, nil
}

// tail reads the whole lines in the last n bytes of path; none when no whole line fits.
func tail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// One byte more: a cut right after a newline keeps the line that starts there
	off := max(0, info.Size()-n-1)
	data := make([]byte, info.Size()-off)
	read, err := f.ReadAt(data, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	data = data[:read]
	if off > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, nil
		}
		data = data[i+1:]
	}
	return data, nil
}

// parseTranscript reads the main thread's rows: the newest assistant text and usage, and the newest tool use
// without a result. A line that does not parse is skipped, and counted unless it is the last, unended one
// (being written).
func parseTranscript(data []byte) transcript {
	t := transcript{lines: []string{}}
	var pending []block
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
		if r.Type == "user" && !(isBlocks && slices.ContainsFunc(blocks, isToolResult)) {
			// A prompt starts a new turn: a tool use left unanswered before it (a crash) runs no more
			pending = nil
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
			case r.Type == "user" && b.Type == "tool_result":
				pending = without(pending, b.ToolUseID)
			}
		}
	}
	if len(pending) > 0 {
		t.tool = pending[len(pending)-1].Name
	}
	return t
}

func isToolResult(b block) bool { return b.Type == "tool_result" }

func without(pending []block, id string) []block {
	for i, b := range pending {
		if b.ID == id {
			return append(pending[:i], pending[i+1:]...)
		}
	}
	return pending
}

// lastLines is text's last non-blank lines, each cut to lineRunes.
func lastLines(text string) []string {
	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > lineRunes {
				l = string(r[:lineRunes-1]) + "…"
			}
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
