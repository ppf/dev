package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// Pending is the AskUserQuestion a worker waits on: its mod's <questions>/<app session id>.json (Answerable), else the transcript's unanswered tool use (its own dialog only);
// or the ask its last turn's wrap_up ended on (Reply).
type Pending struct {
	ToolUseID string      `json:"toolUseId"`
	Questions []agent.Ask `json:"questions"`
	// AskedAt is when the mod wrote it, in milliseconds since the epoch; 0 from the transcript.
	AskedAt    int64 `json:"askedAt"`
	Answerable bool  `json:"answerable"`
	// Reply marks a wrap_up ask: answered by a message to the worker, its options the reply buttons' labels.
	Reply bool `json:"reply"`
	// Listening marks a Reply whose worker's mod takes the reply as its next prompt: <questions>/<id>.listen written within listenFresh.
	Listening bool `json:"listening"`
}

// listenFresh is how recently a worker's mod must have written its <id>.listen heartbeat (every 2 s) to be taken as listening.
const listenFresh = 10 * time.Second

// questionGrace is how long a question file of a session not seen running is kept: one that started after the
// live files were read may have just written it.
const questionGrace = time.Minute

// writtenBefore is whether path was last written before t; false when it can't be read.
func writtenBefore(path string, t time.Time) bool {
	info, err := os.Stat(path)
	return err == nil && info.ModTime().Before(t)
}

// isListening is whether session id's mod wrote its heartbeat within listenFresh of now.
func isListening(dir, id string, now time.Time) bool {
	info, err := os.Stat(filepath.Join(dir, id+".listen"))
	return err == nil && now.Sub(info.ModTime()) < listenFresh
}

// QuestionsDir is where the mods keep the questions and answers: hooks' questions/ (HookDir).
func QuestionsDir(hooks string) string { return filepath.Join(hooks, "questions") }

// readPending reads session id's question file; ok=false when there is none.
func readPending(dir, id string) (p Pending, ok bool, err error) {
	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Pending{}, false, fmt.Errorf("%s: %w", path, err)
	}
	if p.ToolUseID == "" || len(p.Questions) == 0 {
		return Pending{}, false, fmt.Errorf("%s: no toolUseId or questions", path)
	}
	p.Answerable = true
	return p, true, nil
}

// removePending removes session id's question and answer files, half-written ones too, while toolUseID's: a question asked since is another dialog's.
func removePending(dir, id, toolUseID string) error {
	if p, ok, err := readPending(dir, id); err == nil && ok && p.ToolUseID != toolUseID {
		return nil
	}
	var errs []error
	for _, name := range []string{id + ".json", id + ".json.tmp", id + ".answer.json", id + ".answer.json.tmp"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WriteAnswer atomically writes answers (question text → label, multi-select labels comma-joined) for session id's pending toolUseID; refused once that is no longer pending.
func WriteAnswer(dir, id, toolUseID string, answers map[string]string) error {
	p, ok, err := readPending(dir, id)
	if err != nil {
		return err
	}
	if !ok || p.ToolUseID != toolUseID {
		return fmt.Errorf("%s no longer waits on question %s", id, toolUseID)
	}
	if len(answers) != len(p.Questions) {
		return fmt.Errorf("%d answers for %d questions", len(answers), len(p.Questions))
	}
	for _, q := range p.Questions {
		if answers[q.Question] == "" {
			return fmt.Errorf("no answer to %q", q.Question)
		}
	}
	data, err := json.Marshal(struct {
		ToolUseID string            `json:"toolUseId"`
		Answers   map[string]string `json:"answers"`
	}{toolUseID, answers})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".answer.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// askInput reads an AskUserQuestion tool use's questions; ok=false for another tool or input without any.
func askInput(b block) (Pending, bool) {
	if b.Name != agent.AskUserQuestion {
		return Pending{}, false
	}
	var in struct {
		Questions []agent.Ask `json:"questions"`
	}
	if json.Unmarshal(b.Input, &in) != nil || len(in.Questions) == 0 {
		return Pending{}, false
	}
	return Pending{ToolUseID: b.ID, Questions: in.Questions}, true
}

// wrapUpAsk reads a wrap_up call's ask, its reply buttons the options (app actions, e.g. commit, are no reply); ok=false for another tool or a wrap_up without one.
func wrapUpAsk(b block) (Pending, bool) {
	if b.Name != wrapUpTool {
		return Pending{}, false
	}
	var in struct {
		Ask     string `json:"ask"`
		Actions []struct {
			Label  string `json:"label"`
			Action string `json:"action"`
		} `json:"actions"`
	}
	if json.Unmarshal(b.Input, &in) != nil || in.Ask == "" {
		return Pending{}, false
	}
	q := agent.Ask{Question: in.Ask, Options: []agent.Choice{}}
	for _, a := range in.Actions {
		if a.Label != "" && a.Action == "" {
			q.Options = append(q.Options, agent.Choice{Label: a.Label})
		}
	}
	return Pending{ToolUseID: b.ID, Questions: []agent.Ask{q}, Reply: true}, true
}
