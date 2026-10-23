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
