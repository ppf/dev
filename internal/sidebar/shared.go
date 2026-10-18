package sidebar

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

// Lookups are the sidebar's branch and PR caches for another view of the sessions (dev herd). Branch
// is only for one goroutine at a time, as in the sidebar's tick chain.
type Lookups struct {
	branches *branchCache
	prs      *prCache
}

// NewLookups returns empty caches running the real git and gh, sharing PRs through c.
func NewLookups(c *tmux.Client) *Lookups {
	return &Lookups{branches: newBranchCache(), prs: newPRCache(c)}
}

// Branch returns path's branch, "" outside a repo.
func (l *Lookups) Branch(path string) string { return l.branches.get(path) }

// PR returns branch's PR as last looked up in path by the agent pane whose prOption reads stored and
// @dev_pr_gen gen, starting a background lookup when due.
func (l *Lookups) PR(pane, stored, gen, path, branch string) PR {
	return l.prs.get(pane, stored, gen, path, branch)
}

// OpenPR shows url in the browser.
func (l *Lookups) OpenPR(url string) error { return l.prs.open(url) }

// ActiveClient returns the most recently active client viewing session.
func ActiveClient(clients []Client, session string) (Client, bool) {
	return activeClient(clients, session)
}

// Header is s as one line of width columns in the sidebar's style: dot, name, agent, state, cost, PR
// tag and branch, red while waiting, on the selection background when selected.
func Header(s Session, width int, selected bool) string {
	colour := muted
	if s.State == agent.Waiting {
		colour = red
	}
	meta := s.Agent
	if s.State != "" {
		meta += " · " + s.State
	}
	if s.Cost != "" {
		meta += " · " + s.Cost
	}
	line := " " + statusDot(s.Agent, s.State) + " " + bold + colour + s.Name + reset + " " + colour + meta + reset
	if s.PR.Number != 0 {
		line += " " + prColour(s.PR.State) + fmt.Sprintf("#%d %s", s.PR.Number, s.PR.State) + reset
	}
	if s.Branch != "" {
		line += " " + faint + s.Branch + reset
	}
	line = truncate(line, width)
	return cardLine(line, ansi.StringWidth(line), width, selected)
}

// HintRow is text as a faint bottom row.
func HintRow(text string, width int) string {
	return " " + faint + truncate(text, width-1) + reset
}

// AlertRow is msg as a red bottom row of width columns.
func AlertRow(msg string, width int) string {
	return " " + red + truncate(strings.Join(strings.Fields(msg), " "), width-1) + reset
}
