package load

import (
	"bytes"
	"fmt"
	"html/template"
	"io"

	"github.com/bnema/gordon/internal/server"
)

// Fragment loads and renders a specific HTML fragment from the app's TemplateFS
func Fragment(a *server.App, fragmentName string, data ...interface{}) (string, error) {
	filePath := fmt.Sprintf("html/fragments/%s.gohtml", fragmentName)

	// Open the file
	file, err := a.TemplateFS.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Read the file's content
	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	// If no data is provided, just return the raw content
	if len(data) == 0 {
		return string(content), nil
	}

	// Parse the template
	tmpl, err := template.New(fragmentName).Parse(string(content))
	if err != nil {
		return "", err
	}

	// Execute the template with the provided data
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data[0])
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
