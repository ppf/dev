package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	lines  []string
	tool   string
	model  string
	tokens int
}

type row struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
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
	data, err := tail(paths[0], tailBytes)
	if err != nil {
		return transcript{lines: []string{}}, err
	}
	return parseTranscript(data), nil
}

// tail reads the last n bytes of path, from the first whole line in them.
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
	off := max(0, info.Size()-n)
	data := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(data, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
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
// without a result. A line that does not parse is skipped: the last one may be mid-write.
func parseTranscript(data []byte) transcript {
	t := transcript{lines: []string{}}
	var pending []block
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		// Most bytes are other rows (attachments, request shapes): only these two are parsed
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) && !bytes.Contains(line, []byte(`"tool_result"`)) {
			continue
		}
		var r row
		if json.Unmarshal(line, &r) != nil || r.IsSidechain || (r.Type != "assistant" && r.Type != "user") {
			continue
		}
		var blocks []block
		if json.Unmarshal(r.Message.Content, &blocks) != nil {
			continue
		}
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

// contextWindows maps model ids to their context windows (runtime.max_input_tokens) from the newest file
// of the app's model catalog; none is an empty map, so context shows as unknown.
func contextWindows(dir string) (map[string]int, error) {
	windows := map[string]int{}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return windows, err
	}
	var newest string
	var newestAt time.Time
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newestAt) {
			newest, newestAt = p, info.ModTime()
		}
	}
	if newest == "" {
		return windows, nil
	}
	data, err := os.ReadFile(newest)
	if err != nil {
		return windows, err
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return windows, fmt.Errorf("%s: %w", newest, err)
	}
	collectWindows(doc, windows)
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
