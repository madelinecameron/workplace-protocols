package protocol

import "embed"

// Schemas contains the language-neutral JSON Schema for the current Version.
//
//go:embed schema/v2/*.json
var Schemas embed.FS
