package htmx

import (
	"bytes"

	"github.com/PuerkitoBio/goquery"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/templating"
)

// GetHTMLFragmentByID returns the HTML fragment with the specified id
func GetHTMLFragmentByID(id string, data interface{}, a *app.App, ac *app.Config) (string, error) {
	// 1. Render the template
	renderer, err := templating.GetHTMLRenderer("components.gohtml", ac.GetTemplateFS(), a)
	if err != nil {
		return "", err
	}

	renderedHTML, err := renderer.Render(data, a)
	if err != nil {
		return "", err
	}

	// 2. Parse the rendered HTML with goquery
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(renderedHTML)))
	if err != nil {
		return "", err
	}

	// 3. Find the div with the specified id
	fragmentContent, err := doc.Find("#" + id).Html()
	if err != nil {
		return "", err
	}

	if fragmentContent == "" {
		return "", err
	}

	return fragmentContent, nil
}
