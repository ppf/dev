package sidebar

import (
	"fmt"
	"strings"
)

// ProjectColour is a project's colour on the boards: Light and Dark for the page's two themes, Dark on the terminal.
type ProjectColour struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// projectPresets are the design's own colours, by a project name's first part.
var projectPresets = map[string]ProjectColour{
	"dev":     {"#2c64c9", "#5b8be6"},
	"nocworx": {"#187650", "#3aa477"},
	"vitay":   {"#9b3db5", "#c678dd"}, // orchid: brown would read like the boards' amber
}

// projectSpares are the other projects' colours, apart from the boards' amber, green, red and grey: teal, cyan,
// periwinkle, magenta, lilac, ice, pink, violet.
var projectSpares = []ProjectColour{
	{"#0f7d70", "#43b8a6"},
	{"#086a85", "#5cc8e0"},
	{"#4246c0", "#9a9cf0"},
	{"#96266f", "#e070c0"},
	{"#8f6ad8", "#c9b3f5"},
	{"#4f8fae", "#a8dcef"},
	{"#d0609f", "#f2a6d8"},
	{"#4f2ca0", "#8a6ee0"},
}

// BoardProject is a list's project as the boards name it, as model.js project does.
func BoardProject(boss string) string {
	if strings.HasPrefix(boss, "local_") {
		return "desktop"
	}
	return strings.TrimPrefix(boss, "boss-")
}

// ProjectColourOf is a project's colour by its name's first part: a preset, else a spare by hash, so never by position.
func ProjectColourOf(project string) ProjectColour {
	part, _, _ := strings.Cut(project, "-")
	if c, ok := projectPresets[part]; ok {
		return c
	}
	h := uint32(7)
	for _, r := range part {
		h = h*31 + uint32(r)
	}
	return projectSpares[h%uint32(len(projectSpares))]
}

// ANSI is the colour's terminal escape, its Dark one.
func (c ProjectColour) ANSI() string {
	var r, g, b uint8
	if _, err := fmt.Sscanf(c.Dark, "#%02x%02x%02x", &r, &g, &b); err != nil {
		panic("project colour " + c.Dark + ": " + err.Error())
	}
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}
