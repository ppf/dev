package inbox

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/tmux"
)

// The manager's labels of what it sends a boss (dev agent prompt/message/reply --kind): only KindWork is gated.
const (
	KindWork     = "work"     // new work, sent on the user's "Hand it on" pick
	KindQuestion = "question" // a question about state or a fact: starts no work
	KindPick     = "pick"     // a follow-up to a pick relayed with dev todo answer --via manager
	KindOwn      = "own"      // the manager's own easy decision
)

// Kinds are the --kind values, ungated ones after KindWork.
var Kinds = []string{KindWork, KindQuestion, KindPick, KindOwn}

// TypedGo is a KindWork send on the user's typed go (--go) instead of a pick, recorded as its own kind.
const TypedGo = "typed go"

// The outcomes of the manager's hand-on question that send nothing, seen in its AskUserQuestion pick.
const (
	Dropped   = "dropped"   // the user picked DropIt
	Corrected = "corrected" // the user typed under Other or picked another option
	Refused   = "refused"   // the user picked HandItOn, but the gate refused the send
)

// The options of the manager's hand-on question.
const (
	HandItOn = "Hand it on"
	DropIt   = "Drop it"
)

// Handoff is one thing the manager sent a boss (Kind one of Kinds), or a hand-on question that sent nothing (Kind
// Dropped, Corrected or Refused, Line its question's or the brief's first line). At is unix seconds. Go is a TypedGo's
// quoted words.
type Handoff struct {
	Kind string `json:"kind"`
	At   int64  `json:"at"`
	Boss string `json:"boss,omitempty"`
	Line string `json:"line"`
	Go   string `json:"go,omitempty"`
}

// Handoffs is the manager's hand-on record of one tmux server.
type Handoffs struct{ path string }

// OpenHandoffs is the hand-on record of the tmux server at socket, under $XDG_STATE_HOME/dev/handoffs.
func OpenHandoffs(socket string) (Handoffs, error) {
	dir, err := config.StateDir()
	if err != nil {
		return Handoffs{}, err
	}
	name := strings.Trim(unsafe.ReplaceAllString(socket, "_"), "_")
	return Handoffs{path: filepath.Join(dir, "handoffs", name+".json")}, nil
}

// Add records h.
func (s Handoffs) Add(h Handoff) error {
	lock, err := lockPath(s.path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	all, err := s.read()
	if err != nil {
		return err
	}
	return writeJSON(s.path, append(all, h))
}

// List is every hand-on: ungated kinds first, then KindWork and TypedGo, then Dropped, Corrected and Refused, newest
// first in each.
func (s Handoffs) List() ([]Handoff, error) {
	lock, err := lockPath(s.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	all, err := s.read()
	if err != nil {
		return nil, err
	}
	rank := func(kind string) int {
		switch kind {
		case KindWork, TypedGo:
			return 1
		case Dropped, Corrected, Refused:
			return 2
		}
		return 0
	}
	// Newest first also among hand-ons in the same second
	slices.Reverse(all)
	slices.SortStableFunc(all, func(a, b Handoff) int {
		return cmp.Or(cmp.Compare(rank(a.Kind), rank(b.Kind)), cmp.Compare(b.At, a.At))
	})
	return all, nil
}

func (s Handoffs) read() ([]Handoff, error) {
	all := []Handoff{}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("handoffs %s: %w", s.path, err)
	}
	return all, nil
}

// Tally is the trial's counts of hs: "gated 2 · typed go 1 · ungated: question 1, pick 0, own 0 · dropped 1 · corrected 0 · refused 0".
func Tally(hs []Handoff) string {
	n := map[string]int{}
	for _, h := range hs {
		n[h.Kind]++
	}
	ungated := make([]string, 0, len(Kinds)-1)
	for _, k := range Kinds[1:] {
		ungated = append(ungated, fmt.Sprintf("%s %d", k, n[k]))
	}
	return fmt.Sprintf("gated %d · %s %d · ungated: %s · %s %d · %s %d · %s %d", n[KindWork], TypedGo, n[TypedGo], strings.Join(ungated, ", "), Dropped, n[Dropped], Corrected, n[Corrected], Refused, n[Refused])
}

// IsPicked reports whether answer is option, in any case, also marked " (Recommended)".
func IsPicked(answer, option string) bool {
	answer = strings.TrimSpace(answer)
	if n := len(answer) - len(" (Recommended)"); n >= 0 && strings.EqualFold(answer[n:], " (Recommended)") {
		answer = answer[:n]
	}
	return strings.EqualFold(answer, option)
}

// FirstLine is text's first non-blank line, whitespace collapsed.
func FirstLine(text string) string {
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			return l
		}
	}
	return ""
}

// IsHandOn reports question, offering labels, the manager's hand-on question: it offers HandItOn or its first line
// starts "Hand on to boss-", as the hand-on gate wants it. An item's ask offering HandItOn
// counts too: the safe side, as a dialog mixing a hand-on question with others relays nothing.
func IsHandOn(question string, labels []string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return IsPicked(l, HandItOn) }) ||
		strings.HasPrefix(strings.ToLower(FirstLine(question)), "hand on to boss-")
}

// handOnOutcome is Dropped or Corrected for a lone pick on the manager's hand-on question, "" for any other pick: one
// among others sends nothing whatever was picked, as it stays alone in its dialog, asked again.
func handOnOutcome(picks []agent.Pick) string {
	if len(picks) != 1 || !slices.ContainsFunc(picks[0].Labels, func(l string) bool { return IsPicked(l, HandItOn) }) {
		return ""
	}
	switch a := picks[0].Answer; {
	case picks[0].Typed():
		return Corrected
	case IsPicked(a, HandItOn):
		return ""
	case IsPicked(a, DropIt):
		return Dropped
	}
	return Corrected
}

// recordHandOn records the manager's hand-on question that picks dropped or corrected; any other pick records nothing.
func recordHandOn(c *tmux.Client, picks []agent.Pick, now time.Time) error {
	outcome := handOnOutcome(picks)
	if outcome == "" {
		return nil
	}
	socket, err := Socket(c)
	if err != nil {
		return err
	}
	s, err := OpenHandoffs(socket)
	if err != nil {
		return err
	}
	return s.Add(Handoff{Kind: outcome, At: now.Unix(), Line: FirstLine(picks[0].Question)})
}

// AskingHandOn drops the user's latest prompt and hand-on pick when the manager in pane opens a hand-on question q:
// dismissed, it leaves no earlier pick to hand on with; an item's pick not yet relayed stays (Box.DropPrompt).
func AskingHandOn(c *tmux.Client, pane string, q agent.Question) error {
	handOn := func(a agent.Ask) bool {
		var labels []string
		for _, o := range a.Options {
			labels = append(labels, o.Label)
		}
		return IsHandOn(a.Question, labels)
	}
	if q.Tool != agent.AskUserQuestion || !slices.ContainsFunc(q.Questions, handOn) {
		return nil
	}
	b, ok, err := Coordinator(c, pane)
	if err != nil || !ok || b.Name != Manager {
		return err
	}
	return b.Box.DropPrompt()
}
