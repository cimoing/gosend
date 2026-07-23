package web

import "embed"

// Static contains the initial Web UI. A richer frontend can replace these files
// without changing how the Go binary is deployed.
//
//go:embed static
var Static embed.FS
