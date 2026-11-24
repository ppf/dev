package inbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// Go is the user's latest message in the manager's transcript, typed by them, which a --kind work send quotes.
type Go struct {
	UUID string
	At   time.Time
	Text string // its typed words, pasted blocks left out
}

// entry is the part of a Claude Code transcript line the typed go reads: Origin and PromptSource mark a prompt's
// sender (human typed, peer, plugin, task-notification), ToolUseResult.answers an AskUserQuestion pick.
type entry struct {
	Type         string    `json:"type"`
	UUID         string    `json:"uuid"`
	Timestamp    time.Time `json:"timestamp"`
	IsSidechain  bool      `json:"isSidechain"`
	HookText     bool      `json:"hookText"`
	PromptSource string    `json:"promptSource"`
	Origin       *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// pick reports e an AskUserQuestion pick: a tool result holding the user's answers.
func (e entry) pick() bool {
	var r struct {
		Answers json.RawMessage `json:"answers"`
	}
	return len(e.ToolUseResult) > 0 && e.ToolUseResult[0] == '{' && json.Unmarshal(e.ToolUseResult, &r) == nil && r.Answers != nil
}

// message reports e something said to the session: a prompt from anyone, a hook's text or a pick; not a plain tool
// result or a prompt's companion row (an image, a skill's text).
func (e entry) message() bool {
	return e.Type == "user" && !e.IsSidechain && (e.Origin != nil || e.HookText || e.pick())
}

// text is e's message text, its text blocks joined.
func (e entry) text() string {
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(e.Message.Content, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var (
	pasted      = regexp.MustCompile(`(?s)<pasted_content[ >].*?</pasted_content[^>]*>`)
	imageMarker = regexp.MustCompile(`\[Image #\d+\]`)
)

// devPrompt reports text one dev types into a coordinator's pane itself: an inbox batch, a clear's prompt, a wake.
func devPrompt(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "[dev ") || t == ResumePrompt || strings.HasPrefix(t, "Your planned wait ")
}

// askAgain ends every refusal of a typed go.
const askAgain = `: ask with the hand-on question ("Hand it on" / "Drop it")`

// LatestGo is the user's latest message in the transcript at path when they typed it themselves and it holds words:
// refused when the latest message is anyone else's (an inbox batch, another session's, a plugin's, a task's end, a
// hook's text, one dev typed), a pick, an accepted suggestion, only pasted text, or doesn't hold words.
func LatestGo(path, words string) (Go, error) {
	if path == "" {
		return Go{}, errors.New("--go: the manager's transcript is unknown" + askAgain)
	}
	f, err := os.Open(path)
	if err != nil {
		return Go{}, fmt.Errorf("--go: reading the manager's transcript: %w%s", err, askAgain)
	}
	defer func() { _ = f.Close() }()
	var last entry
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"type":"user"`)) {
			var e entry
			if json.Unmarshal(line, &e) == nil && e.message() {
				last = e
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Go{}, fmt.Errorf("--go: reading the manager's transcript: %w%s", err, askAgain)
		}
	}
	text := last.text()
	typed := strings.TrimSpace(imageMarker.ReplaceAllString(pasted.ReplaceAllString(text, ""), ""))
	var why string
	switch {
	case last.UUID == "":
		why = "the user has said nothing here yet"
	case last.pick():
		why = "the user's latest words here are a pick, not a typed message"
	case last.HookText && last.Origin == nil:
		why = "the latest message here is a hook's text (an inbox batch), not the user's"
	case last.Origin.Kind == "peer":
		why = "the latest message here is another session's (an inbox batch or a relayed message), not the user's"
	case last.Origin.Kind == "plugin":
		why = "the latest message here is a plugin's prompt, not the user's"
	case last.Origin.Kind != "human":
		why = fmt.Sprintf("the latest message here is a %s, not the user's", last.Origin.Kind)
	case last.PromptSource != "typed" && last.PromptSource != "queued":
		why = fmt.Sprintf("the user's latest message came as %q, not typed", last.PromptSource)
	case devPrompt(text):
		why = "the latest message here is one dev typed, not the user's"
	case typed == "":
		why = "the user's latest message is pasted text, not typed"
	case !strings.Contains(fold(typed), fold(words)):
		why = fmt.Sprintf("%q is not in the user's latest message", words)
	default:
		return Go{UUID: last.UUID, At: last.Timestamp, Text: typed}, nil
	}
	return Go{}, errors.New("--go: " + why + askAgain)
}

// fold is s in lower case with whitespace and quotes around words collapsed, for a quote's match.
func fold(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.Trim(s, ` "'„”“`)), " "))
}
