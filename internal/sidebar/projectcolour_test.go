package sidebar

import (
	"maps"
	"slices"
	"testing"
)

// A project's colour goes by its name's first part: presets for dev, nocworx and vitay, a fixed spare for the rest, the
// same every call, whatever other projects there are.
func TestProjectColourOf(t *testing.T) {
	for name, want := range map[string]string{
		"dev":          "#5b8be6",
		"nocworx-core": "#3aa477",
		"nocworx":      "#3aa477",
		"vitay":        "#c678dd",
		"stalgast-b2b": "#f2a6d8", // spare 6
		"stalgast-de":  "#f2a6d8",
		"terminal":     "#e070c0", // spare 3
		"kuehne":       "#e070c0",
		"docworx":      "#8a6ee0", // spare 7
		"":             "#8a6ee0",
		"desktop":      "#a8dcef",
		"local_api":    "#8a6ee0", // spare 7: a project of that name, not desktop
		"boss-vitay":   "#c9b3f5", // spare 4 (boss): a project of that name, not vitay
	} {
		if got := ProjectColourOf(name).Dark; got != want {
			t.Errorf("%q: %s, want %s", name, got, want)
		}
	}
	if got := ProjectColourOf("vitay").ANSI(); got != "\033[38;2;198;120;221m" {
		t.Errorf("vitay ANSI %q", got)
	}
	for _, c := range append(slices.Collect(maps.Values(projectPresets)), projectSpares...) {
		_ = c.ANSI() // panics on a malformed hex
		if c.Light == "" {
			t.Errorf("%v: no light colour", c)
		}
	}
}

func TestBoardProject(t *testing.T) {
	for boss, want := range map[string]string{"boss-nocworx-core": "nocworx-core", "local_ab12": "desktop", "manager": "manager"} {
		if got := BoardProject(boss); got != want {
			t.Errorf("%q: %q, want %q", boss, got, want)
		}
	}
}
