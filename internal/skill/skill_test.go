package skill

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var marker = regexp.MustCompile(`^<!-- (/?)boss-core:([a-z0-9-]+) -->$`)

// blocks returns the text between each block's markers in a skill, or the first marker error.
func blocks(text string) (map[string]string, error) {
	got := map[string]string{}
	open, start := "", 0
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "boss-core:") {
			continue
		}
		m := marker.FindStringSubmatch(line)
		switch {
		case m == nil:
			return nil, fmt.Errorf("line %d: stray marker %q", i+1, line)
		case m[1] == "" && open != "":
			return nil, fmt.Errorf("line %d: block %s starts inside block %s", i+1, m[2], open)
		case m[1] == "":
			if _, ok := got[m[2]]; ok {
				return nil, fmt.Errorf("line %d: block %s starts twice", i+1, m[2])
			}
			open, start = m[2], i+1
		case m[2] != open:
			return nil, fmt.Errorf("line %d: block %s ends without its start", i+1, m[2])
		default:
			got[open] = strings.Join(lines[start:i], "\n")
			open = ""
		}
	}
	if open != "" {
		return nil, fmt.Errorf("block %s has no end", open)
	}
	return got, nil
}

func TestSkillFrontmatter(t *testing.T) {
	files, _ := filepath.Glob("*/SKILL.md")
	var embedded []string
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		embedded = append(embedded, name+"/SKILL.md")
	}
	if !slices.Equal(files, embedded) {
		t.Errorf("skills on disk %q, embedded %q", files, embedded)
	}
	for name, text := range byName {
		if !strings.HasPrefix(text, "---\nname: "+name+"\n") {
			t.Errorf("%s/SKILL.md: frontmatter doesn't start with name: %s", name, name)
		}
	}
}

func TestSkillCoreBlocks(t *testing.T) {
	parsed := map[string]map[string]string{}
	names := map[string]bool{}
	for skill, text := range byName {
		b, err := blocks(text)
		if err != nil {
			t.Fatalf("%s/SKILL.md: %v", skill, err)
		}
		parsed[skill] = b
		for name := range b {
			names[name] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no boss-core blocks found")
	}
	skills := slices.Sorted(maps.Keys(byName))
	first := skills[0]
	for name := range names {
		for _, skill := range skills {
			text, ok := parsed[skill][name]
			if !ok {
				t.Errorf("block %s: missing in %s/SKILL.md", name, skill)
				continue
			}
			if want, ok := parsed[first][name]; ok && skill != first && text != want {
				t.Errorf("block %s differs: %s/SKILL.md vs %s/SKILL.md\n%s", name, first, skill, firstDiff(want, text))
			}
		}
	}
}

func TestBlocks(t *testing.T) {
	got, err := blocks("x\n<!-- boss-core:a -->\nfoo\nbar\n<!-- /boss-core:a -->\ny\n<!-- boss-core:b -->\n<!-- /boss-core:b -->")
	if want := map[string]string{"a": "foo\nbar", "b": ""}; err != nil || !maps.Equal(got, want) {
		t.Errorf("blocks = %q, %v, want %q", got, err, want)
	}
}

func TestBlocksErrors(t *testing.T) {
	for _, tt := range []struct{ text, err string }{
		{"<!-- boss-core:a -->\nx", "block a has no end"},
		{"<!-- /boss-core:a -->", "line 1: block a ends without its start"},
		{"<!-- boss-core:a -->\n<!-- /boss-core:b -->", "line 2: block b ends without its start"},
		{"<!-- boss-core:a -->\n<!-- boss-core:b -->", "line 2: block b starts inside block a"},
		{"<!-- boss-core:a -->\n<!-- /boss-core:a -->\n<!-- boss-core:a -->\n<!-- /boss-core:a -->", "line 3: block a starts twice"},
		{"  <!-- boss-core:a -->", `line 1: stray marker "  <!-- boss-core:a -->"`},
		{"<!-- boss-core:a ->", `line 1: stray marker "<!-- boss-core:a ->"`},
	} {
		if _, err := blocks(tt.text); err == nil || err.Error() != tt.err {
			t.Errorf("blocks(%q) = %v, want %q", tt.text, err, tt.err)
		}
	}
}

// firstDiff shows the first line where a and b differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("block line %d:\n- %s\n+ %s", i+1, x, y)
		}
	}
	return ""
}

// Bosses ask the user less and where the user sees it; the hard limits stay.
func TestAskRules(t *testing.T) {
	for name, text := range byName {
		for _, want := range []string{
			"probe it yourself with bounded retries right before the step",
			"**Batch a run's outward asks:**",
			"Every one still needs the user's explicit pick.",
			"nor only as chat text ending your turn",
			"permission prompts (never answer one yourself)",
			"They never let you answer a permission prompt.",
			"probe it yourself right before the step that needs it, with bounded retries",
			"one question of at most 100 characters ending in \"?\"",
			"go in a file whose path is the last",
			"--context '<finding>'",
			"Each option is `<label> | <detail>`",
			"**A contract before a feature's workers.**",
			"**A remark on a feature whose contract is picked**",
			"**Scope gate:**", "--decided '<why it is easy>'", "start without a further ask unless their cost or scope changed since the pick", "Decisions that belong together go in one `dev todo ask`",
			"**A mockup the user must see is opened for them.**",
			`open -a "Google Chrome" <file>`,
			"**A worker's question is answered in the same turn.**",
			"never act on it, never submit or clear it",
			"**An item that waits on an ask is linked to it.**",
			"`dev todo ask --for <n>`",
			"Put the question, context and option texts in single quotes, or keep \"$\" and backticks out of them",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: missing %q", name, want)
			}
		}
	}
	// Workers keep asking as text (their boss reads it); a boss's brief says dev todo ask, so the manager must not hand
	// a boss the worker line.
	for _, want := range []string{
		"Every brief to a worker includes:\n\n> Never use AskUserQuestion: end your turn with the question as text.\n",
		"A brief to a boss (the manager's) says instead",
		"A brief you hand a boss carries the boss line",
		"Relayed worker permission prompts and dialogs (a worker's own AskUserQuestion dialog too) go through your AskUserQuestion",
		// The manager puts asks to the user itself; the shared boss rule stays dev todo ask
		"Unlike a boss (this overrides \"Never wait on a decision\" above), you put what waits on the user to them yourself with AskUserQuestion",
		"**Up to 4 asks per call, in order, one per question,** only independent ones: no pick changes another's options or whether it is needed (else one per call)",
		"A hand-on question stays alone in its call, as does an item offering \"Hand it on\": dev relays nothing from a call mixing one with other questions.",
		"Re-check each item right before each call with `dev todo show --print <boss> <n> --question \"<item text>\"`",
		"**Relay every pick of a call before the next call** (a new call's picks replace any not relayed)",
		"left unanswered in a call of several, or \"Leave it open\": the item stays open",
		"name the item's number, mark and who it waits on as dev had them when the note went (`boss-<project> waits on you: #<n> ☐ on you: …`)",
		"only the items changed since you last got it, under a count line",
		"the full list again after a compaction and at least once an hour (a rewind or a reused item number",
		"--question \"<item text>\" --via manager",
		"Never a boss's note, a recommended option you chose or a suggestion text.",
		"a message the user typed here answers no item (dev refuses it): ask that item as a question",
		"their latest pick counts also with typed messages after it (relay it, don't ask again; words that withdraw or change it need a fresh pick)",
		"with `--kind work` (their latest pick counts also with typed messages after it; words that withdraw or change it mean ask again)",
		"don't relay it or `send-keys` (dev refuses your keys to that worker until it is answered)",
		"(dev refuses any other Claude's keys to it until it is answered)",
		"1500 characters per message (longer: write it to a file and send its path)",
		"`dev todo add` refuses an item the user does (no `--on` or `--on me`, no `--waiting-for`) without `--details`, unless `--no-details`",
		"`--replaces` keeps the old item's details, so it refuses only when there were none",
		"**Each question** starts `<project> #<n>: ` and holds the item text word for word",
		"each option's detail (after ` | `) in its `description`",
		"the item's context as `- ` lines",
		"**Assess the user's ideas before handing them on.**", "Before every hand-on of new work, not only on a conflict", "holding the assessment and the brief you would send",
		"one AskUserQuestion of your own", "Drop it or skipped, it isn't handed on", "you decide yourself and report in one line",
		"you decide nothing for a project",
		"text typed under Other is a correction: revise and ask again", "Not gated: relaying the user's pick on an item", "your own easy decisions (simple or obvious; new work never is one)", "the brief you would send (the assessment and its cons in it)",
		"brief's first line stands word for word on a line of its own", "`message` or `reply` you send a boss carries `--kind`: `work` (new work, refused without that pick)", "prompt the boss (`--kind own`) with the missing fact", "`dev agent message --kind question` while it works", "`dev agent handoffs`", "--kind question \"…; answer with dev agent message manager\"",
		"ending `the manager asks the user: leave it to the manager` is the manager's",
		"**A worker's permission prompt or dialog under a boss of yours**",
		"only the answer after the colon is theirs",
		// The boss may mark an ask obvious; the manager settles a boss's ask, marked or not
		"gets `--obvious '<why>'` on its `dev todo ask` (exactly one option recommended)",
		"`settled by the manager with your recommended option: <answer>` for an ask the manager settled, marked obvious or not",
		"you decide nothing for a project but the asks you settle (below)",
		// The clear's own prompts are dev's fixed text, not the manager's sends
		"`dev agent clear`'s own prompts aren't in `dev agent handoffs`",
		"- **dev offers it:** once per idle stretch", "Run it yourself then, in the background, without asking the user, and tell them in one line; if it refuses, never force it: say why in one line",
		"or 2 minutes at a context of 40% or more with none of its asks up in your question dialog (not again for 30 minutes after an offer)",
		"prints the neutral `working: still at it after <timeout>; wait again or read it` on stdout, not an error", "who runs `dev agent clear manager` in a plain shell (a clear that passes answers it): never clear yourself",
		"- **Mark the call's items asked, right before the dialog:** one `dev todo asking <boss> <n> [<boss> <n>]... --question \"<item text>\" [--question \"<item text>\"]...` naming every item the call asks",
		// Claude Code's dim suggestion is never the user's draft
		"a row ending `[suggestion]` is Claude Code's own dim suggestion", "`input.suggestion` true in `dev agent get boss-<project> --json`",
		// Codex workers take --model and --effort; grok doesn't
		"with `--agent grok` it always fails", "`--agent codex --model <slug> --effort <effort>`, the slug one `codex debug models` lists",
		// An agy worker's decisions, relayed with the keys its lists take
		"agy's lists take the option's digit alone", "`esc` cancels a permission prompt or skips a question",
	} {
		if !strings.Contains(DevAgents, want) {
			t.Errorf("dev-agents/SKILL.md: missing %q", want)
		}
	}
	if strings.Contains(DevAgents, "Every brief includes:") {
		t.Error("dev-agents/SKILL.md: the worker's brief line isn't scoped to workers")
	}
}

// A boss that writes a worker's state into an item's text makes it look like the user's own.
func TestTodoOwnerRule(t *testing.T) {
	for name, text := range byName {
		if strings.Contains(text, "dev todo") && !strings.Contains(text, "add --on <worker name>") {
			t.Errorf("%s/SKILL.md: dev todo rule without --on for a worker's item", name)
		}
		// A boss's own or parked item and a person's must not read as free-text owners
		for _, want := range []string{"An item you do yourself or park gets `--on boss`", "`dev todo doing <n> '<what now>'`", "`--waiting-for <who>`", "never a free-text owner"} {
			if strings.Contains(text, "dev todo") && !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: owner rule missing %q", name, want)
			}
		}
	}
}

func TestTodoNoteRule(t *testing.T) {
	for name, text := range byName {
		for _, want := range []string{"dev todo note decided|said|risk", "`state` holds your standing state off the board"} {
			if strings.Contains(text, "dev todo") && !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: note rule missing %q", name, want)
			}
		}
	}
}

// Settling is the default for a cheap, reversible ask inside what the user agreed: the line and both example lists in
// every skill (shared block), the manager's settle with its one-line report in dev-agents; the old "may" wording gone.
func TestSettleRule(t *testing.T) {
	for name, text := range byName {
		for _, want := range []string{
			"- **Settle, don't ask, what is cheap, reversible and inside what the user already agreed** (a picked contract, an earlier pick, a standing rule): take your recommended option, record it at once with `dev todo ask '<question?>' --option '<pick> | <effect> (Recommended)' --option '<other> | <effect>' --decided '<why it is easy>'` (ticked, under Done as \"decided for you\" with Overrule), never only as a line in your pane, and report it in one line (a worker's question with one clear answer needs only the report above). Permission prompts and dialogs, and the manager's hand-on gate where there is one, stay as they are.",
			"  - **Settle:** rebuild a local dev server, which test key a demo container uses, a workspace or a single file for tests, keep a draft for tomorrow, add the missing test in this PR, order of agreed work.",
			"  - **Ask:** a merge into someone else's repo, a force-push, a send to a person, a settings or secrets change, new scope, taste (looks, wording to people), real money or a large share of the usage limit, anything hard to undo. When unsure which side: ask.",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: missing %q", name, want)
			}
		}
		for _, old := range []string{"An easy decision is yours", "Ask the user only for what stays theirs", "asks-stay", "settle no item on a boss's list", "(obvious: <why>)`"} {
			if strings.Contains(text, old) {
				t.Errorf("%s/SKILL.md: still has the old wording %q", name, old)
			}
		}
	}
	for _, want := range []string{
		"- **Settle, don't ask, a boss's ask that is cheap, reversible and inside what the user already agreed,** on the Settle and Ask lines of [Boss inbox](#boss-inbox), when you see no trade-off the boss missed: `dev todo answer <boss> <n> \"<recommended option>\" --recommended --question \"<item text>\" --via manager`, plus `--why '<one line>'` when its boss didn't mark it obvious",
		"Say each in your reply to the user in one line: `<boss> #<n>: took <option> over <other>; overrule on the board`.",
		"dev refuses it with no single recommended option, once answered or changed, unmarked without `--why`, and for a hand-on question; permission prompts and dialogs, and the hand-on gate, stay as they are.",
		"The manager may settle an unmarked ask too, when it is on the Settle side above.",
	} {
		if !strings.Contains(DevAgents, want) {
			t.Errorf("dev-agents/SKILL.md: missing %q", want)
		}
	}
}

// The rules of the 24.11 analysis (PR A): each is pinned in every skill, its boss-core block being shared.
func TestAnalysisRules(t *testing.T) {
	for name, text := range byName {
		for _, want := range []string{
			// P3: gated commands run bare
			"Run commands auto mode may gate (commit, push, merge, reset, kill, release) bare: no pipe, chain or redirect",
			// P4: one CI wait loop, waiting for checks to exist, ending on a pass and on a failed check
			`for i in $(seq 40); do out=$(gh pr checks <n> 2>&1); rc=$?; [ $rc = 8 ] || { [ $rc = 1 ] && [[ $out == *"no checks reported"* ]]; } || break; sleep 30; done; echo "$out"; echo "exit $rc"; (exit $rc)`,
			"it waits until checks exist, then ends on all passed or on any failed check",
			// P6: chained dev writes
			"use `&&`, never `;`, and read the item back (`dev todo show --print <boss> <n>` for a tmux boss, `dev todo --json` for a desktop one) before you tell anyone it is written",
			// P7: drafts
			"**A saved draft is `requested`,** never `replaced` or `sent`: re-read the thread in the same turn you save it, and save no draft while a pick on that item is open.",
			// P11, P15: owed Codex reviews
			"**A release, and the ask put for it, names every owed Codex review still open**",
			"> If Codex refuses with a usage limit, don't wait for the reset: merge once every other condition holds",
			`"Codex review owed (usage limit)"`,
			// P12: one restart item
			"**One restart item per list:**", "never a second item",
			// P13: optional Approaches
			"optional **Approaches** section: 3-5 clearly different approaches, one inverting an assumption, each with its strongest side and main risk, then the pick and what would move it to a bolder one",
			"the user is asked about it only when the pick is costly",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: missing %q", name, want)
			}
		}
		for _, old := range []string{"[ $? = 8 ] || break"} {
			if strings.Contains(text, old) {
				t.Errorf("%s/SKILL.md: still has the old CI wait %q", name, old)
			}
		}
	}
	// P9: the manager labels what it relays
	if want := "A count, an amount of money or a send you relay reads `per boss-<project>` or `per <list> at HH:MM`, the time from `date` run in that turn"; !strings.Contains(DevAgents, want) {
		t.Errorf("dev-agents/SKILL.md: missing %q", want)
	}
}

func TestAdvisorRules(t *testing.T) {
	for name, text := range byName {
		for _, want := range []string{
			"- **Advisor:** with an advisor tool in your session, call your advisor only before you write a contract or the options of an ask, before a release, when a worker's result contradicts its own evidence, and before a decision you settle yourself that is not plainly cheap and reversible; never on relays, state polls or list updates.",
			"start that reply with `[advisor]`",
			"While the trial runs (from the restart, one week)",
			"~/.local/state/dev/design/trial/tally.log",
			"`<your session> user-caught <what>`",
			"A session with no advisor tool skips this rule.",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s/SKILL.md: missing %q", name, want)
			}
		}
	}
	_, para, ok := strings.Cut(DevAgents, "**Call your advisor sparingly.** (This overrides the boss's Advisor rule above.)")
	if !ok {
		t.Fatal("dev-agents/SKILL.md: no manager advisor paragraph")
	}
	para, _, _ = strings.Cut(para, "\n\n")
	for _, want := range []string{
		"call your advisor only before you assess the user's new idea or request",
		"when a boss's report contradicts what its list or dev's state shows",
		"before you show the user a text meant for other people",
		"before a boss is cleared or closed",
		"when you are unsure whether something is yours to settle or the user's",
		"never on plain relays, state polls or list updates",
		"start that reply with `[advisor]`",
		"While the trial runs (from the restart, one week)",
		`echo "$(date +%F' '%H:%M) manager advisor <why, 3-6 words>" >> ~/.local/state/dev/design/trial/tally.log`,
		"`manager user-caught <what>` or `manager bad-relay <what>`",
		"A session with no advisor tool skips this rule.",
	} {
		if !strings.Contains(para, want) {
			t.Errorf("dev-agents/SKILL.md manager advisor paragraph: missing %q", want)
		}
	}
}
