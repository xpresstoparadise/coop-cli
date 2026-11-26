// Package coopcli holds files shipped inside the coop binary.
package coopcli

import _ "embed"

// Skill is the agent skill (skills/coop/SKILL.md), the single source for
// every agent's instructions written by `coop setup`.
//
//go:embed skills/coop/SKILL.md
var Skill string
