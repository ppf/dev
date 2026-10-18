package herd

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
)

// reviewTitle asks to submit the answers to Claude's questions, on the tab after the last question.
const reviewTitle = "Ready to submit your answers?"

const (
	// keyDelay separates the keys of one answer, so the agent redraws between them.
	keyDelay = 200 * time.Millisecond
	// answerTimeout is how long an answer's keys get to change the dialog, polled every answerPoll.
	answerTimeout = 2 * time.Second
	answerPoll    = 200 * time.Millisecond
)

// pick is a tile's dialog as the reply box shows it: the dialog on screen, told apart by the hook's
// question (@agent_question) when that is the dialog shown.
type pick struct {
	d     dialog
	tabs  []string // the question tabs
	cur   int      // the tab on screen; -1 when unknown
	other int      // the free-text option ("Type something"); -1 when none
	// The permission the dialog asks: its tool, command, file or URL, and description
	tool, detail, about string
}

// newPick decorates dialog d with q, the pane's question, when q is what d shows on screen.
func newPick(d dialog, q agent.Question, screen string) pick {
	p := pick{d: d, cur: -1, other: -1}
	switch {
	case q.Tool == agent.AskUserQuestion:
		for i, a := range q.Questions {
			if !sameText(a.Question, d.title) {
				continue
			}
			p.cur = i
			p.d.opts = slices.Clone(d.opts)
			for k, c := range a.Options {
				if k < len(p.d.opts) {
					// Unwrapped, as the screen may cut it
					p.d.opts[k].desc = c.Description
				}
			}
			if len(a.Options) < len(p.d.opts) {
				p.other = len(a.Options)
			}
			p.d.multi = p.d.multi || a.MultiSelect
		}
		if d.title == reviewTitle {
			p.cur = len(q.Questions)
		}
		if p.cur >= 0 && len(q.Questions) > 1 || d.title == reviewTitle {
			for _, a := range q.Questions {
				p.tabs = append(p.tabs, a.Header)
			}
			p.tabs = append(p.tabs, "Submit")
		}
	case q.Tool != "" && strings.HasPrefix(d.title, "Do you want") && strings.Contains(squeeze(screen), needle(q)):
		p.tool, p.detail, p.about = q.Tool, cmp.Or(q.Command, q.File, q.URL), q.Description
	}
	if p.other < 0 {
		p.other = slices.IndexFunc(d.opts, func(o option) bool { return strings.HasPrefix(o.label, "Type something") })
	}
	if p.tabs == nil && len(d.tabs) > 1 {
		p.tabs = d.tabs
	}
	return p
}

// sameText compares texts ignoring how they wrap.
func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// needle is what a permission dialog for q shows of it, without spaces (the screen wraps it): the file's
// name, or its command or URL (up to maxNeedle), else the tool's name.
func needle(q agent.Question) string {
	s := cmp.Or(q.Command, q.URL, q.Tool)
	if q.File != "" {
		s = filepath.Base(q.File)
	}
	r := []rune(squeeze(strings.TrimSuffix(s, "…")))
	return string(r[:min(len(r), maxNeedle)])
}

// maxNeedle bounds how much of a command must show: a long one may be cut on screen.
const maxNeedle = 200

// squeeze drops s's whitespace.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// key is d's sig with what's checked: what an answer must find unchanged before it sends anything.
func (d dialog) key() string {
	var b strings.Builder
	b.WriteString(d.sig())
	for _, o := range d.opts {
		b.WriteString(strconv.FormatBool(o.checked))
	}
	return b.String()
}

// stroke is one key for control.SendKeys, or text typed literally.
type stroke struct{ key, text string }

// answer is one step answering a dialog: its keys for the dialog as it is on screen now, and how to tell they
// took (after is the dialog after them; open=false when it's gone).
type answer struct {
	keys func(now dialog) ([]stroke, error)
	took func(before, after dialog, open bool) bool
}

var errNoCursor = errors.New("can't see the dialog's cursor: answer it in the worker")

// digit is the key picking o; past 9 there's none.
func digit(o option) ([]stroke, error) {
	if o.num > 9 {
		return nil, fmt.Errorf("option %d has no key: answer it in the worker", o.num)
	}
	return []stroke{{key: strconv.Itoa(o.num)}}, nil
}

// changed: the question on screen is another one, or none.
func changed(before, after dialog, open bool) bool { return !open || after.sig() != before.sig() }

// choose picks option k, which answers a single-choice question.
func choose(k int) answer {
	return answer{keys: func(now dialog) ([]stroke, error) { return digit(now.opts[k]) }, took: changed}
}

// toggle flips multi-select option k.
func toggle(k int) answer {
	return answer{
		keys: func(now dialog) ([]stroke, error) { return digit(now.opts[k]) },
		took: func(before, after dialog, open bool) bool {
			return open && after.sig() == before.sig() && after.opts[k].checked != before.opts[k].checked
		},
	}
}

// writeIn answers a single-choice question with text in its free-text option k: its number focuses the
// field, Enter submits.
func writeIn(k int, text string) answer {
	return answer{keys: func(now dialog) ([]stroke, error) {
		keys, err := digit(now.opts[k])
		return append(keys, stroke{text: text}, stroke{key: "enter"}), err
	}, took: changed}
}

// writeInMulti types text into multi-select free-text option k, which checks it: its number only checks it,
// so the cursor moves there. It took when the option's label changed (the screen may wrap or cut the text).
func writeInMulti(k int, text string) answer {
	return answer{
		keys: func(now dialog) ([]stroke, error) {
			keys, err := moves(now.cursor, now.row(k))
			return append(keys, stroke{text: text}), err
		},
		took: func(before, after dialog, open bool) bool { return open && after.sig() != before.sig() },
	}
}

// submit moves to the multi-select's Submit row and presses Enter: on to the next question or the review.
func submit() answer {
	return answer{keys: func(now dialog) ([]stroke, error) {
		if now.submitAt == 0 {
			return nil, errors.New("no Submit row on screen")
		}
		keys, err := moves(now.cursor, now.submitAt)
		return append(keys, stroke{key: "enter"}), err
	}, took: changed}
}

// moves are the arrow keys taking the dialog's cursor from row from to row to.
func moves(from, to int) ([]stroke, error) {
	if from < 0 {
		return nil, errNoCursor
	}
	var keys []stroke
	for ; from < to; from++ {
		keys = append(keys, stroke{key: "down"})
	}
	for ; from > to; from-- {
		keys = append(keys, stroke{key: "up"})
	}
	return keys, nil
}

// answeredMsg is an answer's outcome for session; took reports the dialog changed as the keys meant.
type answeredMsg struct {
	session, msg string
	took         bool
}

// screenDialog captures pane and parses its dialog.
func screenDialog(c *tmux.Client, pane string) (dialog, bool, error) {
	out, err := c.Run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return dialog{}, false, err
	}
	d, ok := parseDialog(out)
	return d, ok, nil
}

// send runs a: it sends nothing unless the dialog on screen is still shown (shown's key), then presses its
// keys keyDelay apart and polls until they took, or answerTimeout passes.
func (m Model) send(target, pane string, shown dialog, a answer) tea.Cmd {
	c, clk := m.c, m.clk
	return func() tea.Msg {
		fail := func(err error) tea.Msg {
			return answeredMsg{session: target, msg: "❌ " + target + ": " + err.Error()}
		}
		now, open, err := screenDialog(c, pane)
		if err != nil {
			return fail(err)
		}
		if !open || now.key() != shown.key() {
			return answeredMsg{session: target, msg: "⚠ " + target + "'s dialog changed: nothing sent, showing it again"}
		}
		keys, err := a.keys(now)
		if err != nil {
			return fail(err)
		}
		for i, k := range keys {
			if i > 0 {
				clk.Sleep(keyDelay)
			}
			if k.text != "" {
				err = control.SendText(c, pane, k.text)
			} else {
				err = control.SendKeys(c, pane, []string{k.key})
			}
			if err != nil {
				return fail(fmt.Errorf("sent %d of %d keys, check the worker: %w", i, len(keys), err))
			}
		}
		for start := clk.Now(); ; {
			clk.Sleep(answerPoll)
			after, open, err := screenDialog(c, pane)
			if err != nil {
				return fail(fmt.Errorf("keys sent, check the worker: %w", err))
			}
			if a.took(now, after, open) {
				return answeredMsg{session: target, msg: "Answered " + target, took: true}
			}
			if clk.Now().Sub(start) >= answerTimeout {
				return fail(errors.New("keys sent, but the dialog didn't change: check the worker"))
			}
		}
	}
}
