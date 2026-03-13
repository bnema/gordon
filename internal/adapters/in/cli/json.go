package cli

import (
	"encoding/json"
	"io"
)

// writeJSON encodes v as indented JSON to the given writer.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
