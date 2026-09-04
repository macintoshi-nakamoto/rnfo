// Package lists carries the pinned target lists, compiled into the agent.
//
// Embedding rather than shipping files is deliberate: the binary that produced
// a row is enough to reconstruct exactly which targets were measured, and a
// list refresh becomes a rebuild and a recorded event instead of a silent
// change under a running probe.
package lists

import "embed"

//go:embed *.csv MANIFEST.json
var FS embed.FS
