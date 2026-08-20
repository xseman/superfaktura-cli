// Package skills carries the agent skill that ships with the binary, so a
// user who installed a release tarball can still install the skill without
// cloning the repository.
package skills

import _ "embed"

// SuperFaktura is the skill document describing how to drive sf.
//
//go:embed superfaktura/SKILL.md
var SuperFaktura string

// Name is the directory the skill installs into.
const Name = "superfaktura"
