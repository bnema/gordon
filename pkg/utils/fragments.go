package utils

import (
	"fmt"

	"github.com/PuerkitoBio/goquery"
	"gogs.bnema.dev/gordon-echo/internal/ui"
)

// GetHTMLFragmentByID returns the HTML fragment with the specified id
func GetHTMLFragmentByID(id string) (string, error) {
	data, err := ui.TemplateFS.Open("components.html")
	if err != nil {
		return "", fmt.Errorf("failed to open components.html: %w", err)
	}
	defer data.Close()

	doc, err := goquery.NewDocumentFromReader(data)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML document: %w", err)
	}

	// Find the div with the specified id
	fragmentContent, err := doc.Find("#" + id).Html()
	if err != nil {
		return "", fmt.Errorf("failed to extract HTML for id %s: %w", id, err)
	}

	if fragmentContent == "" {
		return "", fmt.Errorf("element with id %s not found", id)
	}

	return fragmentContent, nil
}
