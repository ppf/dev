package skill

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the dev-board mod's copy of desktop-boss")

// modSkill is where the dev-board mod bundles desktop-boss, as its dev-board:boss skill.
const modSkill = "../../mods/dev-board/skills/boss/SKILL.md"

// modCopy is desktop-boss as the mod bundles it: named boss, with a note on where it comes from.
func modCopy(t *testing.T) string {
	rest, ok := strings.CutPrefix(DesktopBoss, "---\nname: desktop-boss\n")
	if !ok {
		t.Fatal("desktop-boss/SKILL.md: frontmatter doesn't start with name: desktop-boss")
	}
	head, body, ok := strings.Cut("name: boss\n"+rest, "\n---\n")
	if !ok {
		t.Fatal("desktop-boss/SKILL.md: frontmatter not closed by ---")
	}
	return "---\n" + head + "\n---\n\n<!-- Generated from internal/skill/desktop-boss/SKILL.md by `go test ./internal/skill -update`: edit that file, not this one. -->\n" + body
}

func TestModSkill(t *testing.T) {
	want := modCopy(t)
	if *update {
		if err := os.WriteFile(modSkill, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(modSkill)
	if err != nil {
		t.Fatalf("%v: run go test ./internal/skill -update", err)
	}
	if string(got) != want {
		t.Errorf("%s differs from desktop-boss/SKILL.md: run go test ./internal/skill -update\n%s", modSkill, firstDiff(want, string(got)))
	}
}
