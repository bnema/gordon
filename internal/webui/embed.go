package webui

import "embed"

// PublicFS holds the embedded static file system for the web UI.
// The path "public" is relative to this package directory (internal/webui).
//
//go:embed public
var PublicFS embed.FS
