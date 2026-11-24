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

// Default is the skill `dev agent skill` prints without a name.
const Default = "dev-agents"

var byName = map[string]string{"dev-agents": DevAgents, "desktop-boss": DesktopBoss}

// Text returns the skill named name (its directory).
func Text(name string) (string, error) {
	text, ok := byName[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q, want dev-agents or desktop-boss", name)
	}
	return text, nil
}
