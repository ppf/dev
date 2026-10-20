package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// OptQuestion is the pane option holding the Question a Claude pane's dialog asks.
const OptQuestion = "@agent_question"

// AskUserQuestion is Claude's question tool.
const AskUserQuestion = "AskUserQuestion"

// maxDetail bounds each text kept (a command, a question, an option's description): the option is read on
// every boss refresh, and tmux caps a command's size.
const maxDetail = 300

// Question is what a Claude PermissionRequest (or PreToolUse of AskUserQuestion) hook says a dialog asks:
// AskUserQuestion's questions, or the tool a permission prompt is for with its command, file or URL.
// It says what the dialog asks, not whether it's still open: an auto-approved tool reports it too.
type Question struct {
	Tool        string `json:"tool"`
	Command     string `json:"command,omitempty"`
	File        string `json:"file,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	Questions   []Ask  `json:"questions,omitempty"`
}

// Ask is one of AskUserQuestion's questions.
type Ask struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Choice `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

// Choice is an Ask option.
type Choice struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// HookQuestion reads the Question from a PermissionRequest or PreToolUse hook's input; ok=false for other
// events, a PreToolUse of another tool, or input without a tool. Only the fields a dialog shows are kept:
// a Write's content can be huge.
func HookQuestion(input []byte) (Question, bool) {
	var e struct {
		Event string `json:"hook_event_name"`
		Tool  string `json:"tool_name"`
		Input struct {
			Command     string `json:"command"`
			File        string `json:"file_path"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Questions   []Ask  `json:"questions"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(input, &e) != nil || e.Event != "PermissionRequest" && e.Event != "PreToolUse" || e.Tool == "" {
		return Question{}, false
	}
	if e.Event == "PreToolUse" && e.Tool != AskUserQuestion {
		return Question{}, false
	}
	for i := range e.Input.Questions {
		a := &e.Input.Questions[i]
		a.Question = cut(a.Question)
		for k := range a.Options {
			a.Options[k].Label, a.Options[k].Description = cut(a.Options[k].Label), cut(a.Options[k].Description)
		}
	}
	return Question{Tool: e.Tool, Command: cut(e.Input.Command), File: cut(e.Input.File), URL: cut(e.Input.URL),
		Description: cut(e.Input.Description), Questions: e.Input.Questions}, true
}

// Ask is what q asks in one line: AskUserQuestion's first question with its options' labels and how many
// follow it, or the tool a permission is for with its command, file or URL.
func (q Question) Ask() string {
	if q.Tool == AskUserQuestion && len(q.Questions) > 0 {
		a := q.Questions[0]
		s := a.Question
		if len(a.Options) > 0 {
			labels := make([]string, len(a.Options))
			for i, c := range a.Options {
				labels[i] = c.Label
			}
			s += " (" + strings.Join(labels, " / ") + ")"
		}
		if more := len(q.Questions) - 1; more > 0 {
			s += fmt.Sprintf(" +%d more", more)
		}
		return s
	}
	if d := cmp.Or(q.Command, q.File, q.URL); d != "" {
		return "allow " + q.Tool + ": " + d
	}
	return "allow " + q.Tool
}

// Shows reports whether screen shows q's dialog: its first question, else a permission's "Do you want" with
// its file name, command or URL, else its tool's name (up to maxNeedle), ignoring how the screen wraps it.
func (q Question) Shows(screen string) bool {
	var s string
	switch {
	case q.Tool == AskUserQuestion && len(q.Questions) > 0:
		s = q.Questions[0].Question
	case q.File != "":
		s = filepath.Base(q.File)
	default:
		s = cmp.Or(q.Command, q.URL, q.Tool)
	}
	screen = squeeze(screen)
	if q.Tool != AskUserQuestion && !strings.Contains(screen, "Doyouwant") {
		return false
	}
	r := []rune(squeeze(strings.TrimSuffix(s, "…")))
	return len(r) > 0 && strings.Contains(screen, string(r[:min(len(r), maxNeedle)]))
}

// maxNeedle bounds how much of a question or command must show: a long one may be cut on screen.
const maxNeedle = 200

// squeeze drops s's whitespace.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func cut(s string) string {
	if r := []rune(s); len(r) > maxDetail {
		return string(r[:maxDetail-1]) + "…"
	}
	return s
}

// RecordQuestion writes q to pane's OptQuestion; Record unsets it once the pane stops waiting.
func RecordQuestion(c *tmux.Client, pane string, q Question) error {
	data, err := json.Marshal(q)
	if err != nil {
		return err
	}
	_, err = c.Run("set-option", "-p", "-t", pane, OptQuestion, string(data))
	return err
}

// ParseQuestion reads an OptQuestion value; ok=false when unset or malformed.
func ParseQuestion(v string) (Question, bool) {
	var q Question
	if v == "" || json.Unmarshal([]byte(v), &q) != nil || q.Tool == "" {
		return Question{}, false
	}
	return q, true
}
