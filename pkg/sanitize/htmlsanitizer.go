package sanitize

import (
	"errors"

	"github.com/microcosm-cc/bluemonday"
)

// SanitizeHTML takes a string containing raw HTML and returns a sanitized string.
func SanitizeHTML(rawHTML string) (string, error) {
	// Use bluemonday's UGCPolicy for user generated content
	p := bluemonday.UGCPolicy()

	// Sanitize the raw HTML string
	sanitized := p.Sanitize(rawHTML)

	if sanitized == "" && rawHTML != "" {
		return "", errors.New("Sanitization removed all content")
	}

	return sanitized, nil
}
