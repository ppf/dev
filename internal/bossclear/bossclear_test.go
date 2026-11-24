package bossclear

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
)

func pct(n int) *int { return &n }

// idleBoss passes every check.
func idleBoss() Boss {
	return Boss{Pane: Pane{Session: "boss-app", State: agent.Idle}, IsBoss: true, Conversation: "s1", InputEmpty: true, Context: pct(35)}
}

// Every refusal names its check; an idle boss sitting in Claude's idle reminder, idle workers and open asks don't
// refuse; --force skips only the context check.
func TestCheck(t *testing.T) {
	idleWorker := Pane{Session: "app-fix", State: agent.Idle}
	for _, tt := range []struct {
		name    string
		boss    func(*Boss)
		workers []Pane
		force   bool
		want    string // "" passes
	}{
		{"idle boss, idle worker", nil, []Pane{idleWorker}, false, ""},
		{"boss resting at Claude's idle reminder", func(b *Boss) { b.State, b.WaitingFor = agent.Waiting, agent.IdlePrompt }, nil, false, ""},
		{"not a boss", func(b *Boss) { b.IsBoss = false }, nil, false, "boss-app isn't a boss (dev boss)"},
		{"no session recorded", func(b *Boss) { b.Conversation = "" }, nil, false, "hook records no session"},
		{"boss working", func(b *Boss) { b.State = agent.Working }, nil, false, "not cleared: is working"},
		{"boss without hook state", func(b *Boss) { b.State = "" }, nil, false, "has no hook state, so it may be working"},
		{"boss on a permission prompt", func(b *Boss) { b.State, b.WaitingFor = agent.Waiting, agent.PermissionPrompt }, nil, false, "waits on a decision (permission_prompt)"},
		{"draft in its input box", func(b *Boss) { b.InputEmpty = false }, nil, false, "its input box holds text, maybe the user's draft"},
		{"worker working", nil, []Pane{{Session: "app-fix", State: agent.Working}}, false, "worker app-fix is working"},
		{"worker without hook state", nil, []Pane{{Session: "app-fix"}}, false, "worker app-fix has no hook state"},
		{"worker on a decision", nil, []Pane{{Session: "app-fix", State: agent.Waiting, WaitingFor: agent.PermissionPrompt}}, false, "worker app-fix waits on a decision"},
		{"inbox notes queued", func(b *Boss) { b.Queued = true }, nil, false, "inbox notes wait for it"},
		{"boss planned wait", func(b *Boss) { b.Wait = "until 16:30 · CI" }, nil, false, "it has a planned wait (until 16:30 · CI)"},
		{"worker planned wait", nil, []Pane{{Session: "app-fix", State: agent.Idle, Wait: "until 16:30 · CI"}}, false, "worker app-fix has a planned wait"},
		{"boss resume ahead", func(b *Boss) { b.Resume = true }, nil, false, "it has a rate-limit resume ahead"},
		{"worker resume ahead", nil, []Pane{{Session: "app-fix", State: agent.Idle, Resume: true}}, false, "worker app-fix has a rate-limit resume ahead"},
		{"small context", func(b *Boss) { b.Context = pct(12) }, nil, false, "its context is 12%, under 20%"},
		{"unknown context", func(b *Boss) { b.Context = nil }, nil, false, "its context share is unknown"},
		{"small context forced", func(b *Boss) { b.Context = pct(12) }, nil, true, ""},
		{"unknown context forced", func(b *Boss) { b.Context = nil }, nil, true, ""},
		{"force skips no other check", func(b *Boss) { b.Context, b.Queued = pct(5), true }, nil, true, "inbox notes wait for it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := idleBoss()
			if tt.boss != nil {
				tt.boss(&b)
			}
			err := Check(b, tt.workers, tt.force)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("Check = %v, want it to pass", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("Check = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// Every failed check is named at once, not only the first.
func TestCheckNamesAll(t *testing.T) {
	b := idleBoss()
	b.Queued, b.Context = true, pct(3)
	err := Check(b, []Pane{{Session: "w", State: agent.Working}}, false)
	if err == nil {
		t.Fatal("Check passed")
	}
	for _, want := range []string{"worker w is working", "inbox notes wait", "its context is 3%"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check = %v, want %q in it", err, want)
		}
	}
}

const transcript = "/home/u/.claude/projects/p/s1.jsonl"

// goodHandoff names items #3 and #12 and the transcript, with three marked facts.
const goodHandoff = `# Handoff boss-app

## Key facts
1. PR #40 merged, v1.2 released. confirmed: PR #40
2. The user wants the mail sent Friday,
   not before. **confirmed: #3**
3. Staging deploy works (unconfirmed)

## Open asks
- #12: "Ship now" starts the release; "Wait" parks it.

## Work in flight
None.

## The user's standing words
Merge only after CI is green.

## Open risks
Flaky test in CI.

## Old transcript
` + transcript + `
`

// A handoff passes with every section, 1-15 marked facts, every open item and the transcript; each miss is named.
func TestVerifyHandoff(t *testing.T) {
	h, miss := VerifyHandoff(goodHandoff, []int{3, 12}, transcript, nil)
	if len(miss) > 0 {
		t.Fatalf("misses %q", miss)
	}
	marks := []string{}
	for _, f := range h.Facts {
		marks = append(marks, f.Mark)
	}
	if got := strings.Join(marks, " "); got != "confirmed confirmed unconfirmed" {
		t.Errorf("marks = %s", got)
	}
	if !strings.Contains(h.Facts[1].Text, "not before") {
		t.Errorf("fact 2 = %q, want its second line joined", h.Facts[1].Text)
	}

	// extra is goodHandoff with n more facts, numbered on from its 3
	extra := func(n int) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(strconv.Itoa(4+i) + ". fact unconfirmed\n")
		}
		return strings.Replace(goodHandoff, "## Open asks", b.String()+"## Open asks", 1)
	}
	fact3 := "3. Staging deploy works (unconfirmed)"
	for _, tt := range []struct {
		name, text string
		open       []int
		want       string
	}{
		{"section missing", strings.Replace(goodHandoff, "## Open risks", "## Risks", 1), []int{3, 12}, "sections missing: ## Open risks"},
		{"no numbered facts", "## Key facts\nnone\n## Open asks\n## Work in flight\n## The user's standing words\n## Open risks\n## Old transcript\n" + transcript, nil, "no numbered key facts"},
		{"15 facts pass", extra(12), []int{3, 12}, ""},
		{"over 15 facts", extra(13), []int{3, 12}, "16 key facts, at most 15"},
		{"number used twice", strings.Replace(goodHandoff, "3. Staging", "2. Staging", 1), []int{3, 12}, "key fact numbers used twice: 2"},
		{"unmarked fact", strings.Replace(goodHandoff, " (unconfirmed)", "", 1), []int{3, 12}, "key facts 3 end in neither"},
		{"confirmed: mid-sentence is no mark", strings.Replace(goodHandoff, fact3, "3. Deploy confirmed: no, still pending", 1), []int{3, 12}, "key facts 3 end in neither"},
		{"confirmed: with nothing after", strings.Replace(goodHandoff, fact3, "3. Deploy done. confirmed:", 1), []int{3, 12}, "key facts 3 end in neither"},
		{"confirmed: after a dash", strings.Replace(goodHandoff, fact3, "3. Deploy done - confirmed: #3", 1), []int{3, 12}, ""},
		{"indented sub-list stays in its fact", strings.Replace(goodHandoff, fact3, "3. Staging works:\n   1. api\n   2. web\n   unconfirmed", 1), []int{3, 12}, ""},
		{"open item not named", goodHandoff, []int{3, 12, 7}, "open items not named: #7"},
		{"#12 doesn't name #1", goodHandoff, []int{1}, "open items not named: #1"},
		{"PR #40 names no item #40", goodHandoff, []int{3, 40}, "open items not named: #40"},
		{"bold and bulleted numbers", strings.Replace(strings.Replace(goodHandoff, "1. PR", "**1.** PR", 1), "2. The user", "- 2. The user", 1), []int{3, 12}, ""},
		{"transcript missing", strings.Replace(goodHandoff, transcript, "lost", 1), []int{3, 12}, "the old transcript's path " + transcript},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, miss := VerifyHandoff(tt.text, tt.open, transcript, nil)
			got := strings.Join(miss, "; ")
			if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
				t.Errorf("misses = %q, want %q", got, tt.want)
			}
		})
	}
}

const goodProof = `## Key facts
1. PR #40 merged and released. confirmed: PR #40
2. Mail goes Friday. confirmed: #3
3. Staging deploy works. unconfirmed

## Open asks
#12: Ship now → I start the release worker; Wait → I park it.

## Roles
The manager answers my workers' permission prompts and dialogs and relays my asks.
`

// A proof passes with each fact restated under its number and mark, each open ask, and roles; each miss is named,
// a missing fact quoted.
func TestVerifyProof(t *testing.T) {
	h, miss := VerifyHandoff(goodHandoff, []int{3, 12}, transcript, nil)
	if len(miss) > 0 {
		t.Fatal(miss)
	}
	if miss := VerifyProof(goodProof, h, []int{12}); len(miss) > 0 {
		t.Fatalf("misses %q", miss)
	}
	for _, tt := range []struct {
		name, proof string
		asks        []int
		want        string
	}{
		{"fact dropped", strings.Replace(goodProof, "2. Mail goes Friday. confirmed: #3\n", "", 1), []int{12}, "key fact 2 not restated: The user wants the mail sent Friday, not before."},
		{"mark changed", strings.Replace(goodProof, "works. unconfirmed", "works. confirmed: staging", 1), []int{12}, "key fact 3 is marked unconfirmed in the handoff, confirmed in the proof"},
		{"mark dropped", strings.Replace(goodProof, "works. unconfirmed", "works.", 1), []int{12}, "key fact 3 is marked unconfirmed in the handoff, neither in the proof"},
		{"number used twice", strings.Replace(goodProof, "2. Mail goes Friday. confirmed: #3", "1. Mail goes Friday. confirmed: #3", 1), []int{12}, "key fact numbers used twice: 1"},
		{"extra fact", strings.Replace(goodProof, "## Open asks", "4. Something new. unconfirmed\n\n## Open asks", 1), []int{12}, "1 more key facts than the handoff's 3"},
		{"ask missing", goodProof, []int{12, 14}, "open asks not under ## Open asks: #14"},
		{"ask only elsewhere", strings.Replace(goodProof, "#12: ", "", 1), []int{12}, "open asks not under ## Open asks: #12"},
		{"roles empty", strings.Replace(goodProof, "The manager answers my workers' permission prompts and dialogs and relays my asks.\n", "", 1), []int{12}, "nothing under ## Roles"},
		{"section missing", strings.Replace(goodProof, "## Roles", "## Who", 1), []int{12}, "sections missing: ## Roles"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(VerifyProof(tt.proof, h, tt.asks), "; ")
			if !strings.Contains(got, tt.want) {
				t.Errorf("misses = %q, want %q", got, tt.want)
			}
		})
	}
}

// The save prompt names the file, every section, the transcript and the facts file; all prompts are one line, as a
// paste.
func TestPrompts(t *testing.T) {
	save := SavePrompt("/s/h.md", transcript, "/s/h-facts.md")
	for _, want := range append([]string{"/s/h.md", transcript, "/s/h-facts.md", "--details", "dev todo ask --replaces", "dev todo note", "at most 15", "never worded as done"}, Sections...) {
		if !strings.Contains(save, want) {
			t.Errorf("save prompt lacks %q", want)
		}
	}
	if strings.Contains(SavePrompt("/s/h.md", transcript, ""), "Merge in") {
		t.Error("save prompt without --facts asks to merge facts")
	}
	proof := ProofPrompt("/s/h.md", "/s/h-proof.md")
	for _, want := range append([]string{"/s/h.md", "/s/h-proof.md", "Before touching any worker", "permission prompts and dialogs"}, ProofSections...) {
		if !strings.Contains(proof, want) {
			t.Errorf("proof prompt lacks %q", want)
		}
	}
	for _, p := range []string{save, proof, FixPrompt("/s/h.md", []string{"a", "b"}), ProofFixPrompt("/s/h.md", "/s/p.md", []string{"a"})} {
		if strings.Contains(p, "\n") {
			t.Errorf("prompt has a newline: %q", p)
		}
	}
}

// A --facts file's facts pass only when each is copied word for word (line breaks and emphasis aside) into a key fact.
func TestVerifyHandoffCarried(t *testing.T) {
	carried := readFacts("1. The user is away until Monday. confirmed: said\n\n2. Staging deploy works (unconfirmed)\n")
	if len(carried) != 2 {
		t.Fatalf("readFacts = %+v", carried)
	}
	copied := strings.Replace(goodHandoff, "## Open asks", "4. The user is away\n   until **Monday**. confirmed: said\n\n## Open asks", 1)
	if _, miss := VerifyHandoff(copied, []int{3, 12}, transcript, carried); len(miss) > 0 {
		t.Errorf("copied: misses %q", miss)
	}
	reworded := strings.Replace(goodHandoff, "## Open asks", "4. The user is out until Monday. confirmed: said\n\n## Open asks", 1)
	_, miss := VerifyHandoff(reworded, []int{3, 12}, transcript, carried)
	if got := strings.Join(miss, "; "); got != "the caller's fact not copied into a key fact word for word: The user is away until Monday. confirmed: said" {
		t.Errorf("reworded: misses %q", got)
	}
}
