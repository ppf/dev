// Package skill holds the coordinator skill that `dev agent skill` prints.
package skill

import _ "embed"

// Text is SKILL.md: how a Claude session drives other agent sessions with dev agent.
//
//go:embed SKILL.md
var Text string
