// Package skills carries the agent skills rig installs into a project (rig skill --install).
package skills

import _ "embed"

//go:embed rig/SKILL.md
var Rig string
