package templating

import (
	"embed"
)

// Embedding localization file

//go:embed models/txt/locstrings.yml
var locFS embed.FS

// LocFS provides access to the embedded localization file.
var LocFS = locFS
