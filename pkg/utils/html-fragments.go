package utils

import (
	"bytes"
	"fmt"

	"github.com/PuerkitoBio/goquery"
	"gogs.bnema.dev/gordon-echo/internal/ui"
)

// GetHTMLFragmentByID returns the HTML fragment with the specified id
func GetHTMLFragmentByID(id string, data interface{}) (string, error) {
	// 1. Render the template
	renderer, err := GetRenderer("components.gohtml", ui.TemplateFS, NewLogger())
	if err != nil {
		return "", fmt.Errorf("failed to get renderer: %w", err)
	}

	renderedHTML, err := renderer.Render(data)
	if err != nil {
		return "", fmt.Errorf("failed to render template: %w", err)
	}

	// 2. Parse the rendered HTML with goquery
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(renderedHTML)))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML document: %w", err)
	}

	// 3. Find the div with the specified id
	fragmentContent, err := doc.Find("#" + id).Html()
	if err != nil {
		return "", fmt.Errorf("failed to extract HTML for id %s: %w", id, err)
	}

	if fragmentContent == "" {
		return "", fmt.Errorf("element with id %s not found", id)
	}

	return fragmentContent, nil
}
