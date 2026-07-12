// Package assets embeds small static files the server hands out directly,
// like install.sh, so there's no separate static-file directory to manage
// on Render (or wherever this gets deployed) — it's just part of the binary.
package assets

import _ "embed"

//go:embed install.sh
var InstallScript []byte

//go:embed install.ps1
var InstallScriptPS1 []byte
