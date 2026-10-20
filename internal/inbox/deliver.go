package inbox

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// sessionView is what finding a session's inbox reads.
type sessionView struct {
	ID      string `tmux:"session_id"`
	Created string `tmux:"session_created"`
	Name    string `tmux:"session_name"`
	Herd    string `tmux:"@dev_herd"`
	Off     string `tmux:"@dev_boss_inbox"`
	Socket  string `tmux:"socket_path"`
}

func view(c *tmux.Client, target string) (sessionView, error) {
	out, err := c.Run("display-message", "-p", "-t", target, tmux.Format[sessionView]())
	if err != nil {
		return sessionView{}, err
	}
	rows, err := tmux.Parse[sessionView](out)
	if err != nil {
		return sessionView{}, err
	}
	if len(rows) != 1 {
		return sessionView{}, fmt.Errorf("inbox: %d rows for %s", len(rows), target)
	}
	return rows[0], nil
}

// Boss is a boss session with its inbox on.
type Boss struct {
	Name string
	Box  Box
}

// boss is the boss in v, ok=false when v isn't one or its inbox is off.
func (v sessionView) boss() (Boss, bool, error) {
	if v.Herd == "" || v.Off == "off" {
		return Boss{}, false, nil
	}
	box, err := Open(v.Socket, v.ID+"-"+v.Created)
	return Boss{Name: v.Name, Box: box}, err == nil, err
}

// workerView is a worker pane as BossOf reads it.
type workerView struct {
	StartedBy string `tmux:"@dev_started_by"`
	Session   string `tmux:"session_name"`
	Since     string `tmux:"@agent_since"`
	Question  string `tmux:"@agent_question"`
}

// BossOf is the boss that started pane's session (@dev_started_by), and the worker pane itself.
func BossOf(c *tmux.Client, pane string) (b Boss, w workerView, ok bool, err error) {
	out, err := c.Run("display-message", "-p", "-t", pane, tmux.Format[workerView]())
	if err != nil {
		return Boss{}, w, false, err
	}
	rows, err := tmux.Parse[workerView](out)
	if err != nil || len(rows) != 1 || rows[0].StartedBy == "" {
		return Boss{}, w, false, err
	}
	w = rows[0]
	target := w.StartedBy
	if !sidebar.IsSessionID(target) {
		target = tmux.SessionTarget(target)
	}
	v, err := view(c, target)
	if err != nil {
		if strings.Contains(err.Error(), "can't find session") {
			// The boss is gone; its workers outlive it
			return Boss{}, w, false, nil
		}
		return Boss{}, w, false, err
	}
	b, ok, err = v.boss()
	return b, w, ok, err
}

// Self is the boss whose own pane (the coordinator) pane is, ok=false for any other pane.
func Self(c *tmux.Client, pane string) (Boss, bool, error) {
	v, err := view(c, pane)
	if err != nil {
		return Boss{}, false, err
	}
	b, ok, err := v.boss()
	if !ok || err != nil {
		return b, ok, err
	}
	a, err := control.Resolve(c, b.Name)
	if err != nil {
		return Boss{}, false, err
	}
	if a.Pane != pane {
		// The previews pane runs in the boss too
		return Boss{}, false, nil
	}
	return b, true, nil
}

// promptRow is Claude's input row: "❯" (or ">" in older versions) and a space or no-break space.
var promptRow = regexp.MustCompile(`^[│|]?\s*[❯>][ \x{00a0}]?(.*)$`)

// sgr is one ANSI SGR escape.
var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// InputEmpty reports whether Claude's input box in screen (capture-pane -p -e) is empty: the last
// prompt row between two horizontal rules holds nothing but dim placeholder text, and no draft line
// follows it. Anything it doesn't recognise counts as not empty, so a paste never joins a draft.
func InputEmpty(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for i := len(lines) - 1; i > 0; i-- {
		if !isRule(lines[i]) {
			continue
		}
		// lines[i] is the box's bottom rule: find its top rule
		for j := i - 1; j >= 0; j-- {
			if !isRule(lines[j]) {
				continue
			}
			if j+1 >= i {
				return false
			}
			m := promptRow.FindStringSubmatch(undimmed(lines[j+1]))
			if m == nil || strings.TrimSpace(m[1]) != "" {
				return false
			}
			for _, l := range lines[j+2 : i] {
				if strings.TrimSpace(plain(l)) != "" {
					return false
				}
			}
			return true
		}
		return false
	}
	return false
}

// isRule: a line that is only box-drawing horizontals (Claude's input box edges), at least 10 wide.
func isRule(line string) bool {
	p := strings.TrimSpace(plain(line))
	return len([]rune(p)) >= 10 && strings.Trim(p, "─━╌╍┄┅╭╮╰╯") == ""
}

// plain drops all escape sequences.
func plain(line string) string {
	return ansiAll.ReplaceAllString(line, "")
}

var ansiAll = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\))`)

// undimmed is line's text outside dim (SGR 2) runs, without escapes.
func undimmed(line string) string {
	line = ansiAll.ReplaceAllStringFunc(line, func(e string) string {
		if sgr.MatchString(e) {
			return e
		}
		return ""
	})
	var b strings.Builder
	dim := false
	for len(line) > 0 {
		loc := sgr.FindStringSubmatchIndex(line)
		if loc == nil {
			if !dim {
				b.WriteString(line)
			}
			break
		}
		if !dim {
			b.WriteString(line[:loc[0]])
		}
		params := strings.Split(line[loc[2]:loc[3]], ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "2":
				dim = true
			case "", "0", "22":
				dim = false
			case "38", "48", "58":
				// Extended colours: their operands (5;n or 2;r;g;b) aren't attributes
				if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				} else if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				}
			}
		}
		line = line[loc[1]:]
	}
	return b.String()
}

// Deliver pastes boss's pending notes into its coordinator pane as one prompt, only while the
// coordinator is idle with an empty input box, at most once per pasteGap. A batch that can't be pasted
// goes back to the inbox. delivered reports a paste.
func Deliver(c *tmux.Client, clk control.Clock, b Boss) (delivered bool, err error) {
	a, err := control.Resolve(c, b.Name)
	if err != nil {
		return false, err
	}
	ready := func() (bool, error) {
		st, err := control.ReadChecked(c, a.Pane)
		if err != nil {
			return false, err
		}
		if !resting(st.Trusted(), st.WaitingFor) {
			return false, nil
		}
		screen, err := c.Run("capture-pane", "-p", "-e", "-t", a.Pane)
		return err == nil && InputEmpty(screen), err
	}
	if ok, err := ready(); !ok || err != nil {
		return false, err
	}
	bt, err := b.Box.TakeForPaste(clk.Now())
	if err != nil || len(bt.Notes) == 0 {
		return false, err
	}
	// The user may have started typing while the inbox was locked
	ok, err := ready()
	if ok && err == nil {
		err = control.Prompt(c, clk, a.Pane, bt.String())
	}
	if !ok || err != nil {
		if rerr := b.Box.Restore(bt); rerr != nil {
			return false, rerr
		}
		return false, err
	}
	return true, nil
}

// Here is the boss session pane is in, ok=false outside a boss or with its inbox off.
func Here(c *tmux.Client, pane string) (Boss, bool, error) {
	v, err := view(c, pane)
	if err != nil {
		return Boss{}, false, err
	}
	return v.boss()
}
