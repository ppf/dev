// Package bossclear clears an idle boss's context safely (dev agent clear): the checks, the handoff file the boss
// writes and dev verifies, and the proof file the fresh boss writes from it.
package bossclear

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/agent"
)

// Sections are a handoff file's "## " headings, in order.
var Sections = []string{"Key facts", "Open asks", "Work in flight", "The user's standing words", "Open risks", "Old transcript"}

// ProofSections are a proof file's.
var ProofSections = []string{"Key facts", "Open asks", "Roles"}

const (
	// MaxFacts is how many key facts a handoff holds at most.
	MaxFacts = 15
	// MinContext is the share of its context window (percent) a boss is cleared from without --force: about 200k
	// tokens of a 1M window.
	MinContext = 20
)

// Pane is what the checks read of the boss or one of its workers.
type Pane struct {
	Session    string
	State      string // trusted hook state; "" when unknown
	WaitingFor string
	Wait       string // its planned wait (dev agent wait-until), as told; "" for none
	Resume     bool   // a rate-limit resume is ahead
}

// Boss is what the checks read of the boss.
type Boss struct {
	Pane
	IsBoss       bool
	Conversation string // its hook's session id; "" when it records none
	InputEmpty   bool
	Context      *int // share of its context window in use, percent; nil when unknown
	Queued       bool // inbox notes wait for it
}

// Check refuses a clear, naming every failed check: b isn't a boss whose hook records its session, it isn't idle
// with an empty input box, a worker of it works (or has no hook state) or waits on a decision, notes wait in its
// inbox, a planned wait or rate-limit resume is ahead for it or a worker, or, without force, its context share is
// unknown or under MinContext.
func Check(b Boss, workers []Pane, force bool) error {
	if !b.IsBoss {
		return fmt.Errorf("%s isn't a boss (dev boss): only a boss is cleared this way", b.Session)
	}
	if b.Conversation == "" {
		return fmt.Errorf("%s's hook records no session (Codex, Grok, or its agent exited): its /clear can't be confirmed", b.Session)
	}
	var failed []string
	if why := busy(b.Pane); why != "" {
		failed = append(failed, why)
	} else if !b.InputEmpty {
		failed = append(failed, "its input box holds text, maybe the user's draft")
	}
	for _, w := range workers {
		if why := busy(w); why != "" {
			failed = append(failed, "worker "+w.Session+" "+why)
		}
	}
	if b.Queued {
		failed = append(failed, "inbox notes wait for it: let it read them first")
	}
	for _, p := range append([]Pane{b.Pane}, workers...) {
		who := "it"
		if p.Session != b.Session {
			who = "worker " + p.Session
		}
		if p.Wait != "" {
			failed = append(failed, who+" has a planned wait ("+p.Wait+")")
		}
		if p.Resume {
			failed = append(failed, who+" has a rate-limit resume ahead")
		}
	}
	switch {
	case force:
	case b.Context == nil:
		failed = append(failed, "its context share is unknown (its status line doesn't run dev statusline): --force clears it anyway")
	case *b.Context < MinContext:
		failed = append(failed, fmt.Sprintf("its context is %d%%, under %d%% (about 200k tokens of a 1M window): --force clears it anyway", *b.Context, MinContext))
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s not cleared: %s", b.Session, strings.Join(failed, "; "))
	}
	return nil
}

// busy is why p can't be left as it is: working, no hook state (maybe working) or waiting on a decision; "" else.
func busy(p Pane) string {
	switch {
	case p.State == "":
		return "has no hook state, so it may be working"
	case p.State == agent.Working:
		return "is working"
	case agent.Deciding(p.State, p.WaitingFor):
		return "waits on a decision (" + p.WaitingFor + ")"
	}
	return ""
}

// Fact is one numbered key fact: its number, its text (lines joined) and its mark, "confirmed" or "unconfirmed"
// ("" when it ends in neither).
type Fact struct {
	N    int
	Text string
	Mark string
}

var (
	heading  = regexp.MustCompile(`^#{2,}\s+(.+?)\s*#*\s*$`)
	numbered = regexp.MustCompile(`^\s*(\d+)[.)]\s+(.*)$`)
	// A fact ends in "unconfirmed", or in "confirmed: <item number, PR or file>", maybe in parentheses or emphasis
	unconfirmed = regexp.MustCompile(`(?i)(?:^|[^a-z])unconfirmed$`)
	confirmed   = regexp.MustCompile(`(?i)(?:^|[^a-z])confirmed:\s*\S.*$`)
	itemRef     = regexp.MustCompile(`#(\d+)\b`)
	trailing    = " \t.)]*_`"
	emphasis    = strings.NewReplacer("**", "", "__", "", "`", "")
)

// sections splits markdown text at its "## " headings; a heading's key is its text, lower-cased.
func sections(text string) map[string][]string {
	out := map[string][]string{}
	key := ""
	for line := range strings.SplitSeq(text, "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			key = strings.ToLower(strings.Trim(emphasis.Replace(m[1]), " :"))
			out[key] = []string{}
			continue
		}
		if key != "" {
			out[key] = append(out[key], line)
		}
	}
	return out
}

// missing lists the headings of want that text's sections lack, as "## <heading>".
func missing(secs map[string][]string, want []string) []string {
	var miss []string
	for _, h := range want {
		if _, ok := secs[strings.ToLower(h)]; !ok {
			miss = append(miss, "## "+h)
		}
	}
	return miss
}

// facts reads the numbered facts in lines; a fact's following unnumbered lines are part of it.
func facts(lines []string) []Fact {
	var out []Fact
	for _, l := range lines {
		if m := numbered.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			out = append(out, Fact{N: n, Text: strings.TrimSpace(m[2])})
			continue
		}
		if t := strings.TrimSpace(l); t != "" && len(out) > 0 {
			out[len(out)-1].Text += " " + t
		}
	}
	for i, f := range out {
		end := strings.TrimRight(emphasis.Replace(f.Text), trailing)
		switch {
		case unconfirmed.MatchString(end):
			out[i].Mark = "unconfirmed"
		case confirmed.MatchString(end):
			out[i].Mark = "confirmed"
		}
	}
	return out
}

// refs is the item numbers (#n) text names.
func refs(text string) map[int]bool {
	out := map[int]bool{}
	for _, m := range itemRef.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		out[n] = true
	}
	return out
}

// numbers is ids as "#n, #m".
func numbers(ids []int) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = "#" + strconv.Itoa(id)
	}
	return strings.Join(s, ", ")
}

// Handoff is a handoff file that passed VerifyHandoff: its key facts.
type Handoff struct {
	Facts []Fact
}

// VerifyHandoff checks handoff text and returns its key facts, or every miss: a section missing, no key facts or more
// than MaxFacts, a fact ending in neither "confirmed: …" nor "unconfirmed", an open item's number (open) it doesn't
// name, or the old transcript's path (when known) it doesn't hold.
func VerifyHandoff(text string, open []int, transcript string) (Handoff, []string) {
	secs := sections(text)
	var miss []string
	if m := missing(secs, Sections); len(m) > 0 {
		miss = append(miss, "sections missing: "+strings.Join(m, ", "))
	}
	fs := facts(secs["key facts"])
	switch {
	case len(fs) == 0:
		miss = append(miss, "no numbered key facts under ## Key facts")
	case len(fs) > MaxFacts:
		miss = append(miss, fmt.Sprintf("%d key facts, at most %d", len(fs), MaxFacts))
	}
	var unmarked []string
	for _, f := range fs {
		if f.Mark == "" {
			unmarked = append(unmarked, strconv.Itoa(f.N))
		}
	}
	if len(unmarked) > 0 {
		miss = append(miss, "key facts "+strings.Join(unmarked, ", ")+" end in neither \"confirmed: <item number, PR or file>\" nor \"unconfirmed\"")
	}
	named := refs(text)
	var unnamed []int
	for _, id := range open {
		if !named[id] {
			unnamed = append(unnamed, id)
		}
	}
	if len(unnamed) > 0 {
		miss = append(miss, "open items not named: "+numbers(unnamed))
	}
	if transcript != "" && !strings.Contains(text, transcript) {
		miss = append(miss, "the old transcript's path "+transcript+" isn't in it")
	}
	return Handoff{Facts: fs}, miss
}

// VerifyProof checks proof text against handoff h and returns every miss: a section missing, a key fact of h not
// restated under its number or with another mark, or an open ask's number (asks) not under ## Open asks, or nothing
// under ## Roles. A miss of a fact quotes it, for the boss to fix.
func VerifyProof(proof string, h Handoff, asks []int) []string {
	secs := sections(proof)
	var miss []string
	if m := missing(secs, ProofSections); len(m) > 0 {
		miss = append(miss, "sections missing: "+strings.Join(m, ", "))
	}
	got := map[int]Fact{}
	for _, f := range facts(secs["key facts"]) {
		got[f.N] = f
	}
	for _, f := range h.Facts {
		p, ok := got[f.N]
		switch {
		case !ok:
			miss = append(miss, fmt.Sprintf("key fact %d not restated: %s", f.N, f.Text))
		case p.Mark != f.Mark:
			miss = append(miss, fmt.Sprintf("key fact %d is marked %s in the handoff, %s in the proof", f.N, f.Mark, cmp.Or(p.Mark, "neither")))
		}
	}
	if extra := len(got) - len(h.Facts); extra > 0 {
		miss = append(miss, fmt.Sprintf("%d more key facts than the handoff's %d", extra, len(h.Facts)))
	}
	named := refs(strings.Join(secs["open asks"], "\n"))
	var unnamed []int
	for _, id := range asks {
		if !named[id] {
			unnamed = append(unnamed, id)
		}
	}
	if len(unnamed) > 0 {
		miss = append(miss, "open asks not under ## Open asks: "+numbers(unnamed))
	}
	if r, ok := secs["roles"]; ok && !slices.ContainsFunc(r, func(l string) bool { return strings.TrimSpace(l) != "" }) {
		miss = append(miss, "nothing under ## Roles")
	}
	return miss
}
