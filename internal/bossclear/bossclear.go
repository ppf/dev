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

// Sections are a handoff file's "## " headings, in the order the save prompt asks for.
var Sections = []string{"Key facts", "Open asks", "Work in flight", "The user's standing words", "Roles", "Open risks", "Old transcript"}

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

// NotBoss is the refusal of session, no boss.
func NotBoss(session string) error {
	return fmt.Errorf("%s isn't a boss (dev boss): only a boss is cleared this way", session)
}

// Check refuses a clear, naming every failed check: b isn't a boss whose hook records its session, it works or
// waits on a decision or its input box holds text, a worker of it works (or has no hook state) or waits on a
// decision, notes wait in its inbox, a planned wait or rate-limit resume is ahead for it or a worker, or, without
// force, its context share is unknown or under MinContext.
func Check(b Boss, workers []Pane, force bool) error {
	if !b.IsBoss {
		return NotBoss(b.Session)
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
	heading = regexp.MustCompile(`^#{2,}\s+(.+?)\s*#*\s*$`)
	// At the line's start, maybe after a list bullet: an indented numbered line is part of the fact above. "1.fact"
	// too, but not "2.5x" or "0.82.0", a wrapped line of the fact above
	numbered = regexp.MustCompile(`^(?:[-*+] )?(\d+)[.)](?:\s+(.*)|([^\d\s].*))$`)
	// A fact ends in "unconfirmed", or in a last clause "confirmed: <item number, PR or file>" (after a sentence's
	// end, a comma, a dash or in parentheses, not "deploy confirmed: no"), maybe in emphasis
	unconfirmed = regexp.MustCompile(`(?i)(?:^|[^a-z])unconfirmed$`)
	confirmed   = regexp.MustCompile(`(?i)(?:^|[.;,(]\s*|\s[-–—]\s*)confirmed:\s*\S.*$`)
	// A PR's, MR's or issue's number names no item
	itemRef = regexp.MustCompile(`(?i)(\bPR\s*|\bMR\s*|pull/|issues/|merge_requests/)?#(\d+)\b`)
	// A word saying work is done, and one hedging it: an unconfirmed fact with the first and none of the second is
	// worded as done
	doneWord = regexp.MustCompile(`(?i)\b(merged|released|shipped|deployed|fixed|done|finished|completed?|closed|pushed|tagged|passed|passing|green|resolved|live|sent|installed)\b`)
	hedge    = regexp.MustCompile(`(?i)\b(reported|not|no|nothing|never|none|unchecked|unverified|said|says|claims?|according|supposedly|expected|should|would|will|may|might|maybe|probably|likely|if|whether|pending|waiting|yet|unknown|must|needs?|wants?|wanted|asks?|asked|after|before|once|until|when|plans?|planned)\b|n't|\?`)
	trailing = " \t.)]*_`"
	emphasis = strings.NewReplacer("**", "", "__", "", "`", "")
)

// sections splits markdown text at its headings ("##" or deeper); a heading's key is its text without emphasis or a
// trailing colon, lower-cased.
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

// missing lists the headings of want that secs lacks, as "## <heading>".
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
		// "**1.** fact" too
		if m := numbered.FindStringSubmatch(emphasis.Replace(l)); m != nil {
			n, _ := strconv.Atoi(m[1])
			out = append(out, Fact{N: n, Text: strings.TrimSpace(m[2] + m[3])})
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

// StatedDone is the miss of fs's unconfirmed facts worded as done ("PR #41 is merged. unconfirmed"), "" for none. By
// words only: a done word (merged, released, fixed, …) with no hedge (reported, not, may, pending, wants, until, a "?",
// …). It misses a fact done in other words ("PR #41 is in main") or hedged elsewhere in it ("merged, no conflicts"),
// may flag one that only mentions such a word unhedged, and can't tell a confirmed fact's source is real.
func StatedDone(fs []Fact) string {
	var nums, words []string
	for _, f := range fs {
		if f.Mark != "unconfirmed" {
			continue
		}
		text := emphasis.Replace(f.Text)
		if w := doneWord.FindString(text); w != "" && !hedge.MatchString(text) {
			nums, words = append(nums, strconv.Itoa(f.N)), append(words, strconv.Quote(strings.ToLower(w)))
		}
	}
	if len(nums) == 0 {
		return ""
	}
	return fmt.Sprintf("key facts %s state as done what is unconfirmed (%s): word each as not checked (\"reported merged, not checked\") or as what is wanted (\"wants it merged\")",
		strings.Join(nums, ", "), strings.Join(slices.Compact(words), ", "))
}

// readFacts is the numbered facts in markdown text, as a --facts file holds them.
func readFacts(text string) []Fact {
	return facts(strings.Split(text, "\n"))
}

// refs is the item numbers (#n, not a PR's "PR #n") text names.
func refs(text string) map[int]bool {
	out := map[int]bool{}
	for _, m := range itemRef.FindAllStringSubmatch(text, -1) {
		if m[1] == "" {
			n, _ := strconv.Atoi(m[2])
			out[n] = true
		}
	}
	return out
}

// squashed is s's words, emphasis dropped, one space apart, for a fact copied with other line breaks.
func squashed(s string) string {
	return strings.Join(strings.Fields(emphasis.Replace(s)), " ")
}

// unnamed is the ids not in named.
func unnamed(ids []int, named map[int]bool) []int {
	var out []int
	for _, id := range ids {
		if !named[id] {
			out = append(out, id)
		}
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

// repeated is the numbers more than one of fs has.
func repeated(fs []Fact) []string {
	seen := map[int]int{}
	var out []string
	for _, f := range fs {
		if seen[f.N]++; seen[f.N] == 2 {
			out = append(out, strconv.Itoa(f.N))
		}
	}
	return out
}

// Handoff is a handoff file's key facts as VerifyHandoff read them: a handoff only when it reported no miss.
type Handoff struct {
	Facts []Fact
}

// VerifyHandoff checks handoff text and returns its key facts, and every miss: a section missing, no key facts or more
// than MaxFacts, a fact number used twice, a fact ending in neither "confirmed: …" nor "unconfirmed", an unconfirmed
// fact worded as done (StatedDone), nothing under ## Roles, an open item's
// number (open) it doesn't name, the old transcript's path (when known) it doesn't hold, or a fact of the caller's
// (carried) not copied word for word into a key fact.
func VerifyHandoff(text string, open []int, transcript string, carried []Fact) (Handoff, []string) {
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
	if r := repeated(fs); len(r) > 0 {
		miss = append(miss, "key fact numbers used twice: "+strings.Join(r, ", "))
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
	if done := StatedDone(fs); done != "" {
		miss = append(miss, done)
	}
	if nothingUnder(secs, "roles") {
		miss = append(miss, "nothing under ## Roles")
	}
	if ids := unnamed(open, refs(text)); len(ids) > 0 {
		miss = append(miss, "open items not named: "+numbers(ids))
	}
	if transcript != "" && !strings.Contains(text, transcript) {
		miss = append(miss, "the old transcript's path "+transcript+" isn't in it")
	}
	for _, cf := range carried {
		if !slices.ContainsFunc(fs, func(f Fact) bool { return strings.Contains(squashed(f.Text), squashed(cf.Text)) }) {
			miss = append(miss, "the caller's fact not copied into a key fact word for word: "+cf.Text)
		}
	}
	return Handoff{Facts: fs}, miss
}

// VerifyProof checks proof text against handoff h and returns every miss: a section missing, a key fact of h not
// restated under its number (quoted, for the boss to fix) or with another mark, a number used twice, more facts than
// h's, an open ask's number (asks) not under ## Open asks, or nothing under ## Roles.
func VerifyProof(proof string, h Handoff, asks []int) []string {
	secs := sections(proof)
	var miss []string
	if m := missing(secs, ProofSections); len(m) > 0 {
		miss = append(miss, "sections missing: "+strings.Join(m, ", "))
	}
	fs := facts(secs["key facts"])
	if r := repeated(fs); len(r) > 0 {
		miss = append(miss, "key fact numbers used twice: "+strings.Join(r, ", "))
	}
	got := map[int]Fact{}
	for _, f := range fs {
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
	if ids := unnamed(asks, refs(strings.Join(secs["open asks"], "\n"))); len(ids) > 0 {
		miss = append(miss, "open asks not under ## Open asks: "+numbers(ids))
	}
	if nothingUnder(secs, "roles") {
		miss = append(miss, "nothing under ## Roles")
	}
	return miss
}

// nothingUnder reports section key there with only blank lines under it.
func nothingUnder(secs map[string][]string, key string) bool {
	r, ok := secs[key]
	return ok && !slices.ContainsFunc(r, func(l string) bool { return strings.TrimSpace(l) != "" })
}
