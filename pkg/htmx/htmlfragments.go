package htmx

import (
	"bytes"
	"fmt"

	"github.com/PuerkitoBio/goquery"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/templating/render"
)

// GetHTMLFragmentByID returns the HTML fragment with the specified id
func GetHTMLFragmentByID(id string, data interface{}, a *app.App) (string, error) {
	// 1. Render the template
	renderer, err := render.GetHTMLRenderer("html/fragments", "components.gohtml", a.TemplateFS, a)
	if err != nil {
		return "", fmt.Errorf("failed to get renderer: %w", err)
	}

	renderedHTML, err := renderer.Render(data, a)
	if err != nil {
		return "", fmt.Errorf("failed to render template: %w", err)
	}

	// 2. Parse the rendered HTML with goquery
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(renderedHTML)))
	if err != nil {
		return "", fmt.Errorf("failed to parse rendered HTML: %w", err)
	}

	// 3. Find the div with the specified id
	fragmentContent, err := doc.Find("#" + id).Html()
	if err != nil {
		return "", fmt.Errorf("failed to find fragment with id %s: %w", id, err)
	}

	if fragmentContent == "" {
		return "", fmt.Errorf("failed to find fragment with id %s: %w", id, err)
	}

	return fragmentContent, nil
}
