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
			"**Scope gate:**", "**An easy decision is yours.**", "--decided '<why it is easy>'", "start without a further ask unless their cost or scope changed since the pick", "Decisions that belong together go in one `dev todo ask`",
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
		"Unlike a boss (this overrides \"Never wait on a decision\" above), you put what waits on the user to them yourself, one by one with AskUserQuestion",
		"**One question per call, in order.** Re-read `dev todo --all` right before each",
		"--question \"<item text>\" --via manager",
		"Never a boss's note, a recommended option you chose or a suggestion text.",
		"a message the user typed here answers no item (dev refuses it): ask that item as a question",
		"their latest pick counts also with typed messages after it (relay it, don't ask again; words that withdraw or change it need a fresh pick)",
		"don't relay it or `send-keys` (dev refuses your keys to that worker until it is answered)",
		"(dev refuses any other Claude's keys to it until it is answered)",
		"1500 characters per message (longer: write it to a file and send its path)",
		"`dev todo add` refuses an item the user does (no `--on` or `--on me`, no `--waiting-for`) without `--details`, unless `--no-details`",
		"`--replaces` keeps the old item's details, so it refuses only when there were none",
		"**The question** starts `<project> #<n>: ` and holds the item text word for word",
		"each option's detail (after ` | `) in its `description`",
		"the item's context as `- ` lines",
		"**Assess the user's ideas before handing them on.**", "Before every hand-on of new work, not only on a conflict", "holding the assessment and the brief you would send",
		"one AskUserQuestion of your own", "Drop it or skipped, it isn't handed on", "settle no item on a boss's list", "you decide yourself and report in one line",
		"you decide nothing for a project",
		"text typed under Other is a correction: revise and ask again", "Not gated: relaying the user's pick on an item", "your own easy decisions (simple or obvious; new work never is one)", "the brief you would send (the assessment and its cons in it)",
		"brief's first line stands word for word on a line of its own", "with `--kind work`;", "`message` or `reply` you send a boss carries `--kind`: `work` (new work, refused without that pick)", "prompt the boss (`--kind own`) with the missing fact", "`dev agent message --kind question` while it works", "`dev agent handoffs`", "--kind question \"…; answer with dev agent message manager\"",
		"ending `the manager asks the user: leave it to the manager` is the manager's",
		"**A worker's permission prompt or dialog under a boss of yours**",
		"only the answer after the colon is theirs",
		// Easy decisions part 2: the boss marks, the manager settles after its two checks
		"gets `--obvious '<why>'` on its `dev todo ask` (exactly one option recommended)",
		"`settled by the manager with your recommended option: <answer>` for an ask you marked obvious",
		"settle no item on a boss's list but an ask its boss marked obvious",
		"first that it is none of the asks-stay kinds", "then that you see no trade-off the boss missed. Either fails: put it to the user as any other ask",
		"`dev todo answer <boss> <n> \"<recommended option>\" --recommended --question \"<item text>\" --via manager`",
		"`<project> #<n>: decided for you: <pick> (obvious: <why>)`",
		// The clear's own prompts are dev's fixed text, not the manager's sends
		"`dev agent clear`'s own prompts aren't in `dev agent handoffs`",
		"- **dev offers it:** once per idle stretch", "Run it then, in the background", "who runs `dev agent clear manager` in a plain shell (a clear that passes answers it): never clear yourself",
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
