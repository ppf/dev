// Package skill holds the coordinator skills that `dev agent skill` prints.
package skill

import (
	_ "embed"
	"fmt"
)

// DevAgents is dev-agents/SKILL.md: how a Claude session drives other agent sessions with dev agent.
//
//go:embed dev-agents/SKILL.md
var DevAgents string

// DesktopBoss is desktop-boss/SKILL.md: how a Claude desktop-app session drives worker sessions with the app's tools.
//
//go:embed desktop-boss/SKILL.md
var DesktopBoss string

// T3Boss is t3-boss/SKILL.md: how a T3 Code thread drives worker threads with T3 Code's own tools.
//
//go:embed t3-boss/SKILL.md
var T3Boss string

// Default is the skill `dev agent skill` prints without a name.
const Default = "dev-agents"

var byName = map[string]string{"dev-agents": DevAgents, "desktop-boss": DesktopBoss, "t3-boss": T3Boss}

// Text returns the skill named name (its directory).
func Text(name string) (string, error) {
	text, ok := byName[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q, want dev-agents, desktop-boss or t3-boss", name)
	}
	return text, nil
}
