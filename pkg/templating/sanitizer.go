package templating

import (
	"fmt"

	"github.com/microcosm-cc/bluemonday"
)

// SanitizeHTML sanitizes HTML input using bluemonday package and returns an error if sanitization altered the input.
func SanitizeHTML(htmlInput string) (string, error) {
	// Create a new UGC policy
	p := bluemonday.UGCPolicy()

	// Sanitize the input HTML
	sanitized := p.Sanitize(htmlInput)

	if sanitized != htmlInput {
		return sanitized, fmt.Errorf("HTML input was altered during sanitization")
	}

	return sanitized, nil
}
