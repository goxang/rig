// Package docs carries rig's reference into the binary: rig docs, and the rig_docs MCP tool.
package docs

import "embed"

//go:embed *.md
var FS embed.FS
