package render

import (
	"bytes"
	"fmt"
	"github.com/bnema/gordon/internal/app"
	"html/template"
	"io/fs"
	"path"
)

type Renderer struct {
	Template   *template.Template
	ParseError error
}

// Render function renders the template with the given data
func (r *Renderer) Render(data interface{}, a *app.App) (string, error) {
	if r.ParseError != nil {
		return "", fmt.Errorf("failed to parse template: %w", r.ParseError)

	}
	if r.Template == nil {
		return "", fmt.Errorf("template is nil")
	}

	buf := new(bytes.Buffer)
	err := r.Template.Execute(buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// GetRenderer function returns a new Renderer instance
func GetHTMLRenderer(pathStr string, filename string, fs fs.FS, a *app.App) (*Renderer, error) {
	// Join path and filename
	fullPath := path.Join(pathStr, filename)
	// Check if the file exists in the provided fs.FS using fs.Open
	file, err := fs.Open(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", filename, err)
	}
	file.Close() // Close the file after checking
	tmpl, err := template.New(filename).ParseFS(fs, fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", filename, err)
	}
	return &Renderer{
		Template: tmpl,
	}, nil
}
