package kanban

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
)

type chip struct{ text, colour string }

// marks are what a card shows beside its text, as the web page's marks: state (a start's, a PR's, its worker's) at the
// head's right, chips on the foot, line (what is done on it now, or that no one is: parked) in lineColour, red (it waits
// on the user).
type marks struct {
	state, stateColour string
	chips              []chip
	line, lineColour   string
	red                bool
}

// marksOf is c's marks; a ticked card has none.
func marksOf(c card, now time.Time) marks {
	it := c.it
	m := marks{red: c.needsYou(), lineColour: sidebar.Gold}
	if c.done {
		return m
	}
	switch {
	case it.StartLate:
		m.state, m.stateColour = "start not picked up", sidebar.Red
	case it.Started != nil:
		m.state, m.stateColour = "starting · "+it.Started.To+" · "+sidebar.AgeSpan(now.Sub(time.Unix(it.Started.At, 0))), sidebar.Gold
	case it.Status == sidebar.StatusBlocked:
		m.state, m.stateColour = "blocked · needs you", sidebar.Red
	case it.Status == sidebar.StatusAsks:
		m.state, m.stateColour = sidebar.StatusWord(it.Status), sidebar.StatusColours[it.Status]
	case it.Status == sidebar.StatusGone:
		who := it.On
		if it.Session != nil {
			who = it.Session.Name
		}
		m.state, m.stateColour = "worker "+who+" gone", sidebar.Red
	default:
		m.state, m.stateColour = checkState(c, now)
	}
	if m.state == "" {
		switch {
		case it.Status == "":
			// its boss's workers unread
			m.state, m.stateColour = "?", sidebar.Muted
		case it.Assignee.Kind == sidebar.AssigneeWorker && slices.Contains([]string{sidebar.StatusWorking, sidebar.StatusIdle, sidebar.StatusWaiting}, it.Status):
			m.state, m.stateColour = it.Status, sidebar.StatusColours[it.Status]
		}
	}
	switch {
	case it.Status == sidebar.StatusDecide:
		m.chips = append(m.chips, chip{"decide", sidebar.Red})
	case m.red && !it.StartLate && !strings.Contains(m.state, "needs you"):
		m.chips = append(m.chips, chip{"needs you", sidebar.Red})
	}
	if it.WaitingFor != "" {
		text := "waiting for " + it.WaitingFor
		if c.closed {
			text += " · no one follows up"
		}
		m.chips = append(m.chips, chip{text, sidebar.Gold})
	}
	if c.closed {
		m.chips = append(m.chips, chip{"boss closed", sidebar.Muted})
	}
	if it.PartOf != 0 {
		m.chips = append(m.chips, chip{"⇄ #" + strconv.Itoa(it.PartOf), sidebar.Gold})
	}
	m.line = it.Now
	switch {
	case it.WaitsOn != nil:
		m.line, m.lineColour = "waits on your answer to #"+strconv.Itoa(it.WaitsOn.ID), sidebar.Red
	case it.Doing != nil:
		m.line = it.Doing.Text
	case it.Parked:
		m.line, m.lineColour = "parked · "+sidebar.ParkedLine(it.Assignee.Name), sidebar.Muted
	}
	return m
}

// checkState is a PR's or a stale item's check: not a boss closed's (the chip says it), nor an unchanged one's on an item
// that isn't a check (the age says it).
func checkState(c card, now time.Time) (string, string) {
	ck := c.it.Check
	if ck == nil || ck.Kind == sidebar.CheckBossClosed || ck.Kind == sidebar.CheckUnchanged && c.it.Status != sidebar.StatusCheck {
		return "", ""
	}
	switch ck.Kind {
	case sidebar.CheckApproved, sidebar.CheckGreen, sidebar.CheckMerged:
		return ck.Short(now), sidebar.Green
	case sidebar.CheckUnchanged:
		return ck.Short(now), sidebar.Gold
	}
	return ck.Short(now), sidebar.Red
}
